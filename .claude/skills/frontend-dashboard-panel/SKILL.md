---
name: frontend-dashboard-panel
description: Adding a new dashboard panel type (gauge / heatmap / table / etc) to Coremetry's Grafana-style dashboard system. Reach for this BEFORE editing frontend/src/components/dashboard/, lib/types.ts PanelType union, or adding a *PanelConfig interface. Captures the 7 touchpoints + variable substitution + bundle-fetch + zoom-sync conventions.
---

# /frontend-dashboard-panel — add a new dashboard panel type

Coremetry's dashboards are operator-curated, Grafana-style grids
of typed panels. The system is a tagged-union over a `PanelType`
discriminator — every panel knows how to fetch its own data and
render its own viz, dispatched by the central `PanelRenderer`.

Adding a new panel type is **7 coordinated edits** across types,
renderer, editor, defaults, persistence, viz, and bundle fetch. Miss
one and you ship something that the editor can create but the
renderer can't draw (or vice versa).

## When to add a panel type vs. extending an existing one

Add a panel type when:
- The data shape is fundamentally different (e.g. `heatmap` 2-D
  buckets vs `metric`'s time series, `table` rows vs `stat`'s
  single number).
- The interaction is different (e.g. `topology` drag-pan + click
  vs `metric`'s drag-to-zoom).
- Existing config doesn't extend naturally — you'd be union-ing
  unrelated optional fields onto an existing config.

Don't add a panel type for:
- A new visualisation shape over the same data (`bar` / `area` /
  `stacked-bar` already exist as `PanelVizType` under
  `SpanMetricPanelConfig`).
- A new aggregation (extend the `agg` string list in the existing
  config).
- A new threshold rule, color rule, or unit (extend the existing
  StatPanelConfig).

If you can solve it with a new field on an existing config, do
that. New panel type = ~150 lines + 7 file touches.

## Existing catalogue (v0.6.40)

| `PanelType` | Config interface | Component | Used for |
|---|---|---|---|
| `metric` | `MetricPanelConfig` | `MetricPanel` | OTel metric series |
| `spanmetric` | `SpanMetricPanelConfig` | `SpanMetricPanel` | Span-derived RED + custom DSL |
| `stat` | `StatPanelConfig` | `StatPanel` | Single-number tile with delta + threshold |
| `gauge` | `GaugePanelConfig` | `GaugePanel` | Semicircle dial with threshold zones |
| `markdown` | `MarkdownPanelConfig` | `MarkdownPanel` | Static notes |
| `row` | (layout marker) | n/a (intercepted by Dashboard.tsx) | Section divider |

Naming: PanelType is lowercase no-space (`heatmap`, not `Heatmap`).
Config interface is `<PascalCase>PanelConfig`. Component is
`<PascalCase>Panel`.

## Steps

### 1. Define `<X>PanelConfig` in `frontend/src/lib/types.ts`

The "Dashboards" section in types.ts is the single source of truth
for what a config holds. Follow the patterns:

- Required scalar fields first, optional `?:` fields after.
- Don't include `type` — that lives on the `Panel` wrapper.
- Reuse existing sub-types where shape matches (e.g. thresholds
  use the same `{ value: number; color: 'green' | 'amber' | 'red' }[]`
  shape across `StatPanelConfig` and `GaugePanelConfig` — copy that
  shape, don't invent a new one).
- If the panel embeds existing data sources, reference them by
  embedding the config interface: `metric?: MetricPanelConfig`
  (Stat / Gauge already do this — gives the operator the full
  fetch-shape vocabulary for free).

### 2. Extend the `PanelType` union (same file)

```ts
export type PanelType =
  'metric' | 'spanmetric' | 'stat' | 'gauge' | 'markdown' | 'row'
  | 'heatmap';  // ← new
```

TypeScript will flag every site that needs a new case. Walk the
errors before doing step 3 — they're your checklist.

### 3. Add the default config in `PanelEditor.tsx` `defaultConfig`

```ts
case 'heatmap': return { source: 'spanmetric', span: { agg: 'p99' }, bucketSecs: 60 };
```

The default must be:
- Renderable WITHOUT any further operator input (no required
  free-text fields like `metricName: ''`).
- Sane for the most common case — a metric panel with `agg: 'avg'`
  feels right for the operator who just clicked "Add metric panel".

### 4. Add the editor form (same file)

The editor renders one form per panel type beside the type-picker.
Follow the existing pattern — `<label>`-wrapped inputs, no fancy
state library, just controlled inputs that mutate `panel.config`
through the parent's `onChange`. Reuse the visual components
(`ServicePicker`, `MetricNamePicker`, `<select>` for enums) so the
editor stays visually consistent across panel types.

### 5. Add `TYPE_LABELS` entry (same file)

```ts
const TYPE_LABELS: Record<PanelType, string> = {
  metric: 'Metric', spanmetric: 'Span metric',
  stat: 'Stat', gauge: 'Gauge', markdown: 'Markdown',
  row: 'Row divider',
  heatmap: 'Heatmap',  // ← new
};
```

This is what the operator sees in the type dropdown. Keep it
short — fits in a chip.

### 6. Add the renderer dispatch in `PanelRenderer.tsx`

```tsx
switch (panel.type) {
  // ... existing cases ...
  case 'heatmap':
    return <HeatmapPanel cfg={applyVarsToHeatmap(panel.config as HeatmapPanelConfig, vars)} range={effectiveRange} syncKey={syncKey} onZoom={onZoom} />;
}
```

And implement the component itself in the same file (or a sibling
file if it's >200 lines). The component:
- Takes `cfg`, `range`, optional `syncKey`, optional `onZoom`,
  optional `dataOverride`.
- Owns its own fetch via `useEffect([rangeNs.from, rangeNs.to, cfg.*])`.
- Renders `<Spinner/>` while loading, an inline error string on
  error (NEVER a blank panel — that's the v0.6.31 / "panel just
  empty" anti-pattern operators report as bugs).
- Returns the viz wrapped in the standard `<div className="panel-body">`.

### 7. Implement `applyVarsTo<X>(cfg, vars)`

Variables let dashboards reference `${service}` etc. in DSL
strings, then a Topbar picker swaps them at render time. The
function takes the static config + a vars map and returns a new
config with `${name}` placeholders expanded.

Pattern (copy `applyVarsToMetric` from PanelRenderer):

```ts
function applyVarsToHeatmap(cfg: HeatmapPanelConfig, vars?: Record<string, string>): HeatmapPanelConfig {
  return { ...cfg,
    dsl:     expand(cfg.dsl, vars),
    filters: expand(cfg.filters, vars),
    // expand each string field that might contain ${vars}
  };
}
```

Critically: if the var is empty (operator hasn't picked yet), the
expanded result should DROP the predicate, not produce `service.name
= ""`. The shared `expand` helper handles this — use it.

### 8. (Optional) Wire into the bundle fetch

`Dashboard.tsx` issues a single `/api/dashboards/{id}/bundle`
request that prefetches every panel's data so the grid doesn't
fire N round-trips. If the new panel type uses the same `/api/spans/metric`
or `/api/metrics/metric` endpoint as existing types, no change
needed — the bundle handler dispatches per panel.

If the new panel hits a fundamentally different endpoint (e.g. a
heatmap endpoint), add it to the bundle handler in
`internal/api/api.go` (`getDashboardBundle`). Otherwise the panel
falls through to its own fetch path — works, just one extra
round-trip per panel.

### 9. Decide if cursor-sync + drag-zoom apply

Time-series panels share a `syncKey` so hovering one shows the
crosshair on all. Drag-zoom on any single-series panel re-points
the dashboard's TimeRange.

- New panel is **time-series**? Plumb `syncKey` + `onZoom` through.
  Use `MultiLineChart` / `DashboardViz` — they already speak this.
- New panel is **single-value** (stat / gauge)? Skip syncKey;
  there's nothing to hover.
- New panel is **non-temporal** (table, status grid, log feed)?
  Both skip. But emit a `data-panel-id` attribute on the root so
  the dashboard-level panel-actions menu can still anchor to it.

### 10. Per-panel time range override

`Panel` has an optional `rangeOverride: TimeRange` — a per-panel
window distinct from the dashboard Topbar. New panels MUST honour
this: `const effectiveRange = panel.rangeOverride ?? range`. Done
by PanelRenderer already for the existing panel types; don't
break the pattern.

If your panel's fetch is bundle-driven, `effectiveDataOverride`
becomes `undefined` when an override is set — the bundle was for
the dashboard range, not the override range. Pattern is already
in PanelRenderer; copy it.

### 11. Backend types if a new endpoint is involved

If you added an endpoint in step 8, the standard Coremetry
"When you ship a new feature" checklist (CLAUDE.md) applies:
chstore method → cache wrapper → auth gate → audit (if mutation)
→ /lib/types.ts response type → /lib/api.ts client method → tsc
gate → go build gate.

For panel viz that reuses existing endpoints, only steps 1-10 of
this skill apply.

### 12. Smoke-test the editor + render path

```
cd frontend && npx tsc --noEmit       # all switch cases covered
make audit                            # repo's hard-constraint lint
make docker-up                        # rebuild
```

Then in the browser: Dashboards → New → Add panel → pick the new
type → fill in defaults → Save → confirm it renders.

Three states to verify:
- Loading (refresh the page; should show spinner briefly)
- Loaded (data shows up)
- Empty (filter to a service with no data; should show "(no data
  for this range)" not a blank panel)

## Code patterns you should not invent locally

- **Don't fetch in PanelRenderer.** Fetch in the per-panel
  component. PanelRenderer is just a dispatcher.
- **Don't read `Date.now()` inside the fetch callback.** Use
  `timeRangeToNs(range)` once at the top of the component, then
  fetch with those exact ns values. v0.5.184 was the original
  "infinite refetch" bug.
- **Don't write a custom chart library.** `MultiLineChart` and
  `DashboardViz` wrap uPlot. CLAUDE.md hard rule: uPlot only;
  no Chart.js / Recharts / D3.
- **Don't render a blank panel on error.** Inline error text
  with a hint ("CH timeout — try a narrower range") beats a
  panel that looks broken.
- **Don't add a new "render mode" for the same data shape.**
  Add a `viz` field to the existing config instead (see
  PanelVizType in SpanMetricPanelConfig).

## Persistence

Dashboard JSON shape is just `{ panels: Panel[], … }` — the
existing serialiser writes whatever the operator's editor
produced. NO migration step is needed for a new PanelType as long
as the JSON shape stays parseable: panels with `type:
"heatmap"` saved before the renderer learns about them will fall
through to the `default:` branch (which renders nothing rather
than crashing). After the renderer ships, those panels start
rendering normally.

If the new panel's config has fields that need backward-compat
defaults when an older JSON is loaded, set them in `defaultConfig`
+ in the panel component's destructure (`const { bucketSecs = 60
} = cfg`).

## Anti-patterns

- **Don't add a panel type without an editor form.** The operator
  can't create what they can't edit.
- **Don't add an editor form without a renderer case.** The
  saved panel becomes invisible.
- **Don't skip `applyVarsTo<X>`.** Without it the panel ignores
  dashboard variables — operators report this as "${service}
  picker doesn't filter the panel".
- **Don't wire syncKey into a single-value panel.** Stat / gauge
  don't have a meaningful cursor; the prop is just unused noise.
- **Don't dispatch on `panel.config.type` or similar nested
  discriminators.** The tag is at the top level (`panel.type`).
  Nested type tags break TypeScript exhaustiveness on the
  `PanelType` union.
- **Don't introduce a new way to express filters.** DSL string +
  optional JSON FilterExpr[] is the contract every panel speaks
  to the backend.

## Pairs with

- `/clickhouse-schema` if your panel needs a new chstore query
  shape (e.g. heatmap bucket aggregation).
- `/otel-conventions` if your panel surfaces a new semconv field
  (e.g. gen_ai.* metrics) that isn't already mapped.
- `/release` to ship the multi-file commit.
