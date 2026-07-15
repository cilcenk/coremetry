import { useEffect, useMemo, useRef, useState } from 'react';
import type { SpanRow } from '@/lib/types';
import { fmtNs, displaySpanName } from '@/lib/utils';

// HH:MM:SS.mmm wall-clock formatter for the waterfall ruler +
// per-span tooltips. Locked to the browser's local timezone so
// the value matches whatever clock the operator's logs already
// show; UTC would require a mental shift mid-incident.
function fmtClock(ns: number): string {
  const d = new Date(ns / 1e6);
  const pad2 = (n: number) => n.toString().padStart(2, '0');
  const pad3 = (n: number) => n.toString().padStart(3, '0');
  return `${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}.${pad3(d.getMilliseconds())}`;
}

const TICKS = [0, 0.25, 0.5, 0.75, 1];
const NAME_MIN = 160;
const NAME_MAX = 800;
const INDENT_PX = 16;
// v0.8.536 — clearance an outside duration label needs to render fully.
// fmtNs tops out around 7 monospace chars ("123.45ms") ≈ 48px at 10px,
// plus the 4px gap, plus slack. Used to decide which SIDE of the bar the
// label goes on, never whether it renders.
const OUTSIDE_LABEL_MIN_PX = 60;

interface Row {
  span: SpanRow;
  depth: number;
  hasChildren: boolean;
  // For each ancestor depth (1..depth), whether the ancestor at that
  // depth still has later siblings in the visible tree. Drives the
  // continuation tree lines: at depths where the ancestor has more
  // siblings, the vertical guide line extends through this row.
  // Standard Tempo / Jaeger waterfall convention.
  ancestorContinues: boolean[];
  isLastSibling: boolean;
  // Group annotations — populated when `groupSimilar` is on AND
  // multiple sibling spans collapsed into this synthetic row.
  // Drives the "×N" badge + the aggregated tooltip stats.
  groupCount?: number;
  groupTotalDur?: number;  // ns, sum across members
  groupAvgDur?: number;    // ns
  groupMaxDur?: number;    // ns
  hasError?: boolean;      // any member errored — error stripe still wins
}

// Span kind is exposed via tooltips on the row name only — we
// used to render an emoji glyph (🖥 / 📡 / 📤 / 📥 / ⚙) per
// row, but the visual noise added more than it surfaced (the
// kind is rarely the operator's first question and the
// service-name + category-chip already separate request-side
// from infra-side spans).

// Span category chip — Uptrace/SigNoz convention. The eye scans the
// category column and immediately sees "this is a DB call vs an
// outbound HTTP vs a Kafka publish", which is faster than parsing
// the operation name.
//
// Detection runs against the OTel semantic conventions: presence of
// `db.system` is the tell for a DB span, `messaging.system` for a
// queue/topic span, etc. Order matters — e.g. an HTTP span calling a
// gRPC server has both `rpc.system` and (rarely) `http.method`; we
// pick RPC first because that's what's actually being executed.
type SpanCategory = { tag: string; color: string };
// v0.5.249 — modern category chip palette aligned to the
// Tailwind v3 -500 family (matches the new hashColor palette
// + the rest of the UI's modern hue stack). The previous muted
// Tempo-classic chips lacked saturation; on dark UI they
// blended into the bar band. The -500 shades have consistent
// lightness so all four categories read at the same visual
// weight regardless of which one is most common in a trace.
function categoryOf(s: SpanRow): SpanCategory | null {
  const a = s.attributes ?? {};
  if (a['db.system'])        return { tag: 'DB',   color: 'var(--warn)' };   // amber
  if (a['messaging.system']) return { tag: 'MQ',   color: 'var(--teal)' };   // cyan/teal
  if (a['rpc.system'])       return { tag: 'RPC',  color: 'var(--purple)' }; // violet
  if (a['http.method'] || a['http.request.method']) {
    return { tag: 'HTTP', color: 'var(--accent)' };                          // blue
  }
  return null;
}

// Stable per-service bar/stripe colour from the globals.css chart-token
// palette (token-only, light+dark safe). Hash the service name so every span
// from the same service shares a colour — the scan-handoffs convention. Five
// well-separated hues (blue/purple/teal/orange/green); collisions across a
// large service set are acceptable (the design's SVC_COLOR reuses hues too).
const SVC_TOKENS = ['var(--accent)', 'var(--purple)', 'var(--teal)', 'var(--orange)', 'var(--ok)'];
export function svcColorToken(name: string): string {
  let h = 5381;
  for (let i = 0; i < name.length; i++) h = ((h << 5) + h) ^ name.charCodeAt(i);
  return SVC_TOKENS[Math.abs(h) % SVC_TOKENS.length];
}

// TraceServiceBreakdown — per-service SELF-time share of the trace
// (span duration minus the sum of its direct children, clamped to 0),
// rendered as a horizontal stacked strip + a top-5 legend. The Jaeger
// trace-summary pattern: "stripe-api ate 83% of these 4.8s" at a
// glance. Colours come from the same svcColorToken hash the waterfall
// stripes use so the strip and the rows read as one palette.
export function TraceServiceBreakdown({ spans }: { spans: SpanRow[] }) {
  const breakdown = useMemo(() => {
    // O(n): one pass to sum direct-child durations per parent,
    // one pass to fold self-time per service.
    const childSum = new Map<string, number>();
    for (const s of spans) {
      if (!s.parentSpanId) continue;
      childSum.set(s.parentSpanId,
        (childSum.get(s.parentSpanId) ?? 0) + (s.endTime - s.startTime));
    }
    const bySvc = new Map<string, number>();
    for (const s of spans) {
      const self = Math.max(0, (s.endTime - s.startTime) - (childSum.get(s.spanId) ?? 0));
      bySvc.set(s.serviceName, (bySvc.get(s.serviceName) ?? 0) + self);
    }
    const total = [...bySvc.values()].reduce((a, b) => a + b, 0) || 1;
    return [...bySvc.entries()]
      .sort((a, b) => b[1] - a[1])
      .map(([svc, ns]) => ({ svc, ns, pct: (ns / total) * 100 }));
  }, [spans]);

  if (breakdown.length === 0) return null;
  return (
    <div style={{ marginBottom: 10 }}>
      <div className="wf-svcbreak" role="img" aria-label="Self-time share per service">
        {breakdown.map(b => (
          <i key={b.svc}
             style={{ width: `${b.pct}%`, background: svcColorToken(b.svc) }}
             title={`${b.svc} — ${fmtNs(b.ns)} self time (${b.pct.toFixed(b.pct < 1 ? 1 : 0)}%)`} />
        ))}
      </div>
      <div className="wf-svcbreak-legend">
        {breakdown.slice(0, 5).map(b => (
          <span className="it" key={b.svc}>
            <span className="sw" style={{ background: svcColorToken(b.svc) }} />
            {b.svc} <span className="ms">{fmtNs(b.ns)} · {b.pct.toFixed(b.pct < 1 ? 1 : 0)}%</span>
          </span>
        ))}
        {breakdown.length > 5 && (
          <span className="it" style={{ color: 'var(--text3)' }}>
            +{breakdown.length - 5} more
          </span>
        )}
      </div>
    </div>
  );
}

export function TraceWaterfall({
  spans, selectedId, onSelect, defaultCollapsed, groupSimilar = false,
  criticalPathIds, matchIds, focusIds, logSignals, onLogsClick,
}: {
  spans: SpanRow[];
  selectedId: string | null;
  onSelect: (id: string) => void;
  // When true, every span that has children starts collapsed —
  // the user sees only the root row(s) and clicks ▶ to drill in.
  // Used by the service-structure view so the operator scans
  // top-level shape first instead of being faced with a 200-span
  // waterfall on mount.
  defaultCollapsed?: boolean;
  // When true, sibling spans sharing the same (service, displayName)
  // collapse to a single "×N" row whose children come from the
  // longest member (representative subtree). Cuts noise on tight-
  // loop patterns like N+1 DB queries — used by the service-
  // structure waterfall, off by default in the regular trace view.
  groupSimilar?: boolean;
  // Optional set of span IDs on the trace's critical path. Rows
  // matching these get the .wf-critical class — left-edge red
  // accent stripe — so the operator sees at a glance which
  // spans actually drive the wall-clock latency. Computed once
  // per trace via lib/criticalPath.ts; we just take the result.
  criticalPathIds?: Set<string>;
  // v0.5.383 — in-trace span filter. Matching span IDs get the
  // .wf-match class (highlight); non-matches get .wf-dim (low
  // opacity). Undefined = no filter active, every row renders
  // normally. Tree structure is unchanged so the operator can
  // still read the call hierarchy around each match.
  matchIds?: Set<string>;
  // v0.8.407 — trace↔log correlation chips. spanId → correlated-row
  // count (span events always; ES logs after the Logs tab's lazy
  // fetch has run once — react-query cache, zero extra queries).
  // Clicking the chip jumps to the Logs tab via onLogsClick.
  logSignals?: Map<string, { n: number; err: boolean }>;
  onLogsClick?: (spanId: string) => void;
  // Critical-path FOCUS mode — rows outside this set get .wf-dim,
  // on top of (not instead of) the .wf-critical left stripe.
  // Undefined = focus off. Independent from criticalPathIds so
  // the stripe toggle and the focus toggle compose freely.
  focusIds?: Set<string>;
}) {
  // Memoise the parents-of-something set keyed by the spans array
  // identity. When defaultCollapsed is on, that set becomes the
  // initial collapsed Set; otherwise we start with an empty Set.
  // Keys depend on spans only — re-renders that just change
  // selectedId / nameWidth don't reset the user's expansions.
  // v0.8.199 (scale-audit) — auto-collapse a LARGE trace even when the caller
  // didn't pass defaultCollapsed. A batch/fan-out trace of thousands–tens of
  // thousands of spans (GetTrace returns up to 50k) painted FULLY EXPANDED locks
  // the main thread on first paint. content-visibility (row style below) lets the
  // browser skip off-screen rows, but at the worst case the DOM-node count alone
  // is too much, so collapse the parent subtrees and let the operator expand in.
  const initialCollapsed = useMemo(() => {
    const LARGE_TRACE = 1500;
    if (!defaultCollapsed && spans.length <= LARGE_TRACE) return new Set<string>();
    const parents = new Set<string>();
    for (const s of spans) if (s.parentSpanId) parents.add(s.parentSpanId);
    return parents;
  }, [spans, defaultCollapsed]);

  const [collapsed, setCollapsed] = useState<Set<string>>(initialCollapsed);
  // Re-sync when spans flip (a fresh trace replaces the previous
  // one) so the new structure also opens collapsed.
  useEffect(() => { setCollapsed(initialCollapsed); }, [initialCollapsed]);
  const [nameWidth, setNameWidth] = useState<number | null>(null);
  const [containerWidth, setContainerWidth] = useState(0);
  const containerRef = useRef<HTMLDivElement>(null);
  const dragRef = useRef<{ startX: number; startW: number } | null>(null);

  useEffect(() => {
    if (!containerRef.current) return;
    setContainerWidth(containerRef.current.clientWidth);
    const ro = new ResizeObserver(entries => setContainerWidth(entries[0].contentRect.width));
    ro.observe(containerRef.current);
    return () => ro.disconnect();
  }, []);

  const { rows, minT, totalNs, maxDepth } = useMemo(() => {
    const map = new Map(spans.map(s => [s.spanId, s]));
    const children = new Map<string, SpanRow[]>(spans.map(s => [s.spanId, []]));
    const roots: SpanRow[] = [];
    for (const s of spans) {
      if (s.parentSpanId && map.has(s.parentSpanId)) children.get(s.parentSpanId)!.push(s);
      else roots.push(s);
    }
    children.forEach(c => c.sort((a, b) => a.startTime - b.startTime));
    roots.sort((a, b) => a.startTime - b.startTime);

    const out: Row[] = [];

    // Group sibling kids by (service, displayName) when groupSimilar
    // is on. Each output entry represents either a single span or a
    // group of N>1 siblings sharing the same identity. Order is
    // chronological by the earliest member's start time so the
    // wall-clock shape of the trace is preserved.
    type ChildEntry = { kind: 'single'; span: SpanRow }
                    | { kind: 'group'; members: SpanRow[]; rep: SpanRow;
                        minStart: number; maxEnd: number;
                        totalDur: number; maxDur: number;
                        anyError: boolean; key: string };
    const groupKey = (sp: SpanRow) => sp.serviceName + '\x01' + displaySpanName(sp);
    const groupChildren = (kids: SpanRow[]): ChildEntry[] => {
      if (!groupSimilar) {
        return kids.map(s => ({ kind: 'single', span: s }));
      }
      const buckets = new Map<string, SpanRow[]>();
      const order: string[] = [];
      for (const k of kids) {
        const key = groupKey(k);
        if (!buckets.has(key)) { buckets.set(key, []); order.push(key); }
        buckets.get(key)!.push(k);
      }
      // Order buckets by earliest member start time.
      order.sort((a, b) => {
        const aMin = buckets.get(a)!.reduce((m, x) => x.startTime < m ? x.startTime : m, Infinity);
        const bMin = buckets.get(b)!.reduce((m, x) => x.startTime < m ? x.startTime : m, Infinity);
        return aMin - bMin;
      });
      return order.map(key => {
        const members = buckets.get(key)!;
        if (members.length === 1) {
          return { kind: 'single', span: members[0] };
        }
        let minStart = Infinity, maxEnd = -Infinity, totalDur = 0, maxDur = 0;
        let rep = members[0]; let repDur = 0;
        let anyError = false;
        for (const m of members) {
          const dur = m.endTime - m.startTime;
          totalDur += dur;
          if (dur > maxDur)  maxDur = dur;
          if (dur > repDur)  { rep = m; repDur = dur; }
          if (m.startTime < minStart) minStart = m.startTime;
          if (m.endTime   > maxEnd)   maxEnd = m.endTime;
          if (m.statusCode === 'error') anyError = true;
        }
        return { kind: 'group', members, rep, minStart, maxEnd,
                 totalDur, maxDur, anyError, key };
      });
    };

    const dfs = (id: string, depth: number, isLast: boolean, ancestorContinues: boolean[]) => {
      const s = map.get(id); if (!s) return;
      const kids = children.get(id) ?? [];
      out.push({ span: s, depth, hasChildren: kids.length > 0, ancestorContinues, isLastSibling: isLast });
      if (collapsed.has(id)) return;
      const entries = groupChildren(kids);
      entries.forEach((entry, i) => {
        const last = i === entries.length - 1;
        if (entry.kind === 'single') {
          dfs(entry.span.spanId, depth + 1, last, [...ancestorContinues, !isLast]);
          return;
        }
        // Synthetic group row — represents N siblings with the same
        // (service, displayName). Children come from the longest
        // member's subtree (representative) so the operator still
        // sees a typical call shape under the group.
        const synthId = `group:${id}:${i}:${entry.key}`;
        const repKids = children.get(entry.rep.spanId) ?? [];
        const synthSpan: SpanRow = {
          ...entry.rep,
          spanId: synthId,
          startTime: entry.minStart,
          endTime:   entry.maxEnd,
          // If any member errored, mark the group; otherwise inherit.
          statusCode: entry.anyError ? 'error' : entry.rep.statusCode,
        };
        out.push({
          span: synthSpan,
          depth: depth + 1,
          hasChildren: repKids.length > 0,
          ancestorContinues: [...ancestorContinues, !isLast],
          isLastSibling: last,
          groupCount: entry.members.length,
          groupTotalDur: entry.totalDur,
          groupAvgDur: entry.totalDur / entry.members.length,
          groupMaxDur: entry.maxDur,
          hasError: entry.anyError,
        });
        if (collapsed.has(synthId)) return;
        // Recurse into the rep's children directly (one level
        // deeper than the synthetic row).
        repKids.forEach((c, j) => {
          const lastChild = j === repKids.length - 1;
          dfs(c.spanId, depth + 2, lastChild,
              [...ancestorContinues, !isLast, !last]);
        });
      });
    };
    roots.forEach((r, i) => dfs(r.spanId, 0, i === roots.length - 1, []));

    const minT = Math.min(...spans.map(s => s.startTime));
    const maxT = Math.max(...spans.map(s => s.endTime));
    const totalNs = maxT - minT || 1;
    const maxDepth = out.reduce((m, r) => Math.max(m, r.depth), 0);
    return { rows: out, minT, totalNs, maxDepth };
  }, [spans, collapsed, groupSimilar]);

  const defaultNameWidth = useMemo(() => {
    if (containerWidth <= 0) return 380;
    const target = Math.round((containerWidth - 6) * 0.4);
    const depthMin = Math.min(320, 220 + maxDepth * INDENT_PX);
    return Math.max(NAME_MIN, depthMin, Math.min(target, NAME_MAX, containerWidth * 0.65));
  }, [containerWidth, maxDepth]);

  const colWidth = nameWidth ?? defaultNameWidth;

  const onResizeStart = (e: React.MouseEvent) => {
    e.preventDefault();
    dragRef.current = { startX: e.clientX, startW: colWidth };
    document.body.style.cursor = 'col-resize';
    document.body.style.userSelect = 'none';
  };

  useEffect(() => {
    const onMove = (e: MouseEvent) => {
      if (!dragRef.current) return;
      const w = dragRef.current.startW + (e.clientX - dragRef.current.startX);
      setNameWidth(Math.max(NAME_MIN, Math.min(NAME_MAX, w)));
    };
    const onUp = () => {
      if (!dragRef.current) return;
      dragRef.current = null;
      document.body.style.cursor = '';
      document.body.style.userSelect = '';
    };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
    return () => {
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
    };
  }, []);

  const onResizeDoubleClick = () => setNameWidth(null);

  const toggle = (id: string, e: React.MouseEvent) => {
    e.stopPropagation();
    const next = new Set(collapsed);
    if (next.has(id)) next.delete(id); else next.add(id);
    setCollapsed(next);
  };

  // Stable per-service colour (the left stripe + the bar + the
  // service badge). Hashing on serviceName ALONE means every span
  // emitted by `user-service` gets the same colour anywhere in the
  // trace — the standard Uptrace/Tempo convention for "scan the row
  // colours to spot service handoffs". (Earlier we hashed on
  // serviceName+name, which gave each operation in a service its
  // own colour and made traces look noisier than the topology
  // actually was.)
  // Token-only service palette (the design's SVC_COLOR map, generalised to
  // the dynamic service set via a name hash). Error spans keep their service
  // colour and get a red inset outline on the bar (see the bar style below) —
  // matching the mockup, where bars are coloured by service and the red edge
  // marks the error/critical path.
  const colorFor = (s: SpanRow) => svcColorToken(s.serviceName);
  // Critical-path toggle is "on" exactly when the parent passes the id set;
  // then non-critical bars drop to 0.62 opacity (design: critOn && !crit).
  const criticalActive = criticalPathIds !== undefined;

  return (
    <div id="wf-outer" ref={containerRef}>
      <div className="wf-header">
        <div className="wf-col-name" style={{ width: colWidth }}>Span</div>
        <div className="wf-resizer"
          title="Drag to resize · double-click to auto-fit"
          onMouseDown={onResizeStart}
          onDoubleClick={onResizeDoubleClick} />
        <div className="wf-col-bar">
          {TICKS.map(t => {
            // Edge ticks need alignment overrides — the parent
            // has overflow:hidden, so the default centred
            // transform clips the trailing half of "583.45ms"
            // on the t=1 tick (which is the trace total!) and
            // the leading half of "0ms" on t=0. Datadog /
            // Tempo align-left at t=0 and align-right at t=1
            // so the edge labels stay readable in full.
            const transform =
              t === 0 ? 'translate(0, -50%)'
              : t === 1 ? 'translate(-100%, -50%)'
              : 'translate(-50%, -50%)';
            const offsetNs = t * totalNs;
            return (
              <span key={`l${t}`}>
                <span className="wf-tick-label"
                      style={{ left: `${t * 100}%`, transform,
                               display: 'inline-flex',
                               flexDirection: 'column',
                               alignItems: t === 0 ? 'flex-start'
                                         : t === 1 ? 'flex-end'
                                         : 'center',
                               lineHeight: 1.1 }}
                      title={`Absolute: ${fmtClock(minT + offsetNs)}  ·  Offset: +${fmtNs(offsetNs)}`}>
                  {/* Absolute wall-clock — small, top line.
                      Operators correlate with logs / dashboards
                      that all show real time, not "+150ms". */}
                  <span style={{ fontSize: 10, opacity: 0.7,
                                 fontVariantNumeric: 'tabular-nums' }}>
                    {fmtClock(minT + offsetNs)}
                  </span>
                  {/* Relative offset — primary label, same look
                      as before so the bar-layout scan still
                      anchors on it. */}
                  <span>{fmtNs(offsetNs)}</span>
                </span>
                {t > 0 && <div className="wf-vline" style={{ left: `${t * 100}%` }} />}
              </span>
            );
          })}
        </div>
      </div>

      {rows.map(({ span: s, depth, hasChildren, ancestorContinues, isLastSibling,
                    groupCount, groupTotalDur, groupAvgDur, groupMaxDur }) => {
        const color = colorFor(s);
        const cat = categoryOf(s);
        const startPct = ((s.startTime - minT) / totalNs * 100).toFixed(4);
        const widthPct = Math.max(0.15, ((s.endTime - s.startTime) / totalNs) * 100).toFixed(4);
        const dur = s.endTime - s.startTime;
        // Replace generic gRPC names ("grpc command", "rpc", bare method)
        // with a richer label derived from rpc.* / peer.service attrs.
        const displayName = displaySpanName(s);
        const durMs = dur / 1e6;
        const isCol = collapsed.has(s.spanId);
        const sel = s.spanId === selectedId;
        const onCritical = criticalPathIds?.has(s.spanId) ?? false;
        // v0.5.383 — in-trace filter classes. matchIds undefined →
        // no filter active, every row is "neutral". matchIds set →
        // matches glow (.wf-match), non-matches dim (.wf-dim).
        const filterActive = matchIds !== undefined;
        const onMatch = filterActive && matchIds!.has(s.spanId);
        // Focus mode dims rows outside focusIds; the filter dims
        // non-matches. Either signal alone is enough to dim — a row
        // must survive BOTH active modes to stay full-opacity.
        const dimmed = (filterActive && !onMatch)
          || (focusIds !== undefined && !focusIds.has(s.spanId));
        const cls = [
          'wf-row',
          s.statusCode === 'error' ? 'wf-err' : '',
          sel ? 'wf-sel' : '',
          onCritical ? 'wf-critical' : '',
          filterActive && onMatch ? 'wf-match' : '',
          dimmed ? 'wf-dim' : '',
        ].filter(Boolean).join(' ');

        // Share of the trace's wall-clock total. Sub-1% spans keep one
        // decimal so a 0.4% hot path doesn't round to invisibility.
        const durPct = (dur / totalNs) * 100;
        const durPctLabel = durPct < 1 ? durPct.toFixed(1) : String(Math.round(durPct));

        // Exception marker — first `exception` event on the span. With
        // a usable timestamp the diamond sits at the exception moment;
        // otherwise it falls back to the bar's end.
        const exc = (s.events ?? []).find(e => e.name === 'exception');
        let excLeftPct: number | null = null;
        if (exc) {
          const t = exc.timeNano > 0 ? exc.timeNano : s.endTime;
          const clamped = Math.min(Math.max(t, s.startTime), s.endTime);
          excLeftPct = Math.min(((clamped - minT) / totalNs) * 100, 99);
        }

        // Decide whether the duration label fits inside the bar (Tempo
        // does this — short bars get the label outside-right). 60px is
        // roughly the width of "10.5ms" at the row's font size.
        const labelInside = parseFloat(widthPct) > 6;

        // v0.8.536 — an outside label is pinned to the bar's right, so
        // every span sitting near the end of the trace (the last, short
        // operation — nearly every trace has one) pushed its label past
        // #wf-outer's overflow:hidden and lost it. Flip to the left only
        // when the right is genuinely tight AND the left is genuinely
        // roomier, so the label can never end up worse off than before.
        //
        // .wf-row-bar's pixel width is exactly containerWidth - colWidth:
        // box-sizing is border-box globally, .wf-row-name's width already
        // contains its 1px border, and neither .wf-row nor .wf-row-bar
        // adds padding. containerWidth is 0 until the ResizeObserver
        // first fires, which lands both spaces on 0 and keeps the old
        // right-hand placement for that paint.
        const barAreaWidth = Math.max(0, containerWidth - colWidth);
        const endFrac = (parseFloat(startPct) + parseFloat(widthPct)) / 100;
        const spaceRightPx = barAreaWidth * Math.max(0, 1 - endFrac);
        const spaceLeftPx = barAreaWidth * (parseFloat(startPct) / 100);
        const labelLeft = !labelInside
          && spaceRightPx < OUTSIDE_LABEL_MIN_PX
          && spaceLeftPx > spaceRightPx + OUTSIDE_LABEL_MIN_PX;

        return (
          <div key={s.spanId} className={cls} onClick={() => onSelect(s.spanId)}
            style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 28px' }}>
            {/* Left stripe — solid 3px service-color marker so the eye
                can scan service handoffs down the trace. Selected row
                gets a brighter, wider stripe to mark focus. */}
            <div className="wf-stripe" style={{ background: color }} />

            <div className="wf-row-name" style={{ width: colWidth }}>
              {/* Tree guide lines — one vertical line per ancestor that
                  still has later siblings, plus an L-shape for the row
                  itself. Indent comes from the lines, not padding, so
                  the visualization is tight at every depth. */}
              {ancestorContinues.map((cont, i) => (
                <span key={i} className={`wf-tree-v${cont ? '' : ' wf-tree-v-empty'}`}
                      style={{ left: i * INDENT_PX + 4 }} />
              ))}
              {depth > 0 && (
                <span className={`wf-tree-elbow${isLastSibling ? ' wf-tree-elbow-last' : ''}`}
                      style={{ left: (depth - 1) * INDENT_PX + 4 }} />
              )}

              <div className="wf-row-name-inner" style={{ paddingLeft: depth * INDENT_PX + 8 }}>
                {hasChildren
                  ? <button className="wf-toggle" onClick={e => toggle(s.spanId, e)}
                            aria-label={isCol ? 'Expand' : 'Collapse'}
                            title={isCol ? 'Expand' : 'Collapse'}>
                      {isCol ? '▶' : '▼'}
                    </button>
                  : <div className="wf-leaf" />}
                {/* Service identifier — Tempo-style soft underline.
                    The service name reads as plain text with a 2px
                    underline in the per-service hash colour, so the
                    scan-down-the-column "this is service X" signal
                    survives without a high-contrast filled chip
                    competing with the operation name. */}
                <span className="wf-svc"
                      title={`service.name: ${s.serviceName}`}>
                  <span className="wf-svc-dot" style={{ background: color }} />
                  {s.serviceName}
                </span>
                {cat && (
                  <span className="wf-cat" title={`Category: ${cat.tag}`}
                        style={{ color: cat.color, borderColor: cat.color }}>
                    {cat.tag}
                  </span>
                )}
                <span className="wf-name" title={s.name === displayName ? s.name : `raw: ${s.name}`}>
                  {displayName}
                </span>
                {/* Group multiplier — only when this row collapses
                    N>1 sibling spans. Tooltip carries total / avg /
                    max duration so the operator can read group cost
                    without expanding. */}
                {groupCount && groupCount > 1 && (
                  <span className="wf-group"
                        title={
                          `${groupCount}× ${displayName}\n` +
                          `total: ${fmtNs(groupTotalDur ?? 0)}\n` +
                          `avg:   ${fmtNs(groupAvgDur ?? 0)}\n` +
                          `max:   ${fmtNs(groupMaxDur ?? 0)}\n` +
                          `representative subtree shown — click to drill into the actual trace`
                        }>
                    ×{groupCount}
                  </span>
                )}
                {s.statusCode === 'error' && (
                  <span className="wf-err-dot" title="Error">●</span>
                )}
                {(() => {
                  // v0.8.407 — "logs in context": correlated log/event
                  // count for THIS span; click opens the Logs tab.
                  const lc = logSignals?.get(s.spanId);
                  if (!lc || lc.n === 0) return null;
                  return (
                    <span
                      onClick={e => { e.stopPropagation(); onLogsClick?.(s.spanId); }}
                      title={`${lc.n} correlated log line${lc.n === 1 ? '' : 's'} / span event${lc.n === 1 ? '' : 's'} — open Logs tab`}
                      style={{
                        flexShrink: 0, cursor: onLogsClick ? 'pointer' : 'default',
                        fontSize: 10, lineHeight: 1, padding: '1px 4px',
                        borderRadius: 3, border: '1px solid var(--border)',
                        color: lc.err ? 'var(--err)' : 'var(--text3)',
                        fontFamily: 'ui-monospace, monospace',
                      }}>
                      ≡{lc.n}
                    </span>
                  );
                })()}
                <span className="wf-pct" title="Share of total trace duration">
                  {durPctLabel}%
                </span>
              </div>
            </div>

            <div className="wf-resizer-row" />

            <div className="wf-row-bar">
              {TICKS.filter(t => t > 0).map(t => (
                <div key={`v${t}`} className="wf-vline" style={{ left: `${t * 100}%` }} />
              ))}
              <div
                className="wf-bar"
                title={
                  `${displayName}\n${s.serviceName}\n` +
                  `start: ${fmtClock(s.startTime)}  (+${fmtNs(s.startTime - minT)})\n` +
                  `end:   ${fmtClock(s.endTime)}\n` +
                  `dur:   ${fmtNs(dur)} (${durMs.toFixed(2)}ms)`
                }
                style={{
                  left: `${startPct}%`, width: `${widthPct}%`, background: color,
                  opacity: criticalActive && !onCritical ? 0.62 : 1,
                }}
              >
                {labelInside && <span className="wf-bar-label">{fmtNs(dur)}</span>}
              </div>
              {excLeftPct !== null && (
                <span className="wf-ev" style={{ left: `${excLeftPct}%` }}
                      title={exc!.attributes?.['exception.type'] || 'exception'} />
              )}
              {!labelInside && (
                <span className={`wf-bar-label-outside${labelLeft ? ' wf-bar-label-outside-left' : ''}`}
                      style={{ left: labelLeft
                        ? `calc(${startPct}% - 4px)`
                        : `calc(${startPct}% + ${widthPct}% + 4px)` }}>
                  {fmtNs(dur)}
                </span>
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
}
