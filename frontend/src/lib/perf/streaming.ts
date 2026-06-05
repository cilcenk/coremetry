// streaming.ts — progressive append for big result sets (v0.8.6 Phase 0).
//
// A 50k-span / 100k-log query shouldn't block on the whole JSON payload before
// the first row paints. streamNDJSON reads a newline-delimited-JSON response
// incrementally and hands each parsed object to `onItem` as it arrives, so the
// table fills top-down. AbortController-aware: pass the react-query queryFn's
// `signal` (or your own) and an unmount / key-change aborts the in-flight read.
//
// Backend contract: the endpoint streams one compact JSON object per line
// (Content-Type application/x-ndjson). Falls back gracefully — a non-streamed
// JSON array body still parses if the server didn't chunk.

export interface StreamOpts {
  signal?: AbortSignal;
  onProgress?: (count: number) => void;
  init?: RequestInit;
  // Flush cadence — call onProgress at most every N items to avoid a setState
  // storm on a fast stream. Default 200.
  progressEvery?: number;
}

export async function streamNDJSON<T>(
  url: string,
  onItem: (item: T) => void,
  opts: StreamOpts = {},
): Promise<{ count: number; aborted: boolean }> {
  const res = await fetch(url, { ...opts.init, signal: opts.signal });
  if (!res.ok) throw new Error(`stream ${url}: HTTP ${res.status}`);
  if (!res.body) {
    // No streaming body (test shim / proxy buffered it) — parse whole.
    const text = await res.text();
    const count = parseWhole<T>(text, onItem);
    opts.onProgress?.(count);
    return { count, aborted: false };
  }

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  const every = opts.progressEvery ?? 200;
  let buf = '';
  let count = 0;
  let sinceProgress = 0;

  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += decoder.decode(value, { stream: true });
      let nl: number;
      while ((nl = buf.indexOf('\n')) >= 0) {
        const line = buf.slice(0, nl).trim();
        buf = buf.slice(nl + 1);
        if (!line) continue;
        try { onItem(JSON.parse(line) as T); count++; sinceProgress++; } catch { /* skip malformed line */ }
        if (sinceProgress >= every) { opts.onProgress?.(count); sinceProgress = 0; }
      }
    }
  } catch (e) {
    if (opts.signal?.aborted) return { count, aborted: true };
    throw e;
  }

  const last = buf.trim();
  if (last) { try { onItem(JSON.parse(last) as T); count++; } catch { /* ignore trailing */ } }
  opts.onProgress?.(count);
  return { count, aborted: false };
}

// parseWhole handles a non-chunked body: either an NDJSON blob or a plain JSON
// array. Returns the item count.
function parseWhole<T>(text: string, onItem: (item: T) => void): number {
  const trimmed = text.trim();
  if (!trimmed) return 0;
  if (trimmed[0] === '[') {
    const arr = JSON.parse(trimmed) as T[];
    for (const it of arr) onItem(it);
    return arr.length;
  }
  let count = 0;
  for (const line of trimmed.split('\n')) {
    const l = line.trim();
    if (!l) continue;
    try { onItem(JSON.parse(l) as T); count++; } catch { /* skip */ }
  }
  return count;
}
