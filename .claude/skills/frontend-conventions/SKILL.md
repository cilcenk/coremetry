---
name: frontend-conventions
description: Coremetry frontend house rules — design language, tables, pickers, URL-state discipline, theme tokens, polling, ES-cost UI patterns. Read BEFORE any frontend change that adds a component, table, filter, drawer, theme value, or polling loop.
---

# /frontend-conventions — Coremetry UI house rules

The operator-facing bar is Datadog/Dynatrace/Honeycomb for
BEHAVIOUR; the VISUAL language may lean Red Hat/PatternFly (see
§5). These are the concrete rules this codebase enforces — each
one exists because its violation shipped a bug or an incident.

## 1. One design language

- Buttons: the shared `<Button variant size>` atom
  (`components/ui/Button.tsx`). Never hand-roll
  `<button style={{fontSize, padding, border}}>` — the 2px6px vs
  3px7px drift class is what forced the atom (v0.7.54).
- Labelled inputs: `components/ui/Field.tsx`; badges `.badge
  .b-ok/.b-err/.b-warn/.b-info/.b-gray` or the typed `<Badge tone>`
  wrapper; cards/rows from `components/ui`.
- Tab strips: the shared `.tab-strip` + active-class buttons
  (Trace, PublicTrace, Events, doc viewer all use it — the
  operator's eye expects one tab anatomy).
- Icons: lucide-react (already a dep) or inline SVG; no new icon
  packages.

## 2. Tables

- EVERY data table adopts `useDataTable`
  (`components/DataTable.tsx`, pure core `lib/dataTable.ts`):
  sortable + column-resizable, widths/sort persisted by
  `storageKey`. Template: `SlowQueries.tsx`.
- Server-paged tables (Services, Traces, Logs) use serverSort /
  resize-only mode — client sort on one page of a server-ordered
  set is misleading (LOG_COLS comment, v0.7.54).
- Rows > 100 → `contentVisibility: 'auto'` +
  `containIntrinsicSize` (hard constraint). Don't virtualize when
  a deep-link needs the target row mounted (anomaly history).
- Header customisation goes through `DataTableHead`'s
  `renderLabel` hook — the pure core's `label: string` stays.

## 3. Pickers & catalogues

- Server-debounced pickers ONLY: `ServicePicker`,
  `OperationPicker`, `MetricNamePicker`. Never feed a full
  catalogue into a datalist/Combobox — and never validate a pick
  against a SAMPLED subset (v0.8.265: /service-map picks silently
  no-opped for any service outside the 200-trace sample).
- Small fixed sets (≤ ~10 values: clusters, channel kinds) may use
  a plain `<select>`.

## 4. URL is the source of truth

- Every operator selection that changes what's on screen writes to
  the URL with `setSearchParams(prev => …, { replace: true })`:
  drawers (`?problem=`, `?event=`), focus (`?focus=`), filters
  (`?filters=`, `?cols=`), tabs (`?tab=`), range (`?range=` via
  `useUrlRange`). Copy link / SavedViewsBar must reproduce the
  exact view. The one-way-read bug class (seeded FROM the URL,
  never written BACK) shipped three times — v0.8.256 problems,
  v0.8.265 service-map, v0.8.267 anomalies.
- URL→state import effects must be SIG-GUARDED (hash only the
  params you own, skip when unchanged) or any unrelated URL write
  (a range change) wipes locally-applied state — see Logs.tsx
  `urlSig`/`lastUrlSigRef` (v0.8.253).
- Preserve foreign params: always copy `prev`, never rebuild the
  query string from scratch.

## 5. Theme tokens

- CSS variables in `styles/globals.css` are the ONLY styling
  system. Three palettes: dark (default), light,
  `redhat` (v0.8.268 — PatternFly light + OpenShift-style dark
  sidebar; the operator's stated visual direction).
- Theme work stays TOKEN-level. Chrome variants use the scoped
  token remap trick (`[data-theme="redhat"] #sidebar { --bg1: … }`)
  — components never branch on the theme.
- Charts re-resolve CSS vars on `data-theme` mutation
  (`useThemeTick`); never hardcode palette hex in components
  (chart-series tokens exist: --purple/--orange/--teal…).
- No PatternFly/shadcn/Tailwind packages; no external font
  fetches (font-family preferences with system fallbacks only).

## 6. Data fetching, polling, ES cost

- React Query via `lib/queries/*`; keys include EVERY input.
  `timeRangeToNs(range)` only inside `useMemo`/`useEffect`
  (v0.5.184 infinite refetch).
- Polling ≥ 10s (only /api/health at 5s); RQ `refetchInterval`
  pauses on hidden tabs by default — raw `setInterval` needs the
  explicit `document.hidden` guard.
- ES-cost UI discipline (operator constraint, twice-stated): fetch
  on EXPAND/OPEN only — fields-panel accordion (v0.8.255), anomaly
  drawer chart (v0.8.267). Never prefetch across a list, never
  poll an expensive endpoint, `staleTime` ≥ the server cache TTL,
  and any param that rides a server cache key must have BOUNDED
  cardinality (window snapped to rungs, v0.8.270).
- Cursor paging accumulates via keepPreviousData + Load more
  (v0.8.260); reset accumulation on any slice change (v0.7.81).

## 7. States & structure

- Never a blank panel: `<Spinner/>` loading, `<Empty icon title>`
  (with a helpful body) for empty/error; tri-state
  `undefined | null | data` convention throughout.
- Shared data shapes live in `lib/types.ts` ONLY. API methods in
  `lib/api.ts`. Routes in `App.tsx`, sidebar in `Sidebar.tsx`.
- Big pages split into `pages/<page>/` or `features/<domain>/`
  modules (v0.8.269 shape); don't force splits on cohesive
  stateful cores — a 700-line coherent file beats a shattered one.
- Charts: uPlot only (operator rule) — no Chart.js or other chart
  deps.

## Gates

`cd frontend && npx tsc --noEmit && npm run lint && npm run test
-- --run` — 0 type errors, 0 lint ERRORS (warnings reviewed), all
vitest green. New pure logic (filter compilers, URL codecs,
segmenters) ships with a vitest file next to it.
