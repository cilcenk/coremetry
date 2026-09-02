// spanAttrGroups.ts — v0.10.277 (trace view Dilim 1e, docs/audit/trace-view.md
// §3.7): SpanDetail attribute'ları düz listeliyordu (Object.entries sırası).
// Semconv ön eklerine göre gruplar: HTTP (http.* url.* server.* client.*
// user_agent.*), DB (db.*), Messaging (messaging.*), RPC (rpc.*), Network
// (net.* network.* peer.*), Infra (k8s.* container.* host.* cloud.* process.*),
// Exception (exception.*), Custom (kalan). Grup içi sıra: girişteki sıra
// (anahtar alfabetik değil — operatörün alışık olduğu düzen). Boş grup çıkmaz.
// Saf; React yok.

export type SpanAttrGroupKey = 'http' | 'db' | 'messaging' | 'rpc' | 'network' | 'infra' | 'exception' | 'custom';

export interface SpanAttrGroup {
  key: SpanAttrGroupKey;
  label: string;
  entries: [string, string][];
}

const RULES: { key: SpanAttrGroupKey; label: string; prefixes: string[] }[] = [
  { key: 'http', label: 'HTTP', prefixes: ['http.', 'url.', 'server.', 'client.', 'user_agent.'] },
  { key: 'db', label: 'Database', prefixes: ['db.'] },
  { key: 'messaging', label: 'Messaging', prefixes: ['messaging.'] },
  { key: 'rpc', label: 'RPC', prefixes: ['rpc.', 'grpc.'] },
  { key: 'network', label: 'Network', prefixes: ['net.', 'network.', 'peer.'] },
  { key: 'infra', label: 'Infra', prefixes: ['k8s.', 'container.', 'host.', 'cloud.', 'process.', 'os.'] },
  { key: 'exception', label: 'Exception', prefixes: ['exception.'] },
];

export function spanAttrGroupOf(key: string): SpanAttrGroupKey {
  for (const r of RULES) for (const p of r.prefixes) if (key.startsWith(p)) return r.key;
  return 'custom';
}

/** groupSpanAttrs — sabit grup sırası, yalnız dolu gruplar. */
export function groupSpanAttrs(attrs: Record<string, unknown> | null | undefined): SpanAttrGroup[] {
  const buckets = new Map<SpanAttrGroupKey, [string, string][]>();
  for (const [k, v] of Object.entries(attrs ?? {})) {
    const g = spanAttrGroupOf(k);
    const list = buckets.get(g) ?? [];
    list.push([k, v == null ? '' : String(v)]);
    buckets.set(g, list);
  }
  const out: SpanAttrGroup[] = [];
  for (const r of RULES) {
    const e = buckets.get(r.key);
    if (e && e.length) out.push({ key: r.key, label: r.label, entries: e });
  }
  const custom = buckets.get('custom');
  if (custom && custom.length) out.push({ key: 'custom', label: 'Custom', entries: custom });
  return out;
}
