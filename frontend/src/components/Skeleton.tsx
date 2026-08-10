// Skeleton placeholders — content-shaped loading states that
// telegraph "what's about to render here" instead of the
// generic Spinner. Same pattern Datadog / Linear / Honeycomb
// use; reduces perceived latency because the shape doesn't
// shift when the real data lands.
//
// All variants share a single keyframes shimmer (defined in
// globals.css under .skeleton-pulse) — one CSS animation
// driving every placeholder on the page is cheaper than
// per-element JS animations.

export function Skeleton({
  width = '100%', height = 14, radius = 4,
  inline = false,
}: {
  width?: number | string;
  height?: number | string;
  radius?: number;
  inline?: boolean;
}) {
  return (
    <span className="skeleton-pulse"
          style={{
            display: inline ? 'inline-block' : 'block',
            width, height, borderRadius: radius,
          }} />
  );
}

// TableSkeleton — drop-in replacement for `<Spinner />` inside
// list-page main areas. Renders a header row + N body rows
// with a consistent layout so the eye doesn't jump when the
// real table swaps in.
export function TableSkeleton({
  rows = 8,
  cols = 5,
  // First column sometimes wants to look like a fat
  // identifier (service name, trace id) — pass `wideFirst`
  // to get a wider first cell.
  wideFirst = true,
}: { rows?: number; cols?: number; wideFirst?: boolean }) {
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            {Array.from({ length: cols }).map((_, i) => (
              <th key={i}>
                <Skeleton width={i === 0 && wideFirst ? '60%' : '40%'}
                          height={10} />
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {Array.from({ length: rows }).map((_, ri) => (
            <tr key={ri}>
              {Array.from({ length: cols }).map((_, ci) => (
                <td key={ci}>
                  <Skeleton
                    width={ci === 0 && wideFirst ? `${50 + (ri % 3) * 10}%` : `${30 + (ri % 4) * 8}%`}
                    height={12}
                  />
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// CardSkeleton — for KPI tiles / dashboard panels where the
// real content is "label + big number + sparkline". Three
// stacked rectangles approximate the shape closely enough.
export function CardSkeleton({ height = 96 }: { height?: number }) {
  return (
    // mK11 (v0.9.921) — `.card-static`, `.card` DEĞİL. `.card`
    // globals.css'te İKİ KEZ tanımlı ve ikinci tanım `cursor: pointer` +
    // `:hover { border-color: accent }` taşıyor (gezinilebilir kart
    // ızgaraları için). Bir YÜKLEME iskeletine onu vermek iskeleti
    // tıklanabilir gösterirdi — operatör henüz var olmayan bir şeye
    // tıklamaya çalışır. Ayrı sınıf, aynı görsel kutu.
    <div className="card-static" style={{
      height,
      display: 'flex', flexDirection: 'column', gap: 'var(--sp-4)',
    }}>
      <Skeleton width="40%" height={10} />
      <Skeleton width="60%" height={22} />
      <Skeleton width="100%" height={6} />
    </div>
  );
}

// ListSkeleton — for collapsed/expanded vertical lists where
// the real rows are simple "icon + text + meta" (Logs page,
// Anomalies, etc.). Looser than TableSkeleton.
export function ListSkeleton({ rows = 8, height = 32 }: {
  rows?: number; height?: number;
}) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} style={{
          display: 'flex', alignItems: 'center', gap: 10,
          height,
          padding: '0 12px',
          borderBottom: '1px solid var(--border)',
        }}>
          <Skeleton width={70}  height={10} inline />
          <Skeleton width={40}  height={10} inline />
          <Skeleton width={120} height={10} inline />
          <Skeleton width="40%" height={10} inline />
        </div>
      ))}
    </div>
  );
}
