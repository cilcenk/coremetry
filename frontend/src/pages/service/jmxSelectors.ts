// Pure helpers for the Service→Infra JMX Pod/Datasource selectors
// (v0.9.150). Extracted from ServiceInfraTab after an adversarial review of
// v0.9.149 found SIX ways a stale or over-broad selector silently blanked the
// JMX section. Each helper below pins one of those failure modes; the sibling
// jmxSelectors.test.ts is the regression guard (CLAUDE.md #11).

// dsToken — the datasource component of a JMX series name. JMXTrend
// (internal/thanos/client.go) builds each name by joining the NON-EMPTY
// [data_source, xa_data_source, pod] label values with ' · ', so the
// datasource is ALWAYS the first token (pod, when present, is appended last)
// in both "By datasource" ("MyDS") and "By pod" ("MyDS · pod-x") modes. A
// non-datasource jboss metric (undertow/threads/transactions) groups to a
// single series whose labels are all empty → name "" → token "".
export function dsToken(name: string): string {
  return name.split(' · ')[0] ?? '';
}

// reconcile — honor a URL-carried selection only while it's still a live
// option; otherwise ignore it (→ ''). A stale ?jpod (cluster switched, pod
// re-named on deploy, shared URL) or ?jds (datasource absent after a
// range/pod re-fetch) must NOT filter the whole section to empty — it falls
// back to "all". We derive this rather than reactively rewriting the URL, to
// avoid the one-way-read trap (v0.8.253/256). (review #2/#3/#5)
export function reconcile(sel: string, options: string[]): string {
  return options.includes(sel) ? sel : '';
}

// isDatasourcePanel — does this panel's series set actually carry a
// datasource dimension? Only datasource panels may be hidden by a datasource
// filter; a non-datasource jboss panel has a single empty-named series and
// must stay visible when an (unrelated) datasource is picked. (review #1)
export function isDatasourcePanel(names: string[]): boolean {
  return names.some(n => dsToken(n) !== '');
}

// applyDsIsolate — the series a jboss panel should render given the effective
// datasource filter. Non-datasource panels pass through unchanged even when a
// datasource is selected; datasource panels isolate to the picked one; an
// empty filter shows everything. (review #1)
export function applyDsIsolate<T extends { name: string }>(series: T[], effJds: string): T[] {
  if (!effJds || !isDatasourcePanel(series.map(s => s.name))) return series;
  return series.filter(s => dsToken(s.name) === effJds);
}
