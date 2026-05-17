import { useEffect, useState } from 'react';
import { api } from '@/lib/api';
import { useAuth } from '@/components/AuthProvider';
import { timeRangeToNs, fmtNum } from '@/lib/utils';
import type { SamplingSettings, TimeRange } from '@/lib/types';

// SamplingChip — surfaces the head-sampling decision for a single
// service inline on the service detail page (v0.5.166). The
// admin-only Settings → Sampling tab was the only entry point;
// operators triaging cost issues now adjust the per-service ratio
// without leaving the service view.
//
// Three states the chip can render:
//   • "Sampling 100%" — service inherits a default of 1.0 OR has
//     an explicit override of 1.0 (everything kept).
//   • "Sampling X% (default)" — uses default, default < 1.
//   • "Sampling X% (override)" — service has its own entry in the
//     services map.
//
// Clicking opens a small inline picker with quick presets +
// custom; only admin/editor can save (viewers see read-only).
const PRESETS = [1.0, 0.5, 0.1, 0.05, 0.01];

export function SamplingChip({ service, spanCount, range }: {
  service: string;
  // Recent span volume + the window it was observed over —
  // both optional. When provided (v0.5.168) the popover shows
  // per-preset "≈ X spans/day kept" projections so the operator
  // sees the volume impact before flipping the knob, not after
  // the storage bill arrives.
  spanCount?: number;
  range?: TimeRange;
}) {
  const { user } = useAuth();
  const canEdit = user?.role === 'admin' || user?.role === 'editor';
  const [s, setS] = useState<SamplingSettings | null | undefined>(undefined);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);

  // Spans-per-day estimate normalised from the observed window.
  // We use this to project per-preset retention volumes. The
  // estimate is naïve (linear extrapolation) but matches the
  // operator's mental model better than a "%" alone.
  let spansPerDay: number | null = null;
  if (spanCount && range) {
    const { from, to } = timeRangeToNs(range);
    const seconds = Math.max(1, (to - from) / 1e9);
    spansPerDay = spanCount * (86400 / seconds);
  }

  useEffect(() => {
    api.getSampling().then(setS).catch(() => setS(null));
  }, []);

  if (s === undefined) return null;
  if (s === null) return null;

  const override = s.services[service];
  const effective = override !== undefined ? override : s.default;
  const hasOverride = override !== undefined;
  const pctLabel = `${(effective * 100).toFixed(effective < 0.01 ? 2 : effective < 0.1 ? 1 : 0)}%`;
  const color = effective >= 1.0
    ? 'var(--text2)'
    : effective >= 0.5
    ? 'var(--text2)'
    : effective >= 0.1
    ? 'var(--warn)'
    : 'var(--err)';

  const save = async (newRatio: number | null) => {
    setBusy(true);
    try {
      const services = { ...s.services };
      if (newRatio === null) {
        // "Use default" — remove the override entirely.
        delete services[service];
      } else {
        services[service] = newRatio;
      }
      const next = await api.putSampling({
        default:          s.default,
        services,
        alwaysKeepErrors: s.alwaysKeepErrors,
        alwaysKeepRoots:  s.alwaysKeepRoots,
        tail:             s.tail,
      });
      setS(next);
      setOpen(false);
    } catch (e) {
      alert('Failed to update sampling: ' + (e instanceof Error ? e.message : String(e)));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div style={{ position: 'relative', display: 'inline-block' }}>
      <button
        type="button"
        onClick={() => canEdit && setOpen(o => !o)}
        disabled={!canEdit}
        title={canEdit
          ? `Click to adjust head-sampling rate. Errors and root spans always bypass the ratio.${hasOverride ? '' : ' Currently inheriting default.'}`
          : 'Admin or editor required to change sampling'}
        style={{
          display: 'inline-flex', alignItems: 'center', gap: 6,
          padding: '5px 10px', borderRadius: 6,
          background: 'var(--bg3)', border: '1px solid var(--border)',
          color, fontSize: 11,
          fontFamily: 'ui-monospace, monospace',
          cursor: canEdit ? 'pointer' : 'default',
        }}>
        <span>◐ Sampling {pctLabel}</span>
        {hasOverride && (
          <span style={{
            fontSize: 9, padding: '1px 5px', borderRadius: 8,
            background: 'var(--accent2)', color: 'var(--bg)',
            fontFamily: 'ui-monospace, monospace',
          }}>override</span>
        )}
      </button>

      {open && (
        <>
          {/* click-out backdrop — transparent but captures clicks */}
          <div onClick={() => setOpen(false)}
            style={{ position: 'fixed', inset: 0, zIndex: 40 }} />
          <div style={{
            position: 'absolute', top: 'calc(100% + 4px)', left: 0,
            zIndex: 41,
            background: 'var(--bg)', border: '1px solid var(--border)',
            borderRadius: 6, boxShadow: '0 8px 24px rgba(0,0,0,0.4)',
            padding: 8, minWidth: 220,
          }}>
            <div style={{
              fontSize: 10, color: 'var(--text3)', padding: '2px 6px',
              textTransform: 'uppercase', letterSpacing: 0.4,
              marginBottom: 4,
            }}>Quick presets</div>
            {PRESETS.map(p => {
              // Projected spans/day kept at this preset, factoring
              // in the always-keep floor (errors + roots stay).
              // Without recent volume data we just show the ratio.
              const projected = spansPerDay !== null
                ? Math.round(spansPerDay * p)
                : null;
              return (
              <button key={p} type="button"
                disabled={busy}
                onClick={() => save(p)}
                style={{
                  display: 'flex', width: '100%', alignItems: 'center',
                  justifyContent: 'space-between',
                  padding: '5px 8px', borderRadius: 4,
                  background: override === p ? 'var(--bg2)' : 'transparent',
                  border: 'none', color: 'inherit', cursor: 'pointer',
                  fontSize: 12, textAlign: 'left',
                }}>
                <span>{p === 1 ? 'Keep everything' : `Keep ${(p * 100).toFixed(p < 0.1 ? 1 : 0)}%`}</span>
                <span style={{
                  fontSize: 10, color: 'var(--text3)',
                  fontFamily: 'ui-monospace, monospace',
                }}>
                  {projected !== null
                    ? `≈ ${fmtNum(projected)}/d`
                    : p.toString()}
                </span>
              </button>
              );
            })}
            <div style={{
              borderTop: '1px solid var(--border)',
              marginTop: 6, paddingTop: 6,
            }}>
              {hasOverride && (
                <button type="button" disabled={busy}
                  onClick={() => save(null)}
                  style={{
                    display: 'block', width: '100%',
                    padding: '5px 8px', borderRadius: 4,
                    background: 'transparent', border: 'none',
                    color: 'var(--text2)', cursor: 'pointer',
                    fontSize: 12, textAlign: 'left',
                  }}>
                  ↻ Use default ({(s.default * 100).toFixed(0)}%)
                </button>
              )}
              <CustomRow current={override ?? s.default} onApply={save} busy={busy} />
            </div>
            <div style={{
              fontSize: 10, color: 'var(--text3)',
              padding: '6px 8px 2px', lineHeight: 1.4,
            }}>
              Errors and root spans always bypass the ratio.
              {spansPerDay !== null && (
                <div style={{ marginTop: 4, color: 'var(--text2)' }}>
                  Current volume ≈ <b>{fmtNum(Math.round(spansPerDay))}</b> spans/day
                  {' '}(extrapolated from selected window).
                </div>
              )}
              {s.alwaysKeepErrors === false && (
                <div style={{ color: 'var(--warn)', marginTop: 2 }}>
                  ⚠ "always keep errors" is currently OFF — errors will be sampled at this rate too.
                </div>
              )}
            </div>
          </div>
        </>
      )}
    </div>
  );
}

function CustomRow({ current, onApply, busy }: {
  current: number;
  onApply: (r: number) => void;
  busy: boolean;
}) {
  const [val, setVal] = useState(String(current));
  return (
    <div style={{ display: 'flex', gap: 4, padding: '5px 8px', alignItems: 'center' }}>
      <span style={{ fontSize: 11, color: 'var(--text3)' }}>Custom</span>
      <input type="number" min={0} max={1} step={0.01}
        value={val}
        onChange={e => setVal(e.target.value)}
        style={{ flex: 1, fontSize: 11, padding: '3px 6px' }} />
      <button type="button" disabled={busy}
        onClick={() => {
          const r = parseFloat(val);
          if (!isNaN(r) && r >= 0 && r <= 1) onApply(r);
        }}
        style={{ fontSize: 11, padding: '3px 8px' }}>
        Apply
      </button>
    </div>
  );
}
