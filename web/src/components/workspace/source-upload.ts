const fileSpecs: Record<string, { format: string; mediaType: string }> = {
  txt: { format: "txt", mediaType: "text/plain" },
  md: { format: "markdown", mediaType: "text/markdown" },
  markdown: { format: "markdown", mediaType: "text/markdown" },
  pdf: { format: "pdf", mediaType: "application/pdf" },
  docx: { format: "docx", mediaType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document" },
  pptx: { format: "pptx", mediaType: "application/vnd.openxmlformats-officedocument.presentationml.presentation" },
  mp3: { format: "mp3", mediaType: "audio/mpeg" },
  wav: { format: "wav", mediaType: "audio/wav" },
  m4a: { format: "m4a", mediaType: "audio/mp4" },
  png: { format: "png", mediaType: "image/png" },
  jpg: { format: "jpeg", mediaType: "image/jpeg" },
  jpeg: { format: "jpeg", mediaType: "image/jpeg" },
  webp: { format: "webp", mediaType: "image/webp" }
};

export const acceptedSourceFormats = Object.keys(fileSpecs).map((extension) => `.${extension}`).join(",");

export async function uploadSourceFile(notebookID: string, file: File) {
  const extension = file.name.split(".").at(-1)?.toLowerCase() ?? "";
  const spec = fileSpecs[extension];
  if (!spec || file.size < 1 || file.size > 100 * 1024 * 1024) throw new Error("unsupported Source file");
  const contentSHA256 = await sha256(file);
  const intentResponse = await memberAPI(`/api/v1/notebooks/${notebookID}/sources/upload-intents`, {
    method: "POST",
    headers: { "Idempotency-Key": crypto.randomUUID(), "X-CSRF-Token": csrfToken() },
    body: JSON.stringify({ title: file.name, format: spec.format, media_type: spec.mediaType, byte_size: file.size, content_sha256: contentSHA256 })
  });
  if (!intentResponse.ok) throw new Error("Source upload intent failed");
  const payload = (await intentResponse.json()) as {
    upload_intent: { id: string };
    upload: { method: string; url: string; fields: Record<string, string> };
  };
  const form = new FormData();
  for (const [key, value] of Object.entries(payload.upload.fields)) form.append(key, value);
  form.append("file", file);
  const uploadResponse = await fetch(payload.upload.url, { method: payload.upload.method, body: form });
  if (!uploadResponse.ok) throw new Error("Source object upload failed");
  const finalizeResponse = await memberAPI(`/api/v1/source-upload-intents/${payload.upload_intent.id}/finalize`, {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken() }
  });
  if (!finalizeResponse.ok) throw new Error("Source upload finalization failed");
}

async function sha256(file: File) {
  const digest = await crypto.subtle.digest("SHA-256", await file.arrayBuffer());
  return [...new Uint8Array(digest)].map((value) => value.toString(16).padStart(2, "0")).join("");
}

export async function memberAPI(path: string, init: RequestInit = {}) {
  const headers = new Headers(init.headers);
  if (init.body && !(init.body instanceof FormData) && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  return fetch(path, { credentials: "include", ...init, headers });
}

export function csrfToken() {
  return document.cookie.split(";").map((part) => part.trim()).find((part) => part.startsWith("nn_csrf="))?.slice("nn_csrf=".length) ?? "";
}
