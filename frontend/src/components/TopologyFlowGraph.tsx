import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import type { ServiceMap, ServiceMapNode, ServiceMapEdge } from '@/lib/types';
import { isMessagingDep, fmtNum } from '@/lib/utils';
import { edgeWeights } from '@/lib/edgeWeight';
import { fitViewport, readableFit, zoomAt, zoomRange, type Viewport } from '@/lib/topoViewport';
import { depInstanceLabel } from '@/lib/topoLabels';
import { Button } from '@/components/ui/Button';
import { useServicesMetadata } from '@/lib/queries';
import {
  foldTopology, defaultCollapsed, parseNsFold, encodeNsFold,
  isNsNode, nsOfNodeId,
} from '@/lib/topoFold';

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
  dropMessaging = true,
}: {
  data: ServiceMap;
  focus: string | null;
  hoverNode: string | null;
  onHoverNode: (s: string | null) => void;
  onSelectNode: (s: string) => void;
  height?: number;
  // /topology'nin odaklı 1-hop görünümü kuyrukları GÖSTERİR (operatör
  // servisi zaten seçti, hairball riski yok) — false geçer. Global
  // /service-map varsayılanı korur: broker düğümleri düşer.
  dropMessaging?: boolean;
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
  const preFold = useMemo<ServiceMap>(() => {
    if (!dropMessaging) return rawData;
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

  // ── v0.8.447 (operatör-seçimi Varyant C) — katlanabilir namespace
  // grup kutuları. Katlanmış namespace'in üyeleri layout'tan ÖNCE tek
  // süper-düğüme iner (pure çekirdek lib/topoFold.ts, vitest-pinli) —
  // BFS/barycenter yerleşimi, zoom/pan ve hover kodu küçülmüş düz bir
  // graf görür. Katla/aç state'i URL'de (?nsfold=, house rule §4):
  // param yokken kalabalık haritada büyük namespace'ler otomatik katlı
  // başlar (operatör şikâyeti "çok deployment'lı topolojide hull'lar
  // küçük kalıyor"); herhangi bir toggle açık listeyi yazar, kopyalanan
  // link aynı görünümü açar. Focus'lu servisin namespace'i asla
  // katlanmaz (1-hop görünümde seçili düğüm kaybolmasın).
  const metaQ = useServicesMetadata();
  const nsOf = useCallback(
    (svc: string) => (metaQ.data ?? {})[svc]?.namespace || undefined,
    [metaQ.data],
  );
  const [params, setParams] = useSearchParams();
  const explicitFold = useMemo(() => parseNsFold(params.get('nsfold')), [params]);
  const collapsed = useMemo(() => {
    const base = explicitFold ?? defaultCollapsed(preFold, nsOf);
    const fns = focus ? nsOf(focus) : undefined;
    if (fns && base.has(fns)) {
      const c = new Set(base);
      c.delete(fns);
      return c;
    }
    return base;
  }, [explicitFold, preFold, nsOf, focus]);
  const { data, groups, bundled } = useMemo(
    () => foldTopology(preFold, nsOf, collapsed),
    [preFold, nsOf, collapsed],
  );
  const writeFold = (next: ReadonlySet<string>) => setParams(prev => {
    const p = new URLSearchParams(prev);
    p.set('nsfold', encodeNsFold(next));
    return p;
  }, { replace: true });
  const focusNs = focus ? nsOf(focus) : undefined;
  const collapseNs = (ns: string) => {
    // Focus'un namespace'i katlanamaz — derivasyon zaten geri açardı;
    // URL'e ölü bir kayıt yazıp auto-fold posture'ını dondurmayalım.
    if (ns === focusNs) return;
    writeFold(new Set([...collapsed, ns]));
  };
  const expandNs = (ns: string) => {
    const c = new Set(collapsed);
    c.delete(ns);
    writeFold(c);
  };

  // Deterministik namespace rengi — TÜM (katlı + açık) ≥2 üyeli
  // namespace'lerin sıralı listesi üzerinden indekslenir ki bir grubun
  // katlanması diğerlerinin rengini kaydırmasın.
  const nsColor = useMemo(() => {
    const meta = metaQ.data ?? {};
    const counts = new Map<string, number>();
    for (const n of preFold.nodes) {
      if (n.kind) continue;
      const ns = meta[n.service]?.namespace;
      if (ns) counts.set(ns, (counts.get(ns) ?? 0) + 1);
    }
    const palette = ['--accent', '--warn', '--ok', '--purple', '--teal', '--orange'];
    const m = new Map<string, string>();
    [...counts.entries()].filter(([, c]) => c >= 2).map(([ns]) => ns).sort()
      .forEach((ns, i) => m.set(ns, palette[i % palette.length]));
    return m;
  }, [preFold.nodes, metaQ.data]);

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
  // v0.8.447 review-fix: hover id'si mevcut grafta yoksa hover YOK say.
  // Eski fallback (tek elemanlı set) katlı süper-düğüm genişletilince
  // unmount olur, mouseleave hiç ateşlenmez ve bayat sentetik id her
  // düğümü .dim'e, her kenarı 0.12 opaklığa kilitliyordu. neighbours
  // render edilen HER düğüm için kayıt taşır — fallback yalnız bayat
  // id durumunda tetiklenirdi, güvenle null'a iner.
  const active = hoverNode ? (neighbours.get(hoverNode) ?? null) : null;

  // ── BFS katmanlı yerleşim ───────────────────────────────────────────
  // Kök = gelen kenarı olmayan düğümler; erişilemeyenler en sağ sütuna.
  // Sütun içi sıralama: ebeveynlerin ortalama y'sine göre (barycenter),
  // tek geçişte kenar kesişimlerini ciddi azaltır.
  //
  // v0.8.296 — yerleşim artık VIEW alanına değil LAYOUT alanına açılır:
  // kalabalık bir sütun (ör. 30 servis) 560px'e sıkışıp pill'leri üst üste
  // bindirmek yerine sütun başına ~46px satır yüksekliğiyle büyür; viewport
  // transform'u (fit/zoom/pan) bu alanı ekrana sığdırır. Operatör-raporlu
  // "çok fazla servis ekrana sığmıyor" düzeltmesinin yarısı budur (diğer
  // yarısı zoom/pan).
  const { positioned, layoutW, layoutH } = useMemo(() => {
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

    // Layout alanı: en kalabalık sütun satır başına ~46px alır (pill ~34px +
    // nefes), sütunlar en az 180px açılır — view'dan KÜÇÜLMEZ (az düğümde
    // eski birebir yerleşim).
    const maxCol = Math.max(1, ...columns.map(c => c.length));
    const lh = Math.max(height, maxCol * 46);
    const padX = 110;
    const lw = Math.max(width, padX * 2 + Math.max(0, columns.length - 1) * 180);

    const pos = new Map<string, { x: number; y: number }>();
    const colW = columns.length > 1 ? (lw - padX * 2) / (columns.length - 1) : 0;
    columns.forEach((col, ci) => {
      // Barycenter: ebeveyn y ortalamasına göre sırala (0. sütun ada göre).
      const sorted = ci === 0
        ? col.slice().sort()
        : col.slice().sort((a, b) => barycenter(a) - barycenter(b));
      function barycenter(svc: string): number {
        const ys = data.edges.filter(e => e.callee === svc).map(e => pos.get(e.caller)?.y ?? lh / 2);
        return ys.length ? ys.reduce((s, y) => s + y, 0) / ys.length : lh / 2;
      }
      sorted.forEach((svc, i) => {
        pos.set(svc, {
          x: columns.length === 1 ? lw / 2 : padX + ci * colW,
          y: ((i + 1) / (sorted.length + 1)) * lh,
        });
      });
    });
    return { positioned: pos, layoutW: lw, layoutH: lh };
  }, [data, width, height]);

  // ── v0.8.296 — zoom/pan viewport ─────────────────────────────────────
  // Transform matematiği pure seam'de (lib/topoViewport.ts, vitest-pinli).
  // Fit-guard: yalnız düğüm-kümesi imzası (veya boyutlar) değişince auto-Fit —
  // 30sn'lik refetch operatörün zoom/pan'ını asla sıfırlamaz.
  // v0.8.544 — OPEN at a readable scale, not a fitted one. fitViewport has
  // no floor, so a busy neighbourhood used to open at k≈0.4 and read as one
  // clump. readableFit lets the graph overflow instead; kMin below still
  // rides the true fit, so zooming out to "everything" stays available, and
  // ⛶ jumps straight there.
  const [vp, setVp] = useState<Viewport>(() => readableFit(layoutW, layoutH, width, height));
  const fitK = useMemo(() => fitViewport(layoutW, layoutH, width, height).k, [layoutW, layoutH, width, height]);
  const { kMin, kMax } = zoomRange(fitK);
  const sig = useMemo(
    () => data.nodes.map(n => n.service).sort().join('|') + `@${layoutW}x${layoutH}:${width}x${height}`,
    [data.nodes, layoutW, layoutH, width, height],
  );
  useEffect(() => {
    setVp(readableFit(layoutW, layoutH, width, height));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sig]);

  // Wheel zoom — native listener: React'in onWheel'i passive olduğundan
  // preventDefault sayfa scroll'unu ancak böyle keser.
  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const r = el.getBoundingClientRect();
      const factor = Math.exp(-e.deltaY * 0.002);
      const { kMin: lo, kMax: hi } = zoomRange(fitViewport(layoutW, layoutH, r.width, height).k);
      setVp(v => zoomAt(v, e.clientX - r.left, e.clientY - r.top, factor, lo, hi));
    };
    el.addEventListener('wheel', onWheel, { passive: false });
    return () => el.removeEventListener('wheel', onWheel);
  }, [layoutW, layoutH, height]);

  // Drag pan — pill üstünde başlamaz (tıklama/focus davranışı bozulmaz).
  const [panning, setPanning] = useState(false);
  const panRef = useRef<{ px: number; py: number } | null>(null);
  const onPointerDown = (e: React.PointerEvent) => {
    if ((e.target as HTMLElement).closest('.topo-node, .topo-zoomctl, .topo-nshdr')) return;
    panRef.current = { px: e.clientX, py: e.clientY };
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    setPanning(true);
  };
  const onPointerMove = (e: React.PointerEvent) => {
    const p = panRef.current;
    if (!p) return;
    const dx = e.clientX - p.px, dy = e.clientY - p.py;
    panRef.current = { px: e.clientX, py: e.clientY };
    setVp(v => ({ ...v, x: v.x + dx, y: v.y + dy }));
  };
  const endPan = () => { panRef.current = null; setPanning(false); };
  const zoomStep = (factor: number) => {
    const el = wrapRef.current;
    const w = el?.clientWidth ?? width;
    setVp(v => zoomAt(v, w / 2, height / 2, factor, kMin, kMax));
  };

  // v0.8.281 — trafik ağırlığı: örneklenmiş global görünümde traceCount
  // (eski davranış birebir), MV-backed focus görünümünde spanCount (orada
  // traceCount hep 0 → tüm kenarlar minimum kalınlıkta çiziliyordu).
  const { weightOf, max: edgeMax } = useMemo(() => edgeWeights(data.edges), [data.edges]);

  // v0.8.437 (Varyant A) → v0.8.447 (Varyant C): AÇIK namespace'ler
  // hâlâ yarı saydam kutuyla sarılır ama etiket artık TIKLANABİLİR
  // başlık — katlar. Katlı olanlar burada hiç görünmez (üyeleri
  // fold'da süper-düğüme indi). Kurallar aynı: ≥2 üye, kind'lı
  // bağımlılıklar ve namespace'siz servisler SERBEST (operatör
  // kararı: "diğer" kovası yok). Renk nsColor'dan — katla/aç
  // toggle'ı diğer grupların rengini kaydırmaz.
  const hulls = useMemo(() => {
    const meta = metaQ.data ?? {};
    const boxes = new Map<string, { x0: number; y0: number; x1: number; y1: number; n: number }>();
    for (const node of data.nodes) {
      if (node.kind || isNsNode(node.service)) continue; // dep / süper-düğüm — free
      const ns = meta[node.service]?.namespace;
      if (!ns) continue;
      const p = positioned.get(node.service);
      if (!p) continue;
      const g = boxes.get(ns);
      if (!g) {
        boxes.set(ns, { x0: p.x, y0: p.y, x1: p.x, y1: p.y, n: 1 });
      } else {
        g.x0 = Math.min(g.x0, p.x); g.y0 = Math.min(g.y0, p.y);
        g.x1 = Math.max(g.x1, p.x); g.y1 = Math.max(g.y1, p.y);
        g.n++;
      }
    }
    const NODE_W = 70, NODE_H = 34, PAD = 26; // kart yarı-boyutu + nefes payı
    return [...boxes.entries()]
      .filter(([, g]) => g.n >= 2)
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([ns, g]) => ({
        ns,
        n: g.n,
        x: g.x0 - NODE_W - PAD,
        y: g.y0 - NODE_H - PAD,
        w: (g.x1 - g.x0) + 2 * (NODE_W + PAD),
        h: (g.y1 - g.y0) + 2 * (NODE_H + PAD),
        colorVar: nsColor.get(ns) ?? '--accent',
      }));
  }, [data.nodes, positioned, metaQ.data, nsColor]);

  // Yoğunluk kapısı (ServiceGraph slice-4 kuralının aynısı): az kenarlı
  // grafikte chip'ler hep açık, kalabalıkta yalnız hover'da.
  const labelAlways = renderedEdges.length < 8;

  return (
    <div
      ref={wrapRef}
      className={'topo ' + (panning ? 'panning' : 'pannable')}
      style={{ height }}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={endPan}
      onPointerCancel={endPan}
      onDoubleClick={e => {
        if ((e.target as HTMLElement).closest('.topo-node, .topo-zoomctl, .topo-nshdr')) return;
        const r = e.currentTarget.getBoundingClientRect();
        const cx = e.clientX - r.left, cy = e.clientY - r.top;
        setVp(v => zoomAt(v, cx, cy, 1.5, kMin, kMax));
      }}>
      <div
        className="topo-viewport"
        style={{ width: layoutW, height: layoutH, transform: `translate(${vp.x}px, ${vp.y}px) scale(${vp.k})` }}>
      <svg className="topo-edges" width={layoutW} height={layoutH}>
        {hulls.map(h => (
          <rect key={`hull-${h.ns}`} x={h.x} y={h.y} width={h.w} height={h.h} rx={24}
            fill={`color-mix(in srgb, var(${h.colorVar}) 7%, transparent)`}
            stroke={`color-mix(in srgb, var(${h.colorVar}) 30%, transparent)`}
            strokeWidth={1} />
        ))}
        {renderedEdges.map((re, i) => {
          const e = re.forward;
          const a = positioned.get(e.caller), b = positioned.get(e.callee);
          if (!a || !b) return null;
          const t = (weightOf(e) + weightOf(re.reverse)) / edgeMax;
          const w = 1 + 2.2 * t;
          const errorish = e.errorCount > 0 || (re.reverse?.errorCount ?? 0) > 0;
          // Hover vurgusu: hover edilen düğüme DOĞRUDAN bağlı kenarlar maviye
          // döner ve kalınlaşır. Semantik renk hiyerarşisi: hata kırmızısı >
          // hover mavisi > nötr — hatalı kenar hover'da kırmızı KALIR.
          const hot = hoverNode && (e.caller === hoverNode || e.callee === hoverNode);
          const dimmed = active && (!active.has(e.caller) || !active.has(e.callee));
          const mx = (a.x + b.x) / 2;
          // Demet sayıları yön başına tutulur (fold sonrası key'ler yönlü);
          // çift yönlü çizgide ters yönün demeti de raporlanır.
          const fwdBundle = bundled.get(`${e.caller}|${e.callee}`) ?? 0;
          const revBundle = re.reverse ? (bundled.get(`${re.reverse.caller}|${re.reverse.callee}`) ?? 0) : 0;
          return (
            <path key={i}
              d={`M ${a.x} ${a.y} C ${mx} ${a.y}, ${mx} ${b.y}, ${b.x} ${b.y}`}
              fill="none"
              stroke={e.isNew ? 'var(--ok)' : errorish ? 'var(--err)' : hot ? 'var(--accent)' : 'var(--border-strong)'}
              strokeWidth={hot ? w + 0.8 : w}
              opacity={dimmed ? 0.12 : hot ? 0.95 : errorish ? 0.85 : 0.55}
              className="topo-edge-flow"
              style={{ animationDuration: `${(2.8 - 1.8 * t).toFixed(2)}s`, transition: 'opacity 120ms, stroke 120ms, stroke-width 120ms' }}>
              <title>
                {`${e.caller} → ${e.callee}\n${e.traceCount} traces · ${e.spanCount} spans` +
                  (e.errorCount > 0 ? ` · ${e.errorCount} errors` : '') +
                  (re.reverse ? `\n${re.reverse.caller} → ${re.reverse.callee} (çift yönlü)` : '') +
                  (fwdBundle > 1 ? `\n${fwdBundle} kenar demetlendi (katlı grup)` : '') +
                  (revBundle > 1 ? `\n${revBundle} kenar demetlendi (ters yön, katlı grup)` : '') +
                  (e.isNew ? '\n[NEW since baseline]' : '')}
              </title>
            </path>
          );
        })}
      </svg>

      {/* Namespace kutu başlıkları — tıkla: grubu tek karta katla
          (v0.8.447). Pan bu elemanda başlamaz (onPointerDown istisnası).
          Focus'lu servisin namespace'i katlanamaz (derivasyon geri
          açardı) — o başlık buton semantiği olmadan salt etiket kalır. */}
      {hulls.map(h => h.ns === focusNs ? (
        <div key={`nshdr-${h.ns}`} className="topo-nshdr"
          style={{ left: h.x + 10, top: h.y + 6, color: `var(${h.colorVar})`, cursor: 'default' }}
          title={`ns: ${h.ns} — odaklı servisin namespace'i katlanamaz`}>
          ns: {h.ns} · {h.n} svc
        </div>
      ) : (
        <div key={`nshdr-${h.ns}`} className="topo-nshdr"
          style={{ left: h.x + 10, top: h.y + 6, color: `var(${h.colorVar})` }}
          role="button" tabIndex={0}
          title={`ns: ${h.ns} — tıkla: ${h.n} servisi tek karta katla`}
          onClick={() => collapseNs(h.ns)}
          onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') collapseNs(h.ns); }}>
          ns: {h.ns} · {h.n} svc <span className="caret">▾</span>
        </div>
      ))}

      {/* v0.8.281 — kenar RED chip'i: hacim/dk · p99 (· err%). Veri yalnız
          MV yolunda var (adapter enrichment) — örneklenmiş global görünümde
          p99Ms undefined kalır ve chip hiç çizilmez. Yoğunluk kapısı:
          labelAlways (<8 kenar) veya hover. pointer-events yok — dekor. */}
      {renderedEdges.map((re, i) => {
        const e = re.forward;
        if (e.p99Ms == null) return null;
        const a = positioned.get(e.caller), b = positioned.get(e.callee);
        if (!a || !b) return null;
        const hot = hoverNode && (e.caller === hoverNode || e.callee === hoverNode);
        const dimmed = active && (!active.has(e.caller) || !active.has(e.callee));
        if (dimmed || (!labelAlways && !hot)) return null;
        const errPct = (e.errorRate ?? 0) * 100;
        return (
          <div key={`chip-${i}`} className={'topo-chip' + (hot ? ' hot' : '')}
            style={{ left: (a.x + b.x) / 2, top: (a.y + b.y) / 2 }}>
            {fmtNum(Math.round(e.rate ?? 0))}/dk · p99 {fmtMs(e.p99Ms)}
            {errPct >= 1 && <span className="chip-err"> · {errPct.toFixed(1)}% err</span>}
          </div>
        );
      })}

      {data.nodes.map(n => {
        const p = positioned.get(n.service);
        if (!p) return null;
        const dim = active && !active.has(n.service);
        const level = n.errorRate > 0.05 ? 'red' : n.errorRate > 0.01 ? 'amber' : 'green';
        // v0.8.447 — katlı namespace süper-düğümü: tıkla → genişlet.
        // onSelectNode ÇAĞRILMAZ (drawer servis bekler); hover sönümü
        // ve sağlık noktası normal düğümle aynı dilde çalışır.
        if (isNsNode(n.service)) {
          const ns = nsOfNodeId(n.service);
          const g = groups.find(x => x.ns === ns);
          const colorVar = nsColor.get(ns) ?? '--accent';
          return (
            <div key={n.service}
              className={'topo-node topo-nsnode' + (dim ? ' dim' : '')}
              style={{
                left: p.x, top: p.y, cursor: 'pointer',
                borderColor: `color-mix(in srgb, var(${colorVar}) 55%, var(--border))`,
              }}
              role="button" tabIndex={0}
              aria-label={`namespace ${ns} — genişlet`}
              onMouseEnter={() => onHoverNode(n.service)}
              onMouseLeave={() => onHoverNode(null)}
              // Hover'ı genişletmeden ÖNCE temizle — pill unmount olunca
              // mouseleave hiç gelmez, bayat sentetik id kalırdı (dim-kilit).
              onClick={() => { onHoverNode(null); expandNs(ns); }}
              onKeyDown={e => {
                if (e.key === 'Enter' || e.key === ' ') { onHoverNode(null); expandNs(ns); }
              }}
              title={
                `ns: ${ns} — ${g?.members.length ?? 0} servis (tıkla: genişlet)\n` +
                (g?.members.join('\n') ?? '')
              }>
              <span className={`topo-dot ${level}`} />
              <div style={{ minWidth: 0 }}>
                <div className="topo-name" style={{ color: `var(${colorVar})` }}>{ns} ▸</div>
                <div className="topo-sub">
                  {g?.members.length ?? 0} svc · {n.spanCount.toLocaleString()} span
                  {n.errorRate > 0.01 ? ` · ${(n.errorRate * 100).toFixed(1)}% err` : ''}
                </div>
              </div>
            </div>
          );
        }
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
              (n.dbName ? `\ndb.name: ${n.dbName}` : '') +
              (n.cluster ? `\ncluster: ${n.cluster}` : '') +
              (n.env ? `\nenv: ${n.env}` : '')
            }>
            <span className={`topo-dot ${level}`} />
            <div style={{ minWidth: 0 }}>
              <div className="topo-name">
                {displayLabel(n)}
                {/* v0.8.383 — env chip: lights up on the MV path where the
                    adapter carries GraphNode.env (deploy_env-led derive,
                    v0.8.380). The sampled global map has no env → no chip. */}
                {n.env && <span className="topo-envchip">{n.env}</span>}
              </div>
              <div className="topo-sub">
                {isDep ? (depInstanceLabel(n) ?? depLabel(n.kind!)) : `${n.spanCount.toLocaleString()} span`}
              </div>
            </div>
          </div>
        );
      })}
      </div>

      {/* v0.8.296 — zoom kontrolleri (transform DIŞINDA, sabit köşe) */}
      <div className="topo-zoomctl">
        <Button variant="secondary" size="sm" aria-label="Yakınlaştır" title="Zoom in" onClick={() => zoomStep(1.3)}>+</Button>
        <span className="pct">{Math.round(vp.k * 100)}%</span>
        <Button variant="secondary" size="sm" aria-label="Uzaklaştır" title="Zoom out" onClick={() => zoomStep(1 / 1.3)}>−</Button>
        <Button variant="secondary" size="sm" aria-label="Sığdır" title="Fit"
          onClick={() => setVp(fitViewport(layoutW, layoutH, wrapRef.current?.clientWidth ?? width, height))}>⛶</Button>
      </div>
    </div>
  );
}

function displayLabel(n: ServiceMapNode): string {
  return n.subkind || n.service.replace(/^(db|queue|ext):/, '');
}
// fmtMs — kompakt latency etiketi (ServiceGraph.fmtMs ile aynı biçim; chip
// ile canvas inspector aynı okunur).
function fmtMs(ms: number): string {
  if (!ms) return '—';
  if (ms >= 1000) return (ms / 1000).toFixed(ms >= 10_000 ? 0 : 1) + 's';
  return Math.round(ms) + 'ms';
}
function depLabel(kind: string): string {
  switch (kind) {
    case 'db': return 'database';
    case 'queue': return 'queue';
    case 'external': return 'dış bağımlılık';
    default: return kind;
  }
}
