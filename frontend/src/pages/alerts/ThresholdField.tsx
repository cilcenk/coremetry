import { useEffect, useState } from 'react';
import { api } from '@/lib/api';
import { Button } from '@/components/ui/Button';

// ThresholdField — the numeric threshold input plus a ✨
// "suggest" button that reads the metric's last 7d
// percentile distribution from the backend, shows it in a
// compact pill row, and lets the operator one-click apply
// either tier into both the threshold AND severity fields.
//
// Pure stats endpoint (no LLM round-trip) so the round trip
// is sub-50ms even on cold cache. Sample count surfaces when
// the data is thin enough to flag the suggestion as low-
// confidence.
// Split out of the Alerts.tsx monolith (v0.8.252 refactor) verbatim.
export function ThresholdField({ value, service, metric, comparator, onChange, onApplySeverity }: {
  value: number;
  service: string;
  metric: string;
  comparator: string;
  onChange: (v: number) => void;
  onApplySeverity: (s: string) => void;
}) {
  const [busy, setBusy] = useState(false);
  const [data, setData] = useState<Awaited<ReturnType<typeof api.alertBaseline>> | null>(null);
  const [error, setError] = useState<string | null>(null);

  const suggest = async () => {
    setBusy(true); setError(null);
    try {
      const r = await api.alertBaseline({ service: service || undefined, metric, comparator });
      setData(r);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Suggestion failed');
    } finally {
      setBusy(false);
    }
  };

  // Force a re-suggest cycle when service / metric / comparator
  // change — the cached panel becomes stale relative to the
  // new context. Cheap because the data is just dropped, not
  // re-fetched until the operator clicks again.
  useEffect(() => { setData(null); }, [service, metric, comparator]);

  const fmt = (v: number) => {
    if (metric.endsWith('_ms')) return `${v} ms`;
    if (metric === 'error_rate') return `${v}%`;
    if (metric === 'request_rate') return `${v}/s`;
    return String(v);
  };

  const apply = (v: number, severity?: string) => {
    onChange(v);
    if (severity) onApplySeverity(severity);
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
        <input type="number" value={value}
          onChange={e => onChange(Number(e.target.value))}
          style={{ flex: 1, minWidth: 80 }} />
        <Button variant="secondary" size="sm" onClick={suggest} disabled={busy}
          title="Suggest threshold from the last 7 days of this metric"
          style={{ color: 'var(--accent2)', whiteSpace: 'nowrap' }}>
          {busy ? '…' : '✨ Suggest'}
        </Button>
      </div>
      {error && (
        <div style={{ fontSize: 11, color: 'var(--err)' }}>{error}</div>
      )}
      {data && (
        <div style={{
          fontSize: 11, padding: '6px 8px', borderRadius: 6,
          background: 'rgba(56,139,253,.08)',
          border: '1px solid rgba(56,139,253,.25)',
          color: 'var(--text2)',
          display: 'flex', flexDirection: 'column', gap: 4,
        }}>
          <div style={{ color: 'var(--text3)' }}>
            Last 7d{service ? ` · ${service}` : ' · all services'}
            {data.sampleCount > 0 && ` · n=${data.sampleCount.toLocaleString()}`}
            {data.sampleCount < 100 && data.sampleCount > 0 && (
              <span style={{ marginLeft: 6, color: 'var(--warn)' }}>· thin data</span>
            )}
          </div>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
            <Button variant="secondary" size="sm"
              onClick={() => apply(data.p50)} title="Apply p50 to threshold">
              p50: {fmt(data.p50)}
            </Button>
            <Button variant="secondary" size="sm"
              onClick={() => apply(data.p95)} title="Apply p95">
              p95: {fmt(data.p95)}
            </Button>
            <Button variant="secondary" size="sm"
              onClick={() => apply(data.p99)} title="Apply p99">
              p99: {fmt(data.p99)}
            </Button>
            <Button variant="secondary" size="sm"
              style={{ color: 'var(--warn)' }}
              onClick={() => apply(data.suggestedWarning, 'warning')}
              title="Apply suggested warning threshold + severity">
              warn: {fmt(data.suggestedWarning)}
            </Button>
            <Button variant="secondary" size="sm"
              style={{ color: 'var(--err)' }}
              onClick={() => apply(data.suggestedCritical, 'critical')}
              title="Apply suggested critical threshold + severity">
              crit: {fmt(data.suggestedCritical)}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
