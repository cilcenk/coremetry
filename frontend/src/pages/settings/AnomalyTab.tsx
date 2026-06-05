import { useEffect, useState } from 'react';
import { Spinner } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import { Field, FlashBox, humanize } from './shared';

// ── Anomaly promotion tab ───────────────────────────────────────
//
// Tunes the evaluator's anomaly auto-promotion (v0.5.59). The
// detector continuously flags "pattern X is occurring N× more
// than baseline" rows on /anomalies; when they sustain past
// the configured thresholds the evaluator graduates them to
// first-class Problems so the existing notify pipeline pages
// the on-call. Master enable flag lets operators kill the
// feature for a chatty detector without changing thresholds.
export function AnomalyPromotionTab() {
  type Cfg = {
    enabled: boolean; minPeakRatio: number;
    minSustainedSec: number; minCount: number;
  };
  const [cfg, setCfg] = useState<Cfg | null>(null);
  const [busy, setBusy] = useState(false);
  const [flash, setFlash] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    api.getAnomalyPromotion()
      .then(c => setCfg(c))
      .catch(err => setFlash({ kind: 'err', text: humanize(err) }));
  }, []);

  const save = async () => {
    if (!cfg) return;
    setBusy(true); setFlash(null);
    try {
      const saved = await api.putAnomalyPromotion(cfg);
      setCfg(saved);
      setFlash({ kind: 'ok', text: 'Saved — next evaluator tick picks it up automatically.' });
    } catch (err) {
      setFlash({ kind: 'err', text: humanize(err) });
    } finally {
      setBusy(false);
    }
  };

  if (!cfg) {
    return (
      <div style={{ maxWidth: 640 }}>
        <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Anomaly auto-promotion</h2>
        {flash ? <FlashBox kind={flash.kind}>{flash.text}</FlashBox> : <Spinner />}
      </div>
    );
  }

  return (
    <div style={{ maxWidth: 640 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Anomaly auto-promotion</h2>
      <p style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 18, lineHeight: 1.55 }}>
        The anomaly detector flags patterns that exceed their rolling baseline; this
        promoter graduates the strong, sustained ones into first-class Problems so
        the on-call pager fires. Tighten the thresholds when the detector is too
        chatty, or disable the whole feature while you calibrate it.
      </p>

      <label style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 16 }}>
        <input type="checkbox" checked={cfg.enabled}
          onChange={e => setCfg({ ...cfg, enabled: e.target.checked })} />
        <span style={{ fontSize: 13, color: 'var(--text)' }}>
          Promote strong anomalies into Problems
        </span>
      </label>

      <div style={{ display: 'grid', gap: 12, opacity: cfg.enabled ? 1 : 0.5 }}>
        <Field label="Minimum peak ratio (× baseline)">
          <input type="number" min={1} max={1000} step={0.5}
            value={cfg.minPeakRatio}
            onChange={e => setCfg({ ...cfg, minPeakRatio: Number(e.target.value) })}
            disabled={!cfg.enabled} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            5× = pattern is occurring at least 5 times more than its
            rolling baseline. Default 5.
          </div>
        </Field>

        <Field label="Minimum sustained (seconds since started_at)">
          <input type="number" min={60} max={86400} step={60}
            value={cfg.minSustainedSec}
            onChange={e => setCfg({ ...cfg, minSustainedSec: Number(e.target.value) })}
            disabled={!cfg.enabled} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Filters out one-tick flares. Default 300s (5 min).
          </div>
        </Field>

        <Field label="Minimum count">
          <input type="number" min={1} max={1000000} step={1}
            value={cfg.minCount}
            onChange={e => setCfg({ ...cfg, minCount: Number(e.target.value) })}
            disabled={!cfg.enabled} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Absolute volume floor — a 100× ratio on 2 occurrences is
            meaningless. Default 10.
          </div>
        </Field>
      </div>

      <div style={{ marginTop: 18, display: 'flex', gap: 8, alignItems: 'center' }}>
        <Button variant="primary" onClick={save} disabled={busy}>
          {busy ? 'Saving…' : 'Save'}
        </Button>
        {flash && <FlashBox kind={flash.kind}>{flash.text}</FlashBox>}
      </div>
    </div>
  );
}
