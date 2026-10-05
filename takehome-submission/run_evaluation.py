#!/usr/bin/env python3
"""Run the take-home questions through Nano Notebook's real local HTTP product."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import http.cookiejar
import json
from pathlib import Path
import re
import secrets
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid


TERMINAL = {"completed", "failed", "cancelled"}
ROOT = Path(__file__).resolve().parent.parent


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def atomic_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=path.parent, delete=False) as stream:
        temporary = Path(stream.name)
        json.dump(value, stream, ensure_ascii=False, indent=2)
        stream.write("\n")
        stream.flush()
    temporary.replace(path)


class API:
    def __init__(self, base: str) -> None:
        self.base = base.rstrip("/")
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))

    def call(self, path: str, data: object | None = None, method: str | None = None) -> dict:
        headers = {"Content-Type": "application/json", "Idempotency-Key": str(uuid.uuid4())}
        for cookie in self.jar:
            if cookie.name == "nn_csrf":
                headers["X-CSRF-Token"] = cookie.value
        body = None if data is None else json.dumps(data, ensure_ascii=False).encode()
        request = urllib.request.Request(
            self.base + path,
            data=body,
            headers=headers,
            method=method or ("POST" if data is not None else "GET"),
        )
        try:
            with self.opener.open(request, timeout=180) as response:
                payload = response.read()
                return json.loads(payload) if payload else {}
        except urllib.error.HTTPError as error:
            safe_body = error.read(2000).decode(errors="replace")
            raise RuntimeError(f"API {path}: HTTP {error.code}: {safe_body}") from None

    def upload_markdown(self, notebook_id: str, path: Path) -> str:
        content = path.read_bytes()
        intent = self.call(
            f"/api/v1/notebooks/{notebook_id}/sources/upload-intents",
            {
                "title": path.name,
                "format": "markdown",
                "media_type": "text/markdown",
                "byte_size": len(content),
                "content_sha256": sha256(content),
            },
        )
        boundary = uuid.uuid4().hex
        body = bytearray()
        for key, value in intent["upload"]["fields"].items():
            body.extend(
                f'--{boundary}\r\nContent-Disposition: form-data; name="{key}"\r\n\r\n{value}\r\n'.encode()
            )
        body.extend(
            f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{path.name}"\r\n'
            "Content-Type: text/markdown\r\n\r\n".encode()
        )
        body.extend(content)
        body.extend(f"\r\n--{boundary}--\r\n".encode())
        upload = urllib.request.Request(
            intent["upload"]["url"],
            bytes(body),
            {"Content-Type": "multipart/form-data; boundary=" + boundary},
            method=intent["upload"]["method"],
        )
        with urllib.request.urlopen(upload, timeout=180) as response:
            response.read()
        finalized = self.call(f'/api/v1/source-upload-intents/{intent["upload_intent"]["id"]}/finalize', {})
        return finalized["source"]["id"]


def wait_for_sources(api: API, notebook_id: str, expected: int, timeout: int) -> list[dict]:
    deadline = time.monotonic() + timeout
    while True:
        sources = api.call(f"/api/v1/notebooks/{notebook_id}/sources")["sources"]
        failed = [source for source in sources if source["state"] == "failed"]
        if failed:
            raise RuntimeError("source processing failed: " + ", ".join(source["title"] for source in failed))
        if len(sources) == expected and all(source["state"] == "ready" for source in sources):
            return sources
        if time.monotonic() >= deadline:
            states = {source["title"]: source["state"] for source in sources}
            raise TimeoutError(f"sources did not become ready: {states}")
        time.sleep(2)


def sql(query: str) -> str:
    """Read only from the local Compose database; never reads provider credentials."""
    return subprocess.check_output(
        [
            "docker",
            "compose",
            "-f",
            str(ROOT / "infra/compose/compose.yaml"),
            "exec",
            "-T",
            "postgres",
            "psql",
            "-X",
            "-v",
            "ON_ERROR_STOP=1",
            "-U",
            "nano",
            "-d",
            "nano",
            "-Atc",
            query,
        ],
        text=True,
    ).strip()


def safe_identity(value: str) -> str:
    if not re.fullmatch(r"[A-Za-z0-9_-]+", value):
        raise ValueError("invalid product identity")
    return value


def wait_for_run(run_id: str, timeout: int) -> str:
    run_id = safe_identity(run_id)
    deadline = time.monotonic() + timeout
    while True:
        status = sql(f"select status from agent_runs where id='{run_id}'")
        if status in TERMINAL:
            return status
        if time.monotonic() >= deadline:
            raise TimeoutError(f"run {run_id} did not finish")
        time.sleep(2)


def collect_result(chat_id: str, run_id: str) -> dict:
    chat_id = safe_identity(chat_id)
    run_id = safe_identity(run_id)
    value = sql(
        "select json_build_object("
        "'status',r.status,'answer',coalesce(m.content,''),'citations',"
        "coalesce((select json_agg(json_build_object("
        "'message_id',c.message_id,'source_id',c.source_id,'source_title',s.title,"
        "'unit_id',c.unit_id,'start_rune',c.start_rune,'end_rune',c.end_rune,"
        "'reference_kind',c.reference_kind,'preview',coalesce(u.text_content,'')) "
        "order by coalesce(c.reference_ordinal,c.claim_ordinal),coalesce(c.citation_ordinal,0)) "
        "from chat_citations c left join source_sources s on s.id=c.source_id "
        "left join source_evidence_units u on u.id=c.unit_id where c.run_id=r.id),'[]'::json)) "
        "from agent_runs r left join chat_messages m on m.id=r.output_message_id "
        f"where r.id='{run_id}' and r.chat_id='{chat_id}'"
    )
    if not value:
        raise RuntimeError(f"run {run_id} was not found")
    return json.loads(value)


def render_markdown(run: dict) -> str:
    lines = [
        "# AtlasDesk 知识库问答实际输出",
        "",
        f"- 运行时间：{run['started_at']}",
        f"- 知识库文件数：{run['knowledge_base_file_count']}",
        f"- 程序：Nano Notebook 真实 HTTP API + Worker",
        "",
    ]
    for index, result in enumerate(run["results"], start=1):
        lines.extend(
            [
                f"## {index}. {result['question']}",
                "",
                f"- Case ID：`{result['case_id']}`",
                f"- 分类：`{result['category']}`",
                f"- Run 状态：`{result['status']}`",
                f"- 用时：{result['elapsed_seconds']:.2f} 秒",
                "",
                "### 程序原始回答",
                "",
                result["answer"] or "（没有发布回答）",
                "",
                "### 程序返回的来源引用",
                "",
            ]
        )
        if result["citations"]:
            for citation in result["citations"]:
                location = []
                if citation.get("unit_id"):
                    location.append(f"unit={citation['unit_id']}")
                if citation.get("start_rune") is not None and citation.get("end_rune") is not None:
                    location.append(f"runes={citation['start_rune']}..{citation['end_rune']}")
                suffix = f"（{', '.join(location)}）" if location else ""
                lines.append(f"- `{citation.get('source_title') or citation['source_id']}`{suffix}")
        else:
            lines.append("- 无（知识库无答案用例期望不引用无关文档）")
        lines.extend(["", "---", ""])
    return "\n".join(lines)


def run(args: argparse.Namespace) -> None:
    suite = json.loads(args.questions.read_text(encoding="utf-8"))
    documents = sorted(
        path for path in args.knowledge_base.glob("*.md") if path.name != "GENERATION_PROMPT.md"
    )
    if not 30 <= len(documents) <= 50:
        raise ValueError(f"expected 30-50 knowledge-base documents, got {len(documents)}")
    api = API(args.api)
    started = dt.datetime.now(dt.timezone.utc).isoformat()
    api.call(
        "/api/v1/auth/register",
        {
            "email": f"takehome-{uuid.uuid4().hex}@example.test",
            "password": "Takehome!" + secrets.token_urlsafe(24),
        },
    )
    notebook_id = api.call("/api/v1/notebooks", {"title": "AtlasDesk take-home evaluation"})["notebook"]["id"]
    print(f"created notebook {notebook_id}", flush=True)

    uploaded = {}
    for index, path in enumerate(documents, start=1):
        uploaded[path.name] = api.upload_markdown(notebook_id, path)
        print(f"uploaded {index}/{len(documents)} {path.name}", flush=True)
    sources = wait_for_sources(api, notebook_id, len(documents), args.source_timeout)
    source_ids = [source["id"] for source in sources]
    print(f"all {len(source_ids)} sources ready", flush=True)

    record = {
        "schema_version": 1,
        "started_at": started,
        "knowledge_base_file_count": len(documents),
        "knowledge_base_sha256": sha256(
            "\n".join(path.name + ":" + sha256(path.read_bytes()) for path in documents).encode()
        ),
        "question_suite_sha256": sha256(args.questions.read_bytes()),
        "results": [],
    }
    args.output.mkdir(parents=True, exist_ok=True)
    for index, case in enumerate(suite["cases"], start=1):
        chat_id = api.call(f"/api/v1/notebooks/{notebook_id}/chats", {})["chat"]["id"]
        before = time.monotonic()
        admitted = api.call(
            f"/api/v1/chats/{chat_id}/messages",
            {
                "id": str(uuid.uuid4()),
                "content": case["question"],
                "time_zone": "America/Los_Angeles",
                "mode": "chat",
                "source_ids": source_ids,
            },
        )
        status = wait_for_run(admitted["run_id"], args.run_timeout)
        product = collect_result(chat_id, admitted["run_id"])
        result = {
            "case_id": case["id"],
            "category": case["category"],
            "question": case["question"],
            "expected_sources": case.get("expected_sources", []),
            "run_id": admitted["run_id"],
            "status": product["status"],
            "elapsed_seconds": round(time.monotonic() - before, 3),
            "answer": product["answer"],
            "citations": product["citations"],
        }
        record["results"].append(result)
        atomic_json(args.output / "actual-outputs.json", record)
        (args.output / "actual-outputs.md").write_text(render_markdown(record), encoding="utf-8")
        print(f"finished {index}/{len(suite['cases'])} {case['id']} {status}", flush=True)


def main() -> None:
    here = Path(__file__).resolve().parent
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--api", default="http://127.0.0.1:8080")
    parser.add_argument("--knowledge-base", type=Path, default=here / "knowledge-base")
    parser.add_argument("--questions", type=Path, default=here / "test-cases/questions.json")
    parser.add_argument("--output", type=Path, default=here / "test-results")
    parser.add_argument("--source-timeout", type=int, default=1800)
    parser.add_argument("--run-timeout", type=int, default=600)
    args = parser.parse_args()
    if args.api != "http://127.0.0.1:8080":
        parser.error("this runner is intentionally restricted to the local product")
    run(args)


if __name__ == "__main__":
    main()
