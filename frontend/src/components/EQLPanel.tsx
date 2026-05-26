import { useState } from 'react';
import { api } from '@/lib/api';
import { toast } from '@/lib/toast';
import { tsLong } from '@/lib/utils';

// EQLPanel (v0.5.468) — collapsible Event Query Language
// section on /logs. Operator writes a multi-step sequence
// like `sequence by service.name with maxspan=5m [event.action:"deploy"] [level:"error"]`
// and gets back the matched sequences in event order.
//
// Visibility: rendered only when the logs backend is ES.
// CH backends return an explicit error from the handler;
// caller (Logs.tsx) hides the panel up-front based on the
// backend name from /api/health.

interface EQLEvent {
  timestamp: number;   // unix ns
  body: string;
  service: string;
  severity: string;
}
interface EQLSequence {
  joinKeys: string[];
  events: EQLEvent[];
}

interface EQLPanelProps {
  // Time bounds in unix-ms. Falls back to "last hour" inside
  // the panel if undefined.
  fromMs?: number;
  toMs?: number;
}

// Seed queries — kept intentionally minimal because field names
// (severity_text vs level vs log.level, message vs body) vary
// across operator deployments. v0.5.470 swapped harder-coded
// ECS shape (event.action / level) for fields Coremetry's own
// schema documents in elasticsearch.go's pickString fallback
// chain. If the operator's index uses different names, swap
// after pasting an example into the editor.
const EXAMPLE_QUERIES = [
  {
    label: 'Two errors within 1m (same service)',
    q: `sequence by service.name with maxspan=1m
  [any where severity_text == "error"]
  [any where severity_text == "error"]`,
  },
  {
    label: 'Warning then error within 5m (same trace)',
    q: `sequence by trace.id with maxspan=5m
  [any where severity_text == "warning"]
  [any where severity_text == "error"]`,
  },
];

export function EQLPanel({ fromMs, toMs }: EQLPanelProps) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState(EXAMPLE_QUERIES[0].q);
  const [size, setSize] = useState(10);
  const [running, setRunning] = useState(false);
  const [results, setResults] = useState<EQLSequence[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const run = async () => {
    if (running) return;
    setRunning(true);
    setErr(null);
    try {
      const res = await api.runLogsEQL({ query, fromMs, toMs, size });
      if (res.error) {
        setErr(res.error);
        setResults([]);
        toast.error('EQL: ' + res.error);
      } else {
        setResults(res.sequences ?? []);
        toast.success(`EQL: ${res.sequences?.length ?? 0} sequence${res.sequences?.length === 1 ? '' : 's'}`);
      }
    } catch (e) {
      const m = e instanceof Error ? e.message : String(e);
      setErr(m);
      setResults([]);
      toast.error('EQL: ' + m);
    } finally {
      setRunning(false);
    }
  };

  return (
    <div style={{
      marginTop: 12, padding: 0, fontSize: 12,
      border: '1px solid var(--border)', borderRadius: 4,
      background: 'var(--bg1)',
    }}>
      <button type="button"
        onClick={() => setOpen(o => !o)}
        style={{
          all: 'unset', cursor: 'pointer',
          width: '100%', padding: '8px 12px',
          display: 'flex', alignItems: 'center', gap: 8,
          color: 'var(--text2)', fontWeight: 600,
        }}>
        <span style={{ fontSize: 10 }}>{open ? '▾' : '▸'}</span>
        <span>EQL Sequence Detection</span>
        <span style={{ marginLeft: 'auto', fontSize: 10, color: 'var(--text3)' }}>
          v0.5.468 · ES only
        </span>
      </button>
      {open && (
        <div style={{ padding: '0 12px 12px 12px' }}>
          <div style={{ display: 'flex', gap: 8, marginBottom: 6, flexWrap: 'wrap', alignItems: 'center' }}>
            <span style={{ color: 'var(--text3)', fontSize: 11 }}>Examples:</span>
            {EXAMPLE_QUERIES.map(e => (
              <button key={e.label} type="button"
                onClick={() => setQuery(e.q)}
                style={{
                  padding: '2px 8px', fontSize: 11, borderRadius: 3,
                  background: 'var(--bg2)', border: '1px solid var(--border)',
                  color: 'var(--accent2)', cursor: 'pointer',
                }}>
                {e.label}
              </button>
            ))}
            <span style={{ fontSize: 10, color: 'var(--text3)' }}>
              Field names depend on your index mapping — swap <code>severity_text</code> / <code>service.name</code> / <code>trace.id</code> if your shippers use different keys.
            </span>
          </div>
          <textarea
            value={query}
            onChange={e => setQuery(e.target.value)}
            spellCheck={false}
            placeholder="sequence by service.name with maxspan=5m [event.action: deploy] [level: error]"
            style={{
              width: '100%', height: 90,
              fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
              fontSize: 12, padding: 8,
              background: 'var(--bg)', color: 'var(--text)',
              border: '1px solid var(--border)', borderRadius: 3,
              resize: 'vertical',
            }} />
          <div style={{ display: 'flex', gap: 8, marginTop: 6, alignItems: 'center' }}>
            <button type="button" onClick={run} disabled={running || !query.trim()}
              style={{ padding: '5px 14px', fontSize: 12 }}>
              {running ? 'Running…' : '▶ Run sequence'}
            </button>
            <label style={{ fontSize: 11, color: 'var(--text2)' }}>
              Size:{' '}
              <input type="number" value={size}
                onChange={e => setSize(Math.max(1, Math.min(100, parseInt(e.target.value || '10', 10))))}
                style={{ width: 50, fontSize: 12, padding: '2px 4px' }} />
            </label>
            <span style={{ fontSize: 10, color: 'var(--text3)', marginLeft: 'auto' }}>
              Window: {fromMs && toMs ? `${tsLong(fromMs * 1_000_000)} → ${tsLong(toMs * 1_000_000)}` : 'panel default (server: last hour)'}
            </span>
          </div>
          {err && (
            <div style={{
              marginTop: 8, padding: '6px 10px', borderRadius: 3,
              background: 'rgba(220,38,38,0.10)', color: 'var(--err)',
              fontSize: 11, fontFamily: 'ui-monospace, monospace',
            }}>{err}</div>
          )}
          {results !== null && !err && (
            <div style={{ marginTop: 10 }}>
              {results.length === 0 ? (
                <div style={{ color: 'var(--text3)', fontSize: 11, padding: '8px 0' }}>
                  No sequences matched.
                </div>
              ) : (
                results.map((seq, si) => (
                  <div key={si} style={{
                    marginBottom: 8,
                    border: '1px solid var(--border)', borderRadius: 3,
                    background: 'var(--bg2)',
                  }}>
                    <div style={{
                      padding: '4px 10px', fontSize: 11,
                      borderBottom: '1px solid var(--border)',
                      color: 'var(--text2)',
                      display: 'flex', gap: 8, alignItems: 'center',
                    }}>
                      <span style={{ fontWeight: 600 }}>Sequence #{si + 1}</span>
                      {seq.joinKeys.length > 0 && (
                        <span style={{ color: 'var(--text3)' }}>
                          by {seq.joinKeys.map(k => `"${k}"`).join(', ')}
                        </span>
                      )}
                      <span style={{ marginLeft: 'auto', color: 'var(--text3)' }}>
                        {seq.events.length} event{seq.events.length === 1 ? '' : 's'}
                      </span>
                    </div>
                    {seq.events.map((e, ei) => (
                      <div key={ei} style={{
                        padding: '4px 10px',
                        borderTop: ei > 0 ? '1px solid var(--border)' : 'none',
                        display: 'flex', gap: 8, fontSize: 11,
                        fontFamily: 'ui-monospace, monospace',
                      }}>
                        <span style={{ color: 'var(--text3)', minWidth: 140 }}>
                          {tsLong(e.timestamp)}
                        </span>
                        {e.service && (
                          <span style={{ color: 'var(--accent2)', minWidth: 100 }}>{e.service}</span>
                        )}
                        {e.severity && (
                          <span style={{
                            padding: '0 4px', borderRadius: 2,
                            background: e.severity.toLowerCase().includes('error')
                              ? 'rgba(220,38,38,0.18)'
                              : 'var(--bg3)',
                            color: 'var(--text)', fontSize: 10,
                          }}>{e.severity}</span>
                        )}
                        <span style={{
                          flex: 1, color: 'var(--text)',
                          whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis',
                        }}>{e.body}</span>
                      </div>
                    ))}
                  </div>
                ))
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
