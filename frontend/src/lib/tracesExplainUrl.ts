// tracesExplainUrl.ts — v0.10.328: /traces boş sonuç ekranından tek tıkla
// teşhis. Listenin SON isteğinin parametreleriyle `/api/traces?…&explain=1`
// (v0.10.326, yalnız admin; sunucu 403 ile korur). Saf: undefined/boş
// değerler atlanır, diziler virgülle (api.ts qs sözleşmesi).
export function tracesExplainUrl(params: object | null | undefined): string | null {
  if (!params) return null;
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params as Record<string, unknown>)) {
    if (v === undefined || v === null || v === '' || v === false) continue;
    if (Array.isArray(v)) { if (v.length) q.set(k, v.join(',')); continue; }
    q.set(k, String(v));
  }
  q.set('explain', '1');
  return `/api/traces?${q.toString()}`;
}
