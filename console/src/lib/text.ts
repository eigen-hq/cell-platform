// Answers of text endpoints (logs): openapi-fetch leaves out an empty body
// and parses a JSON error into an object, so both need a look.

/** The text of a successful answer, or an error with the API's own message. */
export function textOf(r: { data?: string; error?: unknown; response: { ok: boolean; status: number } }): string {
  if (r.response.ok) return r.data ?? "";
  let e = r.error;
  if (typeof e === "string") {
    try {
      e = JSON.parse(e);
    } catch {
      if (e) throw new Error(e as string);
    }
  }
  const msg = (e as { error?: unknown } | undefined)?.error;
  throw new Error(typeof msg === "string" && msg ? msg : `The API answered HTTP ${r.response.status}.`);
}
