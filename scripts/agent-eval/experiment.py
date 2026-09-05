#!/usr/bin/env python3
"""Local product-API research experiment; no replacement Agent implementation."""
import argparse
import concurrent.futures
import fcntl
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[2]
TERMINAL = {"completed", "failed", "cancelled"}


def digest(data):
    return hashlib.sha256(data).hexdigest()


def save_new(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=path.parent, delete=False) as stream:
            temporary = stream.name
            json.dump(value, stream, ensure_ascii=False, indent=2)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.link(temporary, path)
    finally:
        if temporary:
            os.unlink(temporary)


def load_suite(path):
    suite = json.loads(path.read_text())
    if suite.get("schema_version") != 1 or type(suite.get("repeats")) is not int or suite["repeats"] < 1:
        raise ValueError("invalid suite header")
    seen = set()
    for case in suite["cases"]:
        if (not re.fullmatch(r"[a-z0-9-]+", case["id"]) or case["id"] in seen
                or case["category"] not in {"single", "comparison", "open"}
                or case["mode"] not in {"chat", "research"}
                or not case["prompt"] or not case["rubric"]):
            raise ValueError("invalid or duplicate case")
        seen.add(case["id"])
        for source in case["sources"]:
            if source not in suite["sources"]:
                raise ValueError("unknown source")
    if not seen:
        raise ValueError("empty suite")
    return suite


def summarize(cases, rows, k):
    if type(k) is not int or k < 1 or not cases:
        raise ValueError("invalid task count/repeats")
    ids = {case["id"] for case in cases}
    if len(ids) != len(cases):
        raise ValueError("duplicate cases")
    indexed = {}
    for row in rows:
        key = (row["case_id"], row["trial"])
        if (key in indexed or key[0] not in ids or type(key[1]) is not int
                or not 1 <= key[1] <= k or type(row.get("pass")) is not bool):
            raise ValueError("unknown, duplicated, invalid or ungraded trial")
        indexed[key] = row["pass"]
    if len(indexed) != len(cases) * k:
        raise ValueError("incomplete experiment; aggregate scores withheld")

    def metrics(selected):
        counts = [sum(indexed[(case["id"], n)] for n in range(1, k + 1)) for case in selected]
        return {"tasks": len(counts), "trials": len(counts) * k,
                "pass_at_1": sum(counts) / (len(counts) * k),
                "pass_at_k": sum(c > 0 for c in counts) / len(counts),
                "pass_pow_k": sum(c == k for c in counts) / len(counts)}

    return {"overall": metrics(cases), "categories": {
        category: metrics([case for case in cases if case["category"] == category])
        for category in sorted({case["category"] for case in cases})}}


def verdict(case, observation, grade):
    checks = grade.get("checks")
    if (not isinstance(checks, list) or len(checks) != len(case["rubric"])
            or any(type(c) is not bool for c in checks)
            or any(type(grade.get(key)) is not bool for key in ["evidence_supported", "no_fabrication"])):
        raise ValueError("every rubric item and evidence check needs a boolean verdict")
    return (observation["status"] == "completed" and bool(observation["answer"].strip())
            and all(checks) and grade["evidence_supported"] and grade["no_fabrication"])


def verify_grade_observation(grade, observation_bytes):
    if grade.get("observation_sha256") != digest(observation_bytes):
        raise ValueError("grade does not match the observed output")


def can_retry_preparation(admitted, states):
    return not admitted and bool(states) and "failed" in states and all(s in {"ready", "failed"} for s in states)


def verify_preparation_retry(key, out):
    if not re.fullmatch(r"[a-z0-9-]+", key):
        raise ValueError("invalid preparation key")
    state = json.loads(sql("select json_build_object('states',coalesce(json_agg(s.state),'[]'::json),"
        "'admitted',exists(select 1 from chat_chats c join chat_messages m on m.chat_id=c.id "
        "join notebook_notebooks n2 on n2.id=c.notebook_id "
        f"where n2.title='Research evaluation {key}')) from source_sources s "
        f"join notebook_notebooks n on n.id=s.notebook_id where n.title='Research evaluation {key}'"))
    if (out / "admissions" / (key + ".json")).exists() or not can_retry_preparation(state["admitted"], state["states"]):
        raise ValueError("cannot retry admitted, active or unverified preparation: " + key)
    save_new(out / "preparation-retries" / (key + ".json"), state)


def sql(query):
    """Read only, local Compose database, with no provider credential access."""
    return subprocess.check_output([
        "docker", "compose", "-f", str(ROOT / "infra/compose/compose.yaml"),
        "exec", "-T", "postgres", "psql", "-X", "-v", "ON_ERROR_STOP=1",
        "-U", "nano", "-d", "nano", "-Atc", query], text=True).strip()


def run_binding(run_id):
    if not re.fullmatch(r"[a-zA-Z0-9_-]+", run_id):
        raise ValueError("invalid run id")
    return json.loads(sql("select json_build_object('definition',definition_identity||'@'||definition_version,"
                          "'model_policy',model_policy_identity||'@'||model_policy_version,"
                          "'model',provider_model,'definition_sha256',definition_sha256,"
                          "'model_policy_sha256',model_policy_sha256,"
                          "'context_policy_sha256',model_context_policy_sha256,"
                          "'release',parent_context_manifest->>'agent_release') "
                          f"from agent_runs where id='{run_id}'"))


def product_result(admission, case):
    """Observe already admitted work; never create or retry an Agent run."""
    chat_id = admission["chat_id"]
    run_id = admission["run_id"]
    for value in [chat_id, run_id]:
        if not re.fullmatch(r"[a-zA-Z0-9_-]+", value):
            raise ValueError("invalid product identity")
    plan = None
    bindings = [run_binding(run_id)]
    if case["mode"] == "research":
        session_id = admission["research_session_id"]
        if not re.fullmatch(r"[a-zA-Z0-9_-]+", session_id):
            raise ValueError("invalid session identity")
        value = json.loads(sql("select json_build_object('status',s.status,'execution_run_id',s.execution_run_id,"
            "'plan',(select plan_json from research_plan_versions where session_id=s.id order by version desc limit 1),"
            "'answer',coalesce((select content_markdown from research_report_versions where session_id=s.id "
            "order by version desc limit 1),'')) "
            f"from research_sessions s where s.id='{session_id}' and s.chat_id='{chat_id}'"))
        status, answer, plan = value["status"], value["answer"], value["plan"]
        if status == "awaiting_confirmation" and plan and plan.get("clarifying_questions"):
            status = "clarification_required"
        elif status not in TERMINAL:
            return None
        if value["execution_run_id"]:
            bindings.append(run_binding(value["execution_run_id"]))
    else:
        status = sql(f"select status from agent_runs where id='{run_id}'")
        if status not in TERMINAL:
            return None
        answer = json.loads(sql("select coalesce(json_agg(content order by created_at,id),'[]'::json) "
                                f"from chat_messages where chat_id='{chat_id}' and role='assistant'"))
        answer = "\n\n".join(answer)
    citations = json.loads(sql("select coalesce(json_agg(json_build_object('citation',to_jsonb(c),"
        "'source_title',s.title,'preview',coalesce(u.text_content,'')) order by c.citation_id),'[]'::json) "
        "from chat_citations c join chat_messages m on m.id=c.message_id "
        "left join source_sources s on s.id=c.source_id "
        "left join source_evidence_units u on u.id=c.unit_id "
        f"where m.chat_id='{chat_id}'"))
    return {"status": status, "answer": answer, "plan": plan, "citations": citations, "bindings": bindings}


def baseline(suite, suite_path):
    files = subprocess.check_output(["git", "ls-files", "internal", "cmd/control-plane",
                                     "cmd/worker", "go.mod", "go.sum"], cwd=ROOT, text=True).splitlines()
    code = digest("\n".join(path + ":" + digest((ROOT / path).read_bytes()) for path in files).encode())
    return {"commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
            "runtime_source_sha256": code, "suite_sha256": digest(suite_path.read_bytes()),
            "release": suite["release"], "repeats": suite["repeats"],
            "sampling": "Unchanged product model policies; temperature 0; empirical repeatability, not an IID guarantee."}


class API:
    def __init__(self, base):
        self.base = base.rstrip("/")
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))

    def call(self, path, data=None, method=None):
        headers = {"Content-Type": "application/json", "Idempotency-Key": str(uuid.uuid4())}
        for cookie in self.jar:
            if cookie.name == "nn_csrf":
                headers["X-CSRF-Token"] = cookie.value
        request = urllib.request.Request(self.base + path,
                                         data=None if data is None else json.dumps(data).encode(),
                                         headers=headers, method=method or ("POST" if data is not None else "GET"))
        try:
            with self.opener.open(request, timeout=180) as response:
                body = response.read()
                return json.loads(body) if body else {}
        except urllib.error.HTTPError as error:
            # Product errors contain safe error codes; do not log request headers/passwords.
            raise RuntimeError(f"API {path}: HTTP {error.code}: {error.read(2000).decode()}") from None

    def upload(self, notebook, path):
        content = path.read_bytes()
        intent = self.call(f"/api/v1/notebooks/{notebook}/sources/upload-intents", {
            "title": path.name, "format": "pdf", "media_type": "application/pdf",
            "byte_size": len(content), "content_sha256": digest(content)})
        boundary = uuid.uuid4().hex
        body = bytearray()
        for key, value in intent["upload"]["fields"].items():
            body.extend(f'--{boundary}\r\nContent-Disposition: form-data; name="{key}"\r\n\r\n{value}\r\n'.encode())
        body.extend(f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{path.name}"\r\nContent-Type: application/pdf\r\n\r\n'.encode())
        body.extend(content)
        body.extend(f'\r\n--{boundary}--\r\n'.encode())
        request = urllib.request.Request(intent["upload"]["url"], bytes(body),
                                         {"Content-Type": "multipart/form-data; boundary=" + boundary},
                                         method=intent["upload"]["method"])
        with urllib.request.urlopen(request, timeout=180) as response:
            response.read()
        return self.call(f'/api/v1/source-upload-intents/{intent["upload_intent"]["id"]}/finalize', {})["source"]["id"]


def wait_for(get, done, seconds):
    deadline = time.monotonic() + seconds
    while True:
        value = get()
        if done(value):
            return value
        if time.monotonic() >= deadline:
            raise TimeoutError("product did not reach a terminal state; trial remains incomplete")
        time.sleep(2)


def prepare_sources(suite, cache):
    cache.mkdir(parents=True, exist_ok=True)
    manifest = {}
    for key, source in suite["sources"].items():
        path = cache / (key + ".pdf")
        if not path.exists():
            request = urllib.request.Request(source["url"], headers={"User-Agent": "NanoNotebookResearchEval/1.0"})
            with urllib.request.urlopen(request, timeout=120) as response:
                content = response.read(30 * 1024 * 1024)
            if not content.startswith(b"%PDF-"):
                raise ValueError("source download is not a PDF: " + key)
            with path.open("xb") as stream:
                stream.write(content)
        manifest[key] = {**source, "sha256": digest(path.read_bytes())}
    return manifest


def verify_live_baseline(proof):
    processes = proof["processes"]
    if {p["port"] for p in processes} != {8080, 8081} or len(processes) != 2:
        raise ValueError("both baseline services must be pinned")
    for process in processes:
        command = subprocess.check_output(["ps", "-p", str(process["pid"]), "-o", "command="], text=True).strip()
        started = subprocess.check_output(["ps", "-p", str(process["pid"]), "-o", "lstart="], text=True).strip()
        listeners = subprocess.check_output(["lsof", "-t", "-nP", f'-iTCP:{process["port"]}',
                                             "-sTCP:LISTEN"], text=True).splitlines()
        if (command != process["executable"] or started != process["started"]
                or str(process["pid"]) not in listeners
                or digest(Path(command).read_bytes()) != process["sha256"]):
            raise ValueError("live baseline process changed")


def trial(case, number, suite, cache, out, base, retry_preparation=False, live_proof=None):
    if live_proof:
        verify_live_baseline(live_proof)
    key = f'{case["id"]}-{number}'
    result_path = out / "observations" / (key + ".json")
    if result_path.exists():
        return key + " already recorded"
    # A durable reservation prevents a crash/resume from silently resampling a trial.
    reservation = out / "started" / (key + ".json")
    if reservation.exists() and retry_preparation:
        verify_preparation_retry(key, out)
    else:
        save_new(reservation, {"case_id": case["id"], "trial": number})
    api = API(base)
    api.call("/api/v1/auth/register", {"email": f"eval-{uuid.uuid4().hex}@example.test",
                                       "password": "Eval!" + secrets.token_urlsafe(24)})
    notebook = api.call("/api/v1/notebooks", {"title": "Research evaluation " + key})["notebook"]["id"]
    source_ids = []
    if case["sources"]:
        # Preparation is not an Agent attempt. Serialize PDF admission across
        # experiment processes to respect the existing renderer's small capacity.
        with (cache / "preparation.lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            for source in case["sources"]:
                source_id = api.upload(notebook, cache / (source + ".pdf"))
                source_ids.append(source_id)
                for attempt in range(3):
                    items = wait_for(lambda: api.call(f"/api/v1/notebooks/{notebook}/sources")["sources"],
                        lambda values: all(item["state"] != "processing" for item in values), 1800)
                    current = next(item for item in items if item["id"] == source_id)
                    if current["state"] == "ready":
                        break
                    if current["state"] == "failed" and attempt < 2:
                        api.call(f"/api/v1/sources/{source_id}/retry", {})
                    else:
                        raise RuntimeError(key + ": source preparation failed; no Agent trial was admitted")
    chat = api.call(f"/api/v1/notebooks/{notebook}/chats", {})["chat"]["id"]
    if live_proof:
        verify_live_baseline(live_proof)
    instruction = suite["protocol"]["open_instruction" if case["category"] == "open" else "fixed_source_instruction"]
    admission = api.call(f"/api/v1/chats/{chat}/messages", {
        "id": str(uuid.uuid4()), "content": case["prompt"] + "\n\n" + instruction,
        "time_zone": "Asia/Shanghai", "mode": case["mode"], "source_ids": source_ids})
    admission = {"notebook_id": notebook, "chat_id": chat, "source_ids": source_ids, **admission}
    save_new(out / "admissions" / (key + ".json"), admission)
    bindings = [run_binding(admission["run_id"])]
    answer, plan, status = "", None, "failed"
    if case["mode"] == "research":
        session_id = admission["research_session_id"]
        get_session = lambda: api.call(f"/api/v1/research-sessions/{session_id}")
        planned = wait_for(get_session, lambda x: x["session"]["status"] != "planning", 600)
        plan = planned.get("plan")
        if planned["session"]["status"] == "awaiting_confirmation" and plan and not plan["content"].get("clarifying_questions"):
            api.call(f"/api/v1/research-sessions/{session_id}/start", {
                "plan_version": plan["version"], "time_zone": "Asia/Shanghai"})
            finished = wait_for(get_session, lambda x: x["session"]["status"] in TERMINAL, 3000)
            status = finished["session"]["status"]
            answer = finished.get("report", {}).get("content_markdown", "")
            bindings.append(run_binding(finished["session"]["execution_run_id"]))
        else:
            status = "clarification_required" if plan else planned["session"]["status"]
    observed = wait_for(lambda: product_result(admission, case), lambda x: x is not None, 900)
    bindings = observed["bindings"]
    if any(binding["release"] != suite["release"] for binding in bindings):
        raise ValueError("admitted release does not match frozen baseline")
    observation = {"case_id": case["id"], "trial": number, **observed}
    save_new(result_path, observation)
    return key + " " + observed["status"]


def collect(args):
    suite = json.loads((args.out / "suite.json").read_text())
    cases = {case["id"]: case for case in suite["cases"]}
    for path in sorted((args.out / "admissions").glob("*.json")):
        target = args.out / "observations" / path.name
        if target.exists():
            continue
        case_id, trial_no = path.stem.rsplit("-", 1)
        observed = product_result(json.loads(path.read_text()), cases[case_id])
        if observed is None:
            continue
        if any(binding["release"] != suite["release"] for binding in observed["bindings"]):
            raise ValueError("admitted release does not match baseline")
        save_new(target, {"case_id": case_id, "trial": int(trial_no), **observed})
        print(path.stem + " collected " + observed["status"], flush=True)


def failed_trials(out):
    rows = [json.loads(path.read_text()) for path in sorted((out / "observations").glob("*.json"))]
    return [{"case_id": row["case_id"], "trial": row["trial"]}
            for row in rows if row["status"] == "failed"]


def run(args):
    suite = load_suite(args.suite)
    args.out.mkdir(parents=True, exist_ok=True)
    current = baseline(suite, args.suite)
    live_proof = None
    if args.live_baseline_proof:
        live_proof = json.loads(args.live_baseline_proof.read_text())
        verify_live_baseline(live_proof)
        if current["suite_sha256"] != live_proof["baseline"]["suite_sha256"]:
            raise ValueError("suite differs from pinned live baseline")
        current = live_proof["baseline"]
        proof_target = args.out / "live-baseline-proof.json"
        if not proof_target.exists():
            save_new(proof_target, live_proof)
    baseline_path = args.out / "baseline.json"
    if baseline_path.exists():
        if json.loads(baseline_path.read_text()) != current:
            raise ValueError("baseline changed; use a new experiment directory")
    else:
        save_new(baseline_path, current)
        save_new(args.out / "suite.json", suite)
    manifest = prepare_sources(suite, args.cache)
    source_path = args.out / "sources.json"
    if source_path.exists():
        if json.loads(source_path.read_text()) != manifest:
            raise ValueError("fixed source bytes changed")
    else:
        save_new(source_path, manifest)
    cases = [case for case in suite["cases"] if not args.cases or case["id"] in args.cases.split(",")]
    if not cases:
        raise ValueError("no matching cases")
    selected = [{"case_id": case["id"], "trial": n}
                for n in range(1, suite["repeats"] + 1) for case in cases]
    if args.retry_failed_from:
        origin = args.retry_failed_from.resolve()
        if origin == args.out.resolve():
            raise ValueError("reruns require a separate output directory")
        if json.loads((origin / "baseline.json").read_text()) != current:
            raise ValueError("rerun baseline differs from original")
        selection_path = args.out / "retry-selection.json"
        if selection_path.exists():
            selection = json.loads(selection_path.read_text())
            if selection["original_directory"] != str(origin):
                raise ValueError("rerun origin changed")
        else:
            selection = {"original_directory": str(origin), "reason": "User requested rerun of execution failures after provider top-up",
                         "trials": failed_trials(origin)}
            save_new(selection_path, selection)
        selected = selection["trials"]
    indexed = {case["id"]: case for case in cases}
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.workers) as pool:
        futures = [pool.submit(trial, indexed[row["case_id"]], row["trial"], suite, args.cache,
                               args.out, args.api, args.retry_preparation, live_proof) for row in selected]
        errors = []
        for future in concurrent.futures.as_completed(futures):
            try:
                print(future.result(), flush=True)
            except Exception as error:
                errors.append(str(error))
                print("INCOMPLETE: " + str(error), flush=True)
    if errors:
        raise RuntimeError(f"{len(errors)} incomplete trials; aggregate scores withheld")


def report(args):
    suite = json.loads((args.out / "suite.json").read_text())
    grades = json.loads(args.grades.read_text())
    rows = []
    bindings = {}
    for case in suite["cases"]:
        for number in range(1, suite["repeats"] + 1):
            key = f'{case["id"]}-{number}'
            observation_bytes = (args.out / "observations" / (key + ".json")).read_bytes()
            verify_grade_observation(grades[key], observation_bytes)
            observation = json.loads(observation_bytes)
            for binding in observation["bindings"]:
                identity = binding["definition"]
                if identity in bindings and bindings[identity] != binding:
                    raise ValueError("mixed model/prompt baseline for " + identity)
                bindings[identity] = binding
            rows.append({"case_id": case["id"], "trial": number,
                         "pass": verdict(case, observation, grades[key])})
    result = {"suite": suite["id"], "k": suite["repeats"], "bindings": bindings,
              "baseline": json.loads((args.out / "baseline.json").read_text()),
              **summarize(suite["cases"], rows, suite["repeats"]), "results": rows}
    save_new(args.out / "report.json", result)
    k = suite["repeats"]
    lines = ["# Nano Research Agent 基线实验", "", f'基线：{suite["release"]}；Git {result["baseline"]["commit"]}。',
             "模型：" + "、".join(sorted({b["model"] for b in bindings.values()})) + "。保持产品采样配置。",
             "评分：固定模型评委结合原文和任务准则；并非人工金标。", "",
             f"| 类型 | 题数 | Pass@1 | Pass@{k} | Pass^{k} |", "| --- | ---: | ---: | ---: | ---: |"]
    for category, score in {"overall": result["overall"], **result["categories"]}.items():
        lines.append(f'| {category} | {score["tasks"]} | {score["pass_at_1"]:.1%} | {score["pass_at_k"]:.1%} | {score["pass_pow_k"]:.1%} |')
    lines += ["", "| 任务 | 五次结果 |", "| --- | --- |"]
    for case in suite["cases"]:
        marks = ["通过" if row["pass"] else "失败" for row in rows if row["case_id"] == case["id"]]
        lines.append(f'| {case["id"]} | {" / ".join(marks)} |')
    lines += ["", "Pass@1 为全部重复运行的平均单次通过率；Pass@5 为至少一次通过的任务比例；",
              "Pass^5 为五次全部通过的任务比例。固定温度0不保证独立随机采样；开放网页可能变化。",
              "20道人工编写研究题仅构成探索性实验，不能代表所有研究任务或推断小分差显著性。"]
    with (args.out / "report.md").open("x") as stream:
        stream.write("\n".join(lines) + "\n")
    print(json.dumps(result["overall"], ensure_ascii=False))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["run", "collect", "report"])
    parser.add_argument("--suite", type=Path, default=ROOT / "evals/agent/research-v1.json")
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--cache", type=Path, default=ROOT / ".codex-artifacts/agent-eval-papers")
    parser.add_argument("--api", default="http://127.0.0.1:8080")
    parser.add_argument("--workers", type=int, default=4)
    parser.add_argument("--cases", default="", help="pilot subset; final report still requires all tasks")
    parser.add_argument("--retry-preparation", action="store_true", help="retry only verified failed setup with no admitted Agent")
    parser.add_argument("--retry-failed-from", type=Path, help="explicit failed-only rerun into a separate directory; originals retained")
    parser.add_argument("--live-baseline-proof", type=Path, help="pin verified original service processes when the checkout has advanced")
    parser.add_argument("--grades", type=Path)
    args = parser.parse_args()
    if args.workers < 1 or args.workers > 8:
        parser.error("workers must be between 1 and 8")
    if args.api != "http://127.0.0.1:8080":
        parser.error("this experiment uses the local Compose product and database")
    if args.command == "report" and not args.grades:
        parser.error("report needs --grades")
    {"run": run, "collect": collect, "report": report}[args.command](args)


if __name__ == "__main__":
    main()
