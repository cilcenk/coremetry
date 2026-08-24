export type StatTileProps = {
  /** Uppercased at render — pass it in ordinary case. */
  label: string;
  /** `err` / `warn` recolour the VALUE only; the frame stays neutral. */
  tone?: 'err' | 'warn';
  children: React.ReactNode;
};

/**
 * StatTile — the boxed label+value tile the detail pages use for window
 * totals (v0.9.1375).
 *
 * Three byte-for-byte copies existed, one per detail surface:
 * `EndpointDetail.Stat`, `databases/detailSections.Stat` and
 * `slowqueries/stmtDetailSections.HeaderStat`. The first two were
 * identical apart from `children` vs `value: string` — a difference in
 * the SIGNATURE, not the rendering. The third had drifted: its label was
 * neither uppercased nor letter-spaced, and it was missing `minWidth: 0`.
 *
 * That drift is the argument for merging rather than the merge being
 * cosmetic housekeeping. Nothing failed when the statement tile fell out
 * of step; it simply looked like a slightly different product on one
 * page, and no gate could see it because there was no shared definition
 * to disagree with. `minWidth: 0` matters beyond looks — without it a
 * grid child refuses to shrink below its content, so a long value pushes
 * the tile past its column instead of ellipsing inside it.
 *
 * `children` (not `value: string`) is the wider contract: the endpoint
 * and database tiles pass a value plus a `<TrendDelta>`, and a
 * string-only prop would have forced them to keep their own copy — which
 * is exactly how three copies happened.
 *
 * SCOPE, stated plainly: this merges the three DETAIL-PAGE tiles. Six
 * more `Stat`-shaped locals live on other surfaces (Pod, Traces,
 * DBQueriesPanel, FocusedNeighborhood, dependencies/shared,
 * endpoints/MetricTile) with different frames, sizes and props. They are
 * NOT the same component wearing different names, so folding them in
 * here would mean redesigning those surfaces under cover of a refactor.
 */
export function StatTile({ label, tone, children }: StatTileProps) {
  return (
    <div style={{
      padding: '8px 10px', border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg1)', minWidth: 0,
    }}>
      {/* Merdiven token'ları — `components/ui` altındaki atomlar ham
          geometri sayısı kullanamıyor (geometryTokens kapısı, taşıma
          sırasında ısırdı). Değerler AYNI piksel: --fs-2xs = 10px
          ("mikro etiket, uppercase başlık" — tam bu kullanım),
          --sp-1 = 2px. Görsel değişiklik yok. */}
      <div style={{
        fontSize: 'var(--fs-2xs)', color: 'var(--text3)', marginBottom: 'var(--sp-1)',
        textTransform: 'uppercase', letterSpacing: 0.4,
      }}>{label}</div>
      <div className="mono" style={{
        fontSize: 15, fontWeight: 600,
        color: tone === 'err' ? 'var(--err)' : tone === 'warn' ? 'var(--warn)' : 'var(--text)',
      }}>{children}</div>
    </div>
  );
}
