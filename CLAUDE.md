# Coremetry

## Constraints
- Production scale: 1000s of services, 10000s of operations, 1B+ spans/day
- Every picker must server-side search (no eager catalogue fetch)
- Every table over ~100 rows needs virtualization or pagination
- Every cache key must hash ALL inputs (no length-only digests)
- Long lists in DOM: use `content-visibility: auto` if not virtualized

## Conventions
- Release pattern: every functional change = new tag v0.5.X + git push + make docker-up
- Commit message — multi-line via heredoc; format:
  ```
  v0.5.X — short title (max 70 chars)

  Body: what changed, why, root cause if a bug fix. Wrap at 72 chars.
  Operator-reported bugs start with "Operator-reported: …".

  Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
  ```
- AI Copilot system prompts live in internal/copilot/copilot.go bottom
- All Copilot endpoints go through s.copilotExplain(r, ...) wrapper so the
  /ai surface attribution stays accurate (never call s.copilot.Explain directly)
- ServicePicker / OperationPicker / MetricNamePicker — use these, never
  <Combobox options={services|operations|metrics}>
- User-saved configs (alert presets, topology views, dashboards) reuse the
  saved_views CH table with page='<kind>', not a new schema
- Admin Settings edits → s.audit(r, "settings.X.update", ...)
- Permission gating: admin = Settings tab + destructive actions;
  editor = rule/preset/sampling edits; viewer = read-only

## Workflow
- "kuyruk" = show current queue (numbered, prioritised)
- "devam" = continue with the in-progress item
- After each release, rebuild image with: make docker-up (background OK,
  only one rebuild at a time — wait for the previous to finish)
- Bug reports: investigate root cause, write the fix as v0.5.X+1
  *immediately* after the prior release rather than batching
- Type-check / build before commit: `cd frontend && npx tsc --noEmit`,
  `go build ./...`

## Tech
- Frontend: React + Vite, no Tailwind, CSS variables in globals.css
- Backend: Go + ClickHouse + Redis (single binary)
- Versioning: git tags v0.5.X, COREMETRY_VERSION env supported for
  rebuildless override (resolution order: env > ldflag > /app/VERSION > "dev")
- Auth: viewer / editor / admin roles; auth.RequireRole(auth.RoleAdmin, h)
  or auth.RequireAnyRole(editorRoles, h)

## Performance pitfalls to avoid
- timeRangeToNs(range) inside IIFE / on every render — recomputes now()
  every render, breaks useEffect deps → infinite spinner. Memoize on
  `range` identity or pass `range` down and resolve internally.
- Table-layout: fixed + nowrap + small width → silently clips text. Use
  min-width + max-width + ellipsis + title for tooltip.
- Cache keys keyed on length only of a set → cross-set poisoning. Use a
  stable digest (sorted + FNV) of the set contents.
