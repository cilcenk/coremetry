import { PANEL_UNITS, isKnownUnit, normalizeUnit } from '@/lib/units';

// UnitSelect — v0.10.506 (dış skill denetimi D9): panel birim alanı seçici.
// Kanonik listeden seçim; kayıtlı bir özel değer ("req/dk") listede yoksa
// "diğer…" seçili gelir ve yanında metin kutusu açılır — eski dashboard'lar
// bozulmaz, yeni girişler normalize edilir (blur'da).
export function UnitSelect({ value, onChange }: { value: string | undefined; onChange: (v: string) => void }) {
  const v = value ?? '';
  const custom = v !== '' && !isKnownUnit(v);
  return (
    <span style={{ display: 'inline-flex', gap: 6, alignItems: 'center', width: '100%' }}>
      <select value={custom ? '__custom' : v}
        onChange={e => onChange(e.target.value === '__custom' ? (custom ? v : 'x') : e.target.value)}>
        {PANEL_UNITS.map(p => <option key={p.value || 'none'} value={p.value}>{p.label}</option>)}
        <option value="__custom">diğer…</option>
      </select>
      {custom && (
        <input value={v} aria-label="Özel birim eki" style={{ width: 90 }}
          onChange={e => onChange(e.target.value)}
          onBlur={e => onChange(normalizeUnit(e.target.value))} />
      )}
    </span>
  );
}
