import { useMemo } from 'react';
import { evalExpr, exprRefs } from '@/lib/metricFormula';

// FormulaRow — the builder's single derived-series expression
// (explore-v2 Phase 2). Arithmetic over the query letters, e.g.
// "A / B * 100" for an error-rate percent. Evaluation lives in
// formulaSeries.ts; this row only edits + sanity-checks the text.

export function FormulaRow({ value, onChange, letters }: {
  value: string;
  onChange: (expr: string) => void;
  letters: string[];                 // letters that currently produce data
}) {
  // Cheap validity probe: parse with every referenced letter = 1. A null
  // result on non-empty text = parse error or unknown reference.
  const problem = useMemo(() => {
    const t = value.trim();
    if (!t) return null;
    const refs = exprRefs(t);
    const unknown = refs.filter(r => !letters.includes(r));
    if (unknown.length) return `bilinmeyen sorgu: ${unknown.join(', ')}`;
    const vars: Record<string, number> = {};
    for (const r of refs) vars[r] = 1;
    return evalExpr(t, vars) === null ? 'ifade çözümlenemedi' : null;
  }, [value, letters]);

  return (
    <>
      <span style={{
        display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
        width: 22, height: 22, borderRadius: 4, flexShrink: 0,
        background: 'var(--bg3)', border: '1px dashed var(--border)',
        fontSize: 12, fontWeight: 700, color: 'var(--text2)',
      }}>ƒ</span>
      <input value={value}
        onChange={e => onChange(e.target.value)}
        placeholder={`sorgu harfleri üzerinde formül — örn. ${letters[0] ?? 'A'} / ${letters[1] ?? 'B'} * 100`}
        spellCheck={false}
        style={{ flex: 1, minWidth: 220, fontFamily: 'ui-monospace, SFMono-Regular, monospace', fontSize: 12 }} />
      {problem && value.trim() !== '' && (
        <span style={{ fontSize: 11, color: 'var(--warn)' }}>⚠ {problem}</span>
      )}
    </>
  );
}
