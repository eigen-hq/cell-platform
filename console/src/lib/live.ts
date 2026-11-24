// Live events of the cell (GET /v0/events, server-sent events). The stream
// needs the login's token, which EventSource cannot send, so it is read
// with fetch. It ends whenever a watch on the server ends: reconnect, with
// a growing pause while the API is away.
import type { components } from "./api/schema";

export type CellEvent = components["schemas"]["Event"];

/** Splits a stream buffer into the data of complete events and the rest. */
export function parseSSE(buffer: string): { data: string[]; rest: string } {
  const blocks = buffer.replace(/\r\n/g, "\n").split("\n\n");
  const rest = blocks.pop() ?? "";
  const data = blocks
    .map((b) =>
      b
        .split("\n")
        .filter((l) => l.startsWith("data:"))
        .map((l) => l.slice(5).replace(/^ /, ""))
        .join("\n"),
    )
    .filter((d) => d !== ""); // comments (": keepalive") carry no data
  return { data, rest };
}

/** The pause before the next try: doubles from one second up to thirty. */
export const backoff = (tries: number) => Math.min(1000 * 2 ** tries, 30_000);

/**
 * Follows the stream until stop() is called. open() answers the stream
 * (the API client's GET with parseAs "stream"); onState says whether the
 * stream is up, and why not.
 */
export function follow(
  open: (signal: AbortSignal) => Promise<{ data?: ReadableStream<Uint8Array> | null; response: Response }>,
  onEvent: (e: CellEvent) => void,
  onState: (up: boolean, why?: string) => void,
): () => void {
  const ctrl = new AbortController();
  (async () => {
    for (let tries = 0; !ctrl.signal.aborted; tries++) {
      try {
        const r = await open(ctrl.signal);
        if (!r.response.ok || !r.data) throw new Error(`The event stream answered HTTP ${r.response.status}.`);
        onState(true);
        tries = 0;
        const reader = r.data.getReader();
        const text = new TextDecoder();
        let buffer = "";
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          const p = parseSSE(buffer + text.decode(value, { stream: true }));
          buffer = p.rest;
          for (const d of p.data) onEvent(JSON.parse(d) as CellEvent);
        }
        onState(false, "The stream ended; reconnecting.");
      } catch (e) {
        if (ctrl.signal.aborted) return;
        onState(false, e instanceof Error ? e.message : String(e));
      }
      await new Promise((r) => setTimeout(r, backoff(tries)));
    }
  })();
  return () => ctrl.abort();
}
