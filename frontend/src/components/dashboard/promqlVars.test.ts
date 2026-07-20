import { describe, it, expect } from 'vitest';
import { applyVarsToPromql } from './PanelRenderer';

// v0.9.118 — regression for the review MAJOR: routing a PromQL query through the
// line-based substituteVars deleted the WHOLE query when a referenced variable
// was empty (the default "(all)" state), breaking every var-driven PromQL panel.
// applyVarsToPromql must substitute in place + strip empty-var matchers, never
// drop the expression.

const cfg = (query: string) => ({ query });

describe('applyVarsToPromql', () => {
  it('substitutes a non-empty variable in place', () => {
    const r = applyVarsToPromql(cfg('rate(http_requests{service.name="${service}"}[5m])'), { service: 'checkout' });
    expect(r.query).toBe('rate(http_requests{service.name="checkout"}[5m])');
  });

  it('empty variable STRIPS its matcher (selects all), not deletes the query', () => {
    const r = applyVarsToPromql(cfg('rate(http_requests{service.name="${service}"}[5m])'), { service: '' });
    // matcher gone → {} matches all series; the query is intact (NOT "").
    expect(r.query).toBe('rate(http_requests{}[5m])');
  });

  it('unset variable (absent key) also strips the matcher', () => {
    const r = applyVarsToPromql(cfg('http_requests{service.name="${service}"}'), {});
    expect(r.query).toBe('http_requests{}');
  });

  it('strips an empty matcher among others, tidying commas', () => {
    const r = applyVarsToPromql(cfg('http_requests{code="500",service.name="${service}"}'), { service: '' });
    expect(r.query).toBe('http_requests{code="500"}');
  });

  it('strips a FIRST empty matcher, tidying the leading comma', () => {
    const r = applyVarsToPromql(cfg('http_requests{service.name="${service}",code="500"}'), { service: '' });
    expect(r.query).toBe('http_requests{code="500"}');
  });

  it('keeps a non-empty matcher and strips a sibling empty one', () => {
    const r = applyVarsToPromql(cfg('x{a="${a}",b="${b}"}'), { a: 'one', b: '' });
    expect(r.query).toBe('x{a="one"}');
  });

  it('handles a regex matcher value', () => {
    const r = applyVarsToPromql(cfg('x{code=~"${code}"}'), { code: '5..' });
    expect(r.query).toBe('x{code=~"5.."}');
  });

  it('no vars map → query unchanged', () => {
    const q = 'sum by (le) (rate(x[5m]))';
    expect(applyVarsToPromql(cfg(q), undefined).query).toBe(q);
  });

  it('never returns an empty query for a non-empty input with an empty var', () => {
    const r = applyVarsToPromql(cfg('up{job="${service}"}'), { service: '' });
    expect(r.query.length).toBeGreaterThan(0);
    expect(r.query).toBe('up{}');
  });
});
