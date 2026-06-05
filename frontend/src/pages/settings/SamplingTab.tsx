import { useEffect, useState } from 'react';
import { Spinner, Empty } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import type { SamplingSettings } from '@/lib/types';
import { humanize } from './shared';

// ── Sampling tab ────────────────────────────────────────────────────────────
//
// Hot-path policy editor. Default ratio applies to every service
// that doesn't have its own override; per-service rows let the
// admin pin a heavy service to 0.05 (drop 95%) while a low-volume
// one runs at 1.0. AlwaysKeep* are big-stick defaults — turning
// them off trades observability for raw storage savings, almost
// never worth it.
export function SamplingTab() {
  const [s, setS] = useState<SamplingSettings | null | undefined>(undefined);
  const [busy, setBusy] = useState(false);
  const [newSvc, setNewSvc] = useState('');
  const [newRatio, setNewRatio] = useState('1');

  useEffect(() => {
    api.getSampling().then(d => setS(d ?? null)).catch(() => setS(null));
  }, []);

  if (s === undefined) return <Spinner />;
  if (s === null) {
    return <Empty icon="!" title="Failed to load sampling settings">
      Check that the backend is up and you have admin access.
    </Empty>;
  }

  const save = async () => {
    setBusy(true);
    try {
      const next = await api.putSampling({
        default:          s.default,
        services:         s.services,
        alwaysKeepErrors: s.alwaysKeepErrors,
        alwaysKeepRoots:  s.alwaysKeepRoots,
        tail:             s.tail,
      });
      setS(next);
    } catch (err) { alert(humanize(err)); }
    finally { setBusy(false); }
  };

  const updateTail = (partial: Partial<NonNullable<typeof s.tail>>) => {
    const cur = s.tail ?? { enabled: false, windowSec: 30, slowMs: 1000, maxTraces: 200_000 };
    setS({ ...s, tail: { ...cur, ...partial } });
  };

  const addOverride = () => {
    const r = parseFloat(newRatio);
    if (!newSvc.trim() || isNaN(r) || r < 0 || r > 1) return;
    setS({ ...s, services: { ...s.services, [newSvc.trim()]: r } });
    setNewSvc(''); setNewRatio('1');
  };
  const removeOverride = (svc: string) => {
    const next = { ...s.services };
    delete next[svc];
    setS({ ...s, services: next });
  };

  return (
    <div style={{ maxWidth: 720 }}>
      <h3 style={{ marginTop: 0 }}>Trace sampling</h3>
      <p style={{ color: 'var(--text2)', fontSize: 12 }}>
        Head-sampling rules applied at OTLP ingest. Errors and root spans are
        always kept (toggle below). Probabilistic ratio applies to the rest;
        same trace_id always gets the same decision so partial traces don't
        leak through.
      </p>

      <div style={{
        background: 'var(--bg2)', border: '1px solid var(--border)',
        borderRadius: 6, padding: 12, marginBottom: 12,
        display: 'grid', gridTemplateColumns: '180px 1fr', gap: '10px 14px',
        alignItems: 'center', fontSize: 13,
      }}>
        <label>Default ratio</label>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <input type="number" min={0} max={1} step={0.01}
                 value={s.default}
                 onChange={e => setS({ ...s, default: parseFloat(e.target.value) || 0 })}
                 style={{ width: 100 }} />
          <span style={{ color: 'var(--text3)', fontSize: 11 }}>
            (0 = drop all probabilistic spans · 1 = keep everything)
          </span>
        </div>

        <label>Always keep errors</label>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
          <input type="checkbox" checked={s.alwaysKeepErrors}
                 onChange={e => setS({ ...s, alwaysKeepErrors: e.target.checked })} />
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>
            status_code = ERROR spans bypass the ratio
          </span>
        </label>

        <label>Always keep roots</label>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
          <input type="checkbox" checked={s.alwaysKeepRoots}
                 onChange={e => setS({ ...s, alwaysKeepRoots: e.target.checked })} />
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>
            parent_span_id == "" spans bypass the ratio (preserves RPS counts)
          </span>
        </label>
      </div>

      <h4 style={{ marginBottom: 8 }}>Per-service overrides</h4>
      <div style={{
        background: 'var(--bg2)', border: '1px solid var(--border)',
        borderRadius: 6, padding: 12, marginBottom: 12,
      }}>
        {Object.keys(s.services).length === 0 && (
          <div style={{ color: 'var(--text3)', fontSize: 12, marginBottom: 8 }}>
            No overrides — every service uses the default ratio above.
          </div>
        )}
        {Object.entries(s.services).map(([svc, r]) => (
          <div key={svc} style={{
            display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6,
          }}>
            <code style={{ flex: 1, fontSize: 12 }}>{svc}</code>
            <input type="number" min={0} max={1} step={0.01}
                   value={r}
                   onChange={e => setS({
                     ...s,
                     services: { ...s.services, [svc]: parseFloat(e.target.value) || 0 },
                   })}
                   style={{ width: 80 }} />
            <Button variant="secondary" size="sm"
                    onClick={() => removeOverride(svc)}>Remove</Button>
          </div>
        ))}

        <div style={{
          display: 'flex', alignItems: 'center', gap: 8,
          borderTop: '1px solid var(--border)', paddingTop: 10, marginTop: 8,
        }}>
          <input value={newSvc} onChange={e => setNewSvc(e.target.value)}
                 placeholder="service-name" style={{ flex: 1 }} />
          <input type="number" min={0} max={1} step={0.01}
                 value={newRatio} onChange={e => setNewRatio(e.target.value)}
                 style={{ width: 80 }} />
          <Button variant="secondary" size="sm"
                  onClick={addOverride}>Add</Button>
        </div>
      </div>

      <h4 style={{ marginBottom: 8 }}>Tail sampling (buffered)</h4>
      <div style={{
        background: 'var(--bg2)', border: '1px solid var(--border)',
        borderRadius: 6, padding: 12, marginBottom: 12, fontSize: 13,
      }}>
        <p style={{ color: 'var(--text2)', fontSize: 12, marginTop: 0 }}>
          Buffers each trace for the decision window, then keeps it if any
          span had an error, the root duration exceeded the slow-trace
          threshold, or it falls under the probabilistic ratio. Late-
          arriving spans of decided traces follow the prior verdict.
        </p>
        <div style={{
          display: 'grid', gridTemplateColumns: '180px 1fr', gap: '10px 14px',
          alignItems: 'center',
        }}>
          <label>Enabled</label>
          <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            <input type="checkbox" checked={s.tail?.enabled ?? false}
                   onChange={e => updateTail({ enabled: e.target.checked })} />
            <span style={{ fontSize: 11, color: 'var(--text3)' }}>
              when on, head ratios are bypassed for traces — tail decides instead
            </span>
          </label>

          <label>Decision window</label>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <input type="number" min={5} max={300}
                   value={s.tail?.windowSec ?? 30}
                   onChange={e => updateTail({ windowSec: parseInt(e.target.value) || 30 })}
                   style={{ width: 80 }} />
            <span style={{ color: 'var(--text3)', fontSize: 11 }}>seconds (default 30)</span>
          </div>

          <label>Slow trace threshold</label>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <input type="number" min={50} max={60000}
                   value={s.tail?.slowMs ?? 1000}
                   onChange={e => updateTail({ slowMs: parseInt(e.target.value) || 1000 })}
                   style={{ width: 80 }} />
            <span style={{ color: 'var(--text3)', fontSize: 11 }}>
              ms (root duration above this = always keep)
            </span>
          </div>

          <label>Max in-flight traces</label>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <input type="number" min={1000} max={1_000_000} step={1000}
                   value={s.tail?.maxTraces ?? 200_000}
                   onChange={e => updateTail({ maxTraces: parseInt(e.target.value) || 200_000 })}
                   style={{ width: 100 }} />
            <span style={{ color: 'var(--text3)', fontSize: 11 }}>
              memory bound (~5 spans × 500 B per trace)
            </span>
          </div>
        </div>
        {s.tailStats && s.tailStats.enabled && (
          <div style={{
            marginTop: 10, padding: 8,
            background: 'var(--bg1)', borderRadius: 4,
            fontFamily: 'ui-monospace, monospace', fontSize: 11, color: 'var(--text3)',
          }}>
            open: {s.tailStats.openTraces.toLocaleString()} traces ·
            flushed: {s.tailStats.flushedSpans.toLocaleString()} spans ·
            dropped: {s.tailStats.droppedSpans.toLocaleString()} spans ·
            evicted: {s.tailStats.evictedTraces.toLocaleString()} traces
          </div>
        )}
      </div>

      <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
        <Button variant="primary" onClick={save} disabled={busy}>
          {busy ? 'Saving…' : 'Save & apply'}
        </Button>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          Head-stage drops since boot: <b>{s.droppedSinceBoot.toLocaleString()}</b>
        </span>
      </div>
    </div>
  );
}
