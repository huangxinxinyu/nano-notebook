import { beforeEach, expect, test, vi } from "vitest";
import { uploadSourceFile } from "./source-upload";

beforeEach(() => {
  vi.restoreAllMocks();
  document.cookie = "nn_csrf=upload-token";
});

test("uses a content-bound direct upload intent and finalizes the immutable Source", async () => {
  const file = new File(["hello"], "paper.pdf", { type: "application/pdf" });
  if (!file.arrayBuffer) Object.defineProperty(file, "arrayBuffer", { value: async () => new TextEncoder().encode("hello").buffer });
  const requests: Array<{ url: string; init?: RequestInit }> = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    requests.push({ url, init });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources/upload-intents")) return response({
      upload_intent: { id: "upl_test" },
      upload: { method: "POST", url: "https://objects.example/upload", fields: { key: "staged/object", policy: "signed" } }
    }, 201);
    if (url === "https://objects.example/upload") return new Response(null, { status: 204 });
    if (url.endsWith("/api/v1/source-upload-intents/upl_test/finalize")) return response({ source: { id: "src_test" } }, 201);
    return response({ error: { code: "not_found" } }, 404);
  }));

  await uploadSourceFile("nb_test", file);

  const intent = requests[0];
  expect(intent?.init?.method).toBe("POST");
  expect(new Headers(intent?.init?.headers).get("Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  expect(new Headers(intent?.init?.headers).get("X-CSRF-Token")).toBe("upload-token");
  expect(JSON.parse(String(intent?.init?.body))).toEqual({
    title: "paper.pdf",
    format: "pdf",
    media_type: "application/pdf",
    byte_size: 5,
    content_sha256: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
  });
  const directUpload = requests[1]?.init?.body;
  expect(directUpload).toBeInstanceOf(FormData);
  expect((directUpload as FormData).get("key")).toBe("staged/object");
  expect((directUpload as FormData).get("policy")).toBe("signed");
  expect((directUpload as FormData).get("file")).toBe(file);
  expect(requests[2]?.url.endsWith("/api/v1/source-upload-intents/upl_test/finalize")).toBe(true);
});

test("rejects an unsupported file before creating an upload intent", async () => {
  const fetchSpy = vi.fn();
  vi.stubGlobal("fetch", fetchSpy);

  await expect(uploadSourceFile("nb_test", new File(["binary"], "payload.exe"))).rejects.toThrow("unsupported Source file");
  expect(fetchSpy).not.toHaveBeenCalled();
});

function response(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } });
}
