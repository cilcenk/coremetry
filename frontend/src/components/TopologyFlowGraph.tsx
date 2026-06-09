import { useEffect, useMemo, useRef, useState } from 'react';
import type { ServiceMap, ServiceMapNode, ServiceMapEdge } from '@/lib/types';
import { isMessagingDep } from '@/lib/utils';

// TopologyFlowGraph — prototipteki "pill düğüm + akış animasyonlu
// bezier kenar" topoloji görünümü. ServiceMapGraph ile AYNI props
// sözleşmesi, bu yüzden ServiceMap.tsx'te tek satırlık import
// değişikliğiyle yer değiştirir:
//
//   - import { ServiceMapGraph } from '@/components/ServiceMapGraph';
//   + import { TopologyFlowGraph } from '@/components/TopologyFlowGraph';
//   ...
//   - <ServiceMapGraph data={filtered} ... />
//   + <TopologyFlowGraph data={filtered} ... />
//
// Görsel dil — prototipten birebir:
//   • Düğümler HTML pill'leri (.topo-node): sağlık noktası + isim +
//     mono alt satır (req/s veya span sayısı). Dış bağımlılıklar
//     (db/queue/ext) kesikli çerçeve alır.
//   • Kenarlar SVG kübik bezier; kalınlık ∝ trafik, renk hata
//     durumunda --err. stroke-dasharray akış animasyonu
//     (globals.css'teki topo-edge-flow) canlı trafiği okutur;
//     yoğun kenar daha hızlı akar. prefers-reduced-motion global
//     kuralı animasyonu otomatik kapatır.
//   • Hover: 1-hop komşuluk dışındaki her şey .dim ile söner.
//   • Yerleşim: BFS katmanlı soldan-sağa (kapalı form, fizik yok) —
//     kök servisler solda, bağımlılıklar sağda. Focus modunda da
//     aynı yerleşim çalışır çünkü ServiceMap.tsx zaten 1-hop
//     komşuluğa filtreleyip veriyor.
//
// CSS bağımlılıkları globals.css'te zaten mevcut: .topo, .topo-edges,
// .topo-node (+ .focus/.dim), .topo-name, .topo-sub, .topo-dot,
// @keyframes topo-edge-flow. Tek ekleme (aşağıya bakın):
//   .topo-node.ext { border-style: dashed; background: var(--bg0); }

export function TopologyFlowGraph({
  data: rawData, focus, hoverNode, onHoverNode, onSelectNode, height = 560,
}: {
  data: ServiceMap;
  focus: string | null;
  hoverNode: string | null;
  onHoverNode: (s: string | null) => void;
  onSelectNode: (s: string) => void;
  height?: number;
}) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(900);

  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const ro = new ResizeObserver(entries => {
      for (const e of entries) setWidth(Math.max(400, e.contentRect.width));
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // Mesajlaşma broker'larını düşür — ServiceMapGraph'taki davranışın aynısı
  // (broadcast topic'ler topolojiyi hairball'a çevirir; messaging'in kendi
  // yüzeyi var).
  const data = useMemo<ServiceMap>(() => {
    if (!rawData.nodes.some(n => isMessagingDep(n.kind, n.subkind))) return rawData;
    const drop = new Set(
      rawData.nodes.filter(n => isMessagingDep(n.kind, n.subkind)).map(n => n.service),
    );
    return {
      ...rawData,
      nodes: rawData.nodes.filter(n => !drop.has(n.service)),
      edges: rawData.edges.filter(e => !drop.has(e.caller) && !drop.has(e.callee)),
    };
  }, [rawData]);

  // Çift yönlü kenarları tek çizgiye indir (A→B + B→A) — ServiceMapGraph deseni.
  type RenderedEdge = { forward: ServiceMapEdge; reverse?: ServiceMapEdge };
  const renderedEdges = useMemo<RenderedEdge[]>(() => {
    const byKey = new Map<string, RenderedEdge>();
    for (const e of data.edges) {
      const canon = e.caller < e.callee ? `${e.caller}|${e.callee}` : `${e.callee}|${e.caller}`;
      const ex = byKey.get(canon);
      if (!ex) byKey.set(canon, { forward: e });
      else ex.reverse = e;
    }
    return Array.from(byKey.values());
  }, [data.edges]);

  // Hover sönümü için 1-hop komşuluk.
  const neighbours = useMemo(() => {
    const m = new Map<string, Set<string>>();
    for (const n of data.nodes) m.set(n.service, new Set([n.service]));
    for (const e of data.edges) {
      m.get(e.caller)?.add(e.callee);
      m.get(e.callee)?.add(e.caller);
    }
    return m;
  }, [data]);
  const active = hoverNode ? (neighbours.get(hoverNode) ?? new Set([hoverNode])) : null;

  // ── BFS katmanlı yerleşim ───────────────────────────────────────────
  // Kök = gelen kenarı olmayan düğümler; erişilemeyenler en sağ sütuna.
  // Sütun içi sıralama: ebeveynlerin ortalama y'sine göre (barycenter),
  // tek geçişte kenar kesişimlerini ciddi azaltır.
  const positioned = useMemo(() => {
    const incoming = new Map<string, number>();
    for (const e of data.edges) incoming.set(e.callee, (incoming.get(e.callee) ?? 0) + 1);
    let roots = data.nodes.filter(n => !incoming.has(n.service)).map(n => n.service);
    if (roots.length === 0 && data.nodes.length > 0) {
      // Döngü: en yüksek hacimli servisi kök say.
      roots = [data.nodes.slice().sort((a, b) => b.spanCount - a.spanCount)[0].service];
    }
    const depth = new Map<string, number>();
    roots.forEach(r => depth.set(r, 0));
    let frontier = [...roots];
    let d = 0;
    while (frontier.length && d < 12) {
      d++;
      const next = new Set<string>();
      for (const f of frontier) {
        for (const e of data.edges) {
          if (e.caller === f && !depth.has(e.callee)) { depth.set(e.callee, d); next.add(e.callee); }
        }
      }
      frontier = Array.from(next);
    }
    let maxDepth = 0;
    for (const v of depth.values()) maxDepth = Math.max(maxDepth, v);
    for (const n of data.nodes) if (!depth.has(n.service)) depth.set(n.service, maxDepth + 1);
    maxDepth = Math.max(...Array.from(depth.values()), 0);

    const columns: string[][] = Array.from({ length: maxDepth + 1 }, () => []);
    for (const n of data.nodes) columns[depth.get(n.service)!].push(n.service);

    const pos = new Map<string, { x: number; y: number }>();
    const padX = 110;
    const colW = columns.length > 1 ? (width - padX * 2) / (columns.length - 1) : 0;
    columns.forEach((col, ci) => {
      // Barycenter: ebeveyn y ortalamasına göre sırala (0. sütun ada göre).
      const sorted = ci === 0
        ? col.slice().sort()
        : col.slice().sort((a, b) => barycenter(a) - barycenter(b));
      function barycenter(svc: string): number {
        const ys = data.edges.filter(e => e.callee === svc).map(e => pos.get(e.caller)?.y ?? height / 2);
        return ys.length ? ys.reduce((s, y) => s + y, 0) / ys.length : height / 2;
      }
      sorted.forEach((svc, i) => {
        pos.set(svc, {
          x: columns.length === 1 ? width / 2 : padX + ci * colW,
          y: ((i + 1) / (sorted.length + 1)) * height,
        });
      });
    });
    return pos;
  }, [data, width, height]);

  const edgeMaxTraces = useMemo(
    () => data.edges.reduce((m, e) => Math.max(m, e.traceCount), 0),
    [data.edges],
  );

  return (
    <div ref={wrapRef} className="topo" style={{ height }}>
      <svg className="topo-edges" width={width} height={height}>
        {renderedEdges.map((re, i) => {
          const e = re.forward;
          const a = positioned.get(e.caller), b = positioned.get(e.callee);
          if (!a || !b) return null;
          const totalTraces = e.traceCount + (re.reverse?.traceCount ?? 0);
          const t = totalTraces / Math.max(1, edgeMaxTraces);
          const w = 1 + 2.2 * t;
          const errorish = e.errorCount > 0 || (re.reverse?.errorCount ?? 0) > 0;
          const dimmed = active && (!active.has(e.caller) || !active.has(e.callee));
          const mx = (a.x + b.x) / 2;
          return (
            <path key={i}
              d={`M ${a.x} ${a.y} C ${mx} ${a.y}, ${mx} ${b.y}, ${b.x} ${b.y}`}
              fill="none"
              stroke={e.isNew ? 'var(--ok)' : errorish ? 'var(--err)' : 'var(--border-strong)'}
              strokeWidth={w}
              opacity={dimmed ? 0.12 : errorish ? 0.85 : 0.55}
              className="topo-edge-flow"
              style={{ animationDuration: `${(2.8 - 1.8 * t).toFixed(2)}s`, transition: 'opacity 120ms' }}>
              <title>
                {`${e.caller} → ${e.callee}\n${e.traceCount} traces · ${e.spanCount} spans` +
                  (e.errorCount > 0 ? ` · ${e.errorCount} errors` : '') +
                  (re.reverse ? `\n${re.reverse.caller} → ${re.reverse.callee} (çift yönlü)` : '') +
                  (e.isNew ? '\n[NEW since baseline]' : '')}
              </title>
            </path>
          );
        })}
      </svg>

      {data.nodes.map(n => {
        const p = positioned.get(n.service);
        if (!p) return null;
        const dim = active && !active.has(n.service);
        const level = n.errorRate > 0.05 ? 'red' : n.errorRate > 0.01 ? 'amber' : 'green';
        const isDep = !!n.kind;
        return (
          <div key={n.service}
            className={
              'topo-node' +
              (focus === n.service ? ' focus' : '') +
              (dim ? ' dim' : '') +
              (isDep ? ' ext' : '')
            }
            style={{ left: p.x, top: p.y, cursor: 'pointer' }}
            role="button" tabIndex={0}
            aria-label={n.service}
            onMouseEnter={() => onHoverNode(n.service)}
            onMouseLeave={() => onHoverNode(null)}
            onClick={() => onSelectNode(n.service)}
            onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') onSelectNode(n.service); }}
            title={
              `${n.service}${n.kind ? ` · ${n.kind}` : ''}\n` +
              `${n.spanCount.toLocaleString()} spans · ${(n.errorRate * 100).toFixed(2)}% error` +
              (n.cluster ? `\ncluster: ${n.cluster}` : '')
            }>
            <span className={`topo-dot ${level}`} />
            <div style={{ minWidth: 0 }}>
              <div className="topo-name">{displayLabel(n)}</div>
              <div className="topo-sub">
                {isDep ? depLabel(n.kind!) : `${n.spanCount.toLocaleString()} span`}
              </div>
            </div>
          </div>
        );
      })}
    </div>
  );
}

function displayLabel(n: ServiceMapNode): string {
  return n.subkind || n.service.replace(/^(db|queue|ext):/, '');
}
function depLabel(kind: string): string {
  switch (kind) {
    case 'db': return 'database';
    case 'queue': return 'queue';
    case 'external': return 'dış bağımlılık';
    default: return kind;
  }
}
