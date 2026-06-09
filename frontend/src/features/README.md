# Features

Feature-scoped folders for the larger pages. Each feature owns
its page component, feature-specific components, hooks, and
types in one place — so a contributor can grep `features/foo/`
to see everything that powers `/foo` without crawling
`pages/`, `components/`, `lib/`.

## Target structure (per feature)

```
features/
  anomalies/
    AnomaliesPage.tsx      ← the route component (`<AnomaliesPage>`)
    components/
      LogPatternsSection.tsx
      AnomalyCard.tsx
    hooks.ts               ← feature-local hooks (debouncers etc.)
    types.ts               ← feature-local TS types if any
    queries.ts             ← feature-specific query hooks (or
                              re-export from lib/queries when the
                              same hook is consumed by multiple
                              features)
    index.ts               ← re-export AnomaliesPage as default
```

## What stays in lib/queries vs. moves to features/

- **lib/queries/** — query hooks consumed by **multiple
  features**. Examples: `useHealth` (sidebar + system page),
  `useOpenProblemCount` (sidebar badge + /problems page),
  `useServiceMap` (/services + /service-map). Cross-feature
  shared.
- **features/X/queries.ts** — hooks used **only** by feature X.
  When a query starts being consumed by another feature, lift
  it back to lib/queries (don't import across features).

## Migration strategy

We're moving in **one feature per release**. The first migration
(this release) is `anomalies` because it's the largest single
page (827 lines) and already has its own query hook file in
`lib/queries/anomalies.ts`. Each follow-up release moves another
feature; the pattern stays consistent.

Old `src/pages/<Name>.tsx` is the staging area — pages live there
until they get a feature folder. They keep working unchanged
because `src/App.tsx` imports them by file path.

## What we don't restructure

- `components/ui/*` — design-system primitives, cross-feature
  by definition.
- `components/Topbar.tsx`, `Sidebar.tsx`, `AppShell.tsx` —
  app-shell, cross-feature.
- `components/Sparkline.tsx`, `MultiLineChart.tsx`,
  `TraceWaterfall.tsx`, `FlameGraph.tsx`, `ServiceMapGraph.tsx`
  — visualisations consumed by 2+ features. Stay shared.
- `lib/api.ts`, `lib/types.ts`, `lib/utils.ts` — typed boundary
  with the backend, no good reason to slice these by feature.
