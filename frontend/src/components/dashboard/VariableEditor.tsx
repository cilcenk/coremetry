// VariableEditor (v0.9.759) — düzenleme modunda Grafana-tarzı değişken
// tanımı: ad · tip (service/database/custom) · custom seçenekleri ·
// varsayılan. Paneller ${ad} ile başvurur (applyVars* mevcut mekanizma);
// bar zaten URL'e yazıyor. JSON elle düzenleme devri kapanıyor.
import { Button } from '../ui/Button';
import type { DashboardVariable } from '@/lib/types';

export function VariableEditor({ value, onChange }: {
  value: DashboardVariable[];
  onChange: (next: DashboardVariable[]) => void;
}) {
  const upd = (i: number, patch: Partial<DashboardVariable>) =>
    onChange(value.map((v, j) => (j === i ? { ...v, ...patch } : v)));
  const del = (i: number) => onChange(value.filter((_, j) => j !== i));
  const add = () =>
    onChange([...value, { name: `var${value.length + 1}`, type: 'service' }]);

  return (
    <div className="card" style={{ padding: 10, marginBottom: 10 }}>
      <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 6 }}>
        Değişkenler <span style={{ color: 'var(--text3)', fontWeight: 400 }}>
          — panellerde {'${ad}'} ile kullan (dsl / service / groupBy)</span>
      </div>
      {value.length === 0 && (
        <div style={{ fontSize: 12, color: 'var(--text3)', marginBottom: 6 }}>
          Tanımlı değişken yok. "service" tipli bir değişken ekleyip panellerde
          {' ${service}'} yazarak dashboard'u servise göre sürebilirsin.
        </div>
      )}
      {value.map((v, i) => (
        <div key={i} style={{ display: 'flex', gap: 6, alignItems: 'center', marginBottom: 6, flexWrap: 'wrap' }}>
          <input value={v.name} aria-label="Değişken adı" placeholder="ad"
            onChange={e => upd(i, { name: e.target.value.replace(/[^a-zA-Z0-9_]/g, '') })}
            style={{ width: 120 }} />
          <select value={v.type} aria-label="Değişken tipi"
            onChange={e => upd(i, { type: e.target.value as DashboardVariable['type'] })}>
            <option value="service">service</option>
            <option value="database">database</option>
            <option value="custom">custom</option>
          </select>
          {v.type === 'custom' && (
            <input value={(v.options ?? []).join(',')} placeholder="seçenekler (virgüllü)"
              aria-label="Custom seçenekleri"
              onChange={e => upd(i, { options: e.target.value.split(',').map(s => s.trim()).filter(Boolean) })}
              style={{ width: 220 }} />
          )}
          <input value={v.defaultValue ?? ''} placeholder="varsayılan (boş = all)"
            aria-label="Varsayılan değer"
            onChange={e => upd(i, { defaultValue: e.target.value || undefined })}
            style={{ width: 160 }} />
          <Button variant="ghost" size="sm" onClick={() => del(i)} title="Değişkeni sil">✕</Button>
        </div>
      ))}
      <Button variant="secondary" size="sm" onClick={add}>+ Değişken ekle</Button>
    </div>
  );
}
