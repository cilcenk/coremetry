import { useEffect, useState, type FormEvent } from 'react';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import { humanize } from './shared';

// RetentionTab — per-signal TTL controls. Each signal (spans / logs /
// metrics / profiles) takes a number + unit (hours / days). Save calls
// PUT /api/settings/retention which runs ALTER TABLE ... MODIFY TTL on
// the underlying ClickHouse tables. Effect is online — ClickHouse
// re-evaluates TTL on next merge so deletions catch up within ~10 min.
export function RetentionTab() {
  type Unit = 'h' | 'd';
  type Row = { value: string; unit: Unit };
  const empty: Row = { value: '', unit: 'd' };
  const [spans,    setSpans]    = useState<Row>(empty);
  const [logs,     setLogs]     = useState<Row>(empty);
  const [metrics,  setMetrics]  = useState<Row>(empty);
  const [profiles, setProfiles] = useState<Row>(empty);
  const [busy, setBusy] = useState(false);
  const [msg,  setMsg]  = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const decode = (s?: string): Row => {
    const m = s?.match(/^(\d+)([hd])$/);
    return m ? { value: m[1], unit: m[2] as Unit } : empty;
  };
  const encode = (r: Row): string => r.value ? `${r.value}${r.unit}` : '';

  useEffect(() => {
    api.getRetention().then(sp => {
      setSpans(decode(sp.spans));
      setLogs(decode(sp.logs));
      setMetrics(decode(sp.metrics));
      setProfiles(decode(sp.profiles));
    }).catch(() => {});
  }, []);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      await api.putRetention({
        spans:    encode(spans),
        logs:     encode(logs),
        metrics:  encode(metrics),
        profiles: encode(profiles),
      });
      setMsg({ kind: 'ok', text: 'Applied — ClickHouse will re-evaluate TTL on next merge (~10 min).' });
    } catch (err) {
      setMsg({ kind: 'err', text: humanize(err) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} style={{ maxWidth: 560 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Data retention</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Per-signal TTL on the underlying ClickHouse tables. Older data is dropped
        on the next merge cycle. Leave a field blank to keep the current value
        (initial defaults come from the config file: spans 30d, logs 30d,
        metrics 7d, profiles 7d).
      </p>

      <RetentionRow label="Spans"    row={spans}    setRow={setSpans} />
      <RetentionRow label="Logs"     row={logs}     setRow={setLogs} />
      <RetentionRow label="Metrics"  row={metrics}  setRow={setMetrics} />
      <RetentionRow label="Profiles" row={profiles} setRow={setProfiles} />

      <div style={{ marginTop: 14, display: 'flex', gap: 8, alignItems: 'center' }}>
        <Button type="submit" variant="primary" disabled={busy}>{busy ? 'Applying…' : 'Apply'}</Button>
        {msg && (
          <span style={{ fontSize: 12, color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)' }}>
            {msg.text}
          </span>
        )}
      </div>

      <p style={{ marginTop: 18, fontSize: 11, color: 'var(--text3)' }}>
        Hour-precision TTL is supported (e.g. <code>36h</code>) but ClickHouse
        partitions data per day, so very short retention windows still
        process at day-boundary granularity. Examples: <code>48h</code> = last 2 days,
        <code> 2d</code> = same thing, <code>30d</code> = last 30 days.
      </p>
    </form>
  );
}

function RetentionRow({ label, row, setRow }: {
  label: string;
  row: { value: string; unit: 'h' | 'd' };
  setRow: (r: { value: string; unit: 'h' | 'd' }) => void;
}) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 10 }}>
      <span style={{ width: 90, fontSize: 13 }}>{label}</span>
      <input type="number" min={1} value={row.value}
        onChange={e => setRow({ ...row, value: e.target.value })}
        placeholder="(unchanged)"
        style={{ width: 100 }} />
      <select value={row.unit}
        onChange={e => setRow({ ...row, unit: e.target.value as 'h' | 'd' })}>
        <option value="h">hours</option>
        <option value="d">days</option>
      </select>
    </div>
  );
}
