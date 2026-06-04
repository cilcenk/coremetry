// metricFormula (v0.7.128) — the pure expression evaluator behind the metric
// query editor's formula rows (e.g. "A / B * 100" for error-rate %). Kept out
// of the component so it's unit-testable without pulling in the chart bundle.
//
// Safe recursive-descent arithmetic over query-id operands (A, B, C…) plus
// numeric literals: + - * / ( ) and unary minus. NO eval(). Returns null on
// any unknown id, parse error, or non-finite result (e.g. divide-by-zero) so
// the caller can render a gap rather than NaN.

export function evalExpr(expr: string, vars: Record<string, number>): number | null {
  const s = expr;
  let i = 0;
  const ws = () => { while (i < s.length && s[i] === ' ') i++; };

  function parseAdd(): number | null {
    let v = parseMul();
    if (v === null) return null;
    ws();
    while (s[i] === '+' || s[i] === '-') {
      const op = s[i++];
      const r = parseMul();
      if (r === null) return null;
      v = op === '+' ? v + r : v - r;
      ws();
    }
    return v;
  }
  function parseMul(): number | null {
    let v = parseFactor();
    if (v === null) return null;
    ws();
    while (s[i] === '*' || s[i] === '/') {
      const op = s[i++];
      const r = parseFactor();
      if (r === null) return null;
      v = op === '*' ? v * r : v / r;
      ws();
    }
    return v;
  }
  function parseFactor(): number | null {
    ws();
    if (s[i] === '-') { i++; const f = parseFactor(); return f === null ? null : -f; }
    if (s[i] === '(') { i++; const v = parseAdd(); ws(); if (s[i] !== ')') return null; i++; return v; }
    const num = /^\d+(\.\d+)?/.exec(s.slice(i));
    if (num) { i += num[0].length; return parseFloat(num[0]); }
    const id = /^[A-Za-z][A-Za-z0-9]*/.exec(s.slice(i));
    if (id) { i += id[0].length; return id[0] in vars ? vars[id[0]] : null; }
    return null;
  }

  const r = parseAdd();
  ws();
  return (r !== null && i >= s.length && Number.isFinite(r)) ? r : null;
}

// exprRefs — the distinct query ids a formula expression references.
export function exprRefs(expr: string): string[] {
  return [...new Set(expr.match(/[A-Za-z][A-Za-z0-9]*/g) ?? [])];
}
