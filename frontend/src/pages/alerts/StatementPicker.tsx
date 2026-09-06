// StatementPicker.tsx — v0.10.331: /alerts "DB statement" hedefi — SQL arayıp
// seç. Sunucu tarafı arama (debounce 300 ms, ≥3 karakter, son 24 saat);
// katalog getirilmez (picker kuralı). Seçim RuleTarget olarak çağırana.
import { useEffect, useState } from 'react';
import { api } from '@/lib/api';
import type { RuleTarget, StatementSearchRow } from '@/lib/types';
import { Button } from '@/components/ui/Button';
import { Spinner } from '@/components/Spinner';

export function statementTargetOf(row: StatementSearchRow): RuleTarget {
  return { kind: 'db_statement', dbSystem: row.dbSystem, dbName: row.dbName, stmtHash: row.stmtHash, sample: row.sample.slice(0, 300) };
}

// v0.10.515 — `service`: kuralda servis seçiliyse arama o servisin
// çağırdığı ifadelerle sınırlı (operatör: "ilgili servisin query'sini
// bulurken" 241). Seçim sonrası kural yine tüm çağıranları kapsar.
export function StatementPicker({ value, onChange, service = '' }: { value?: RuleTarget; onChange: (t: RuleTarget | undefined) => void; service?: string }) {
  const [q, setQ] = useState('');
  const [rows, setRows] = useState<StatementSearchRow[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    const term = q.trim();
    if (term.length < 3) { setRows(null); return; }
    const ctl = new AbortController();
    const t = setTimeout(() => {
      setBusy(true); setErr(null);
      api.searchStatements(term, 20, ctl.signal, service)
        .then(r => setRows(r.rows))
        .catch(e => { if (!ctl.signal.aborted) setErr(e instanceof Error ? e.message : 'search failed'); })
        .finally(() => setBusy(false));
    }, 300);
    return () => { clearTimeout(t); ctl.abort(); };
  }, [q, service]);
  if (value) {
    return (
      <div className="stmt-pick" style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
        <span className="badge b-gray mono" style={{ fontSize: 10 }}>{value.dbSystem || 'db'}{value.dbName && value.dbName !== 'default' ? ` · ${value.dbName}` : ''}</span>
        <code className="mono cell-ellipsis" style={{ flex: 1, minWidth: 0, fontSize: 11 }} title={value.sample || value.stmtHash}>{value.sample || `#${value.stmtHash}`}</code>
        <Button variant="secondary" size="xs" onClick={() => onChange(undefined)} title="Hedefi kaldır (sıradan servis kuralına dön)">×</Button>
      </div>
    );
  }
  return (
    <div className="stmt-pick">
      <input value={q} onChange={e => setQ(e.target.value)} placeholder="SQL ara (≥3 karakter): tablo adı, kolon, şekil…" style={{ width: '100%' }} />
      {busy && <Spinner />}
      {err && <div style={{ fontSize: 11, color: 'var(--err)', marginTop: 4 }}>{err}</div>}
      {rows && rows.length === 0 && !busy && <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>Son 24 saatte eşleşen ifade yok{service ? ` (${service} kapsamında)` : ''}.</div>}
      {rows && rows.length > 0 && (
        <div style={{ marginTop: 6, maxHeight: 220, overflow: 'auto', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)' }}>
          {rows.map(r => (
            <button key={`${r.dbSystem}|${r.dbName}|${r.stmtHash}`} type="button" className="stmt-pick-row" onClick={() => onChange(statementTargetOf(r))}
              title={`${r.sample}\n${r.execs.toLocaleString()} yürütme/24s · p95 ${Math.round(r.p95Ms)} ms · ${r.services.join(', ')}`}>
              <span className="badge b-gray mono" style={{ fontSize: 10 }}>{r.dbSystem}{r.dbName && r.dbName !== 'default' ? ` · ${r.dbName}` : ''}</span>
              <code className="mono cell-ellipsis" style={{ flex: 1, minWidth: 0, fontSize: 11 }}>{r.sample}</code>
              <span className="mono" style={{ fontSize: 10, color: 'var(--text3)', whiteSpace: 'nowrap' }}>{r.execs.toLocaleString()}× · p95 {Math.round(r.p95Ms)} ms</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
