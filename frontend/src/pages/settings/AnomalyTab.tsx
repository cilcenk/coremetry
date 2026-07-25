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
    enabled: boolean; minPeakRatio: number; criticalPeakRatio: number;
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

        {/* v0.9.247 — severity cut-off, previously a hard-coded 20 in the
            evaluator. It sat invisibly ABOVE the promotion gate, so an
            operator raising "minimum peak ratio" to 20+ to get FEWER pages
            silently turned every surviving promotion critical. Surfacing it
            makes the two decisions independent: the gate controls HOW MANY
            promote, this controls how many of those page. */}
        <Field label="Critical at peak ratio (x baseline)">
          <input type="number" min={cfg.minPeakRatio} max={1000} step={0.5}
            value={cfg.criticalPeakRatio}
            onChange={e => setCfg({ ...cfg, criticalPeakRatio: Number(e.target.value) })}
            disabled={!cfg.enabled} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            At or above this ratio a promoted anomaly opens as <b>critical</b>;
            below it, <b>warning</b>. Must be &ge; the minimum peak ratio &mdash;
            setting them equal means every promoted anomaly pages. Default 20.
            {cfg.criticalPeakRatio <= cfg.minPeakRatio && (
              <div style={{ color: 'var(--warn)', marginTop: 4 }}>
                Su an esit: promote edilen HER anomali critical acilir.
              </div>
            )}
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

      <hr style={{ border: 0, borderTop: '1px solid var(--border)', margin: '28px 0 22px' }} />
      <EscalationSection />
    </div>
  );
}

// ── Age-based escalation (v0.9.248) ─────────────────────────────
//
// Lives on this tab rather than its own because an operator arrives
// here asking one question — "why am I getting so many pages?" — and
// the two halves of the answer are the promotion gate above and this
// ladder. It is NOT scoped to anomalies though: every open Problem
// climbs it, whichever rule opened it. The copy says so, because
// putting it under a heading called "Anomaly auto-promotion" would
// otherwise imply a narrower blast radius than it has.
//
// The windows were hard-coded 15 min / 30 min until now, which meant
// tightening promotion barely helped: anything that got through
// escalated itself to critical half an hour later and re-fired the
// notify channel on the way (escalateStaleProblems calls
// SendProblemAlert on every bump).
function EscalationSection() {
  type Esc = { enabled: boolean; infoToWarningSec: number; warningToCriticalSec: number };
  const [cfg, setCfg] = useState<Esc | null>(null);
  const [busy, setBusy] = useState(false);
  const [flash, setFlash] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    api.getProblemEscalation()
      .then(c => setCfg(c))
      .catch(err => setFlash({ kind: 'err', text: humanize(err) }));
  }, []);

  const save = async () => {
    if (!cfg) return;
    setBusy(true); setFlash(null);
    try {
      const saved = await api.putProblemEscalation(cfg);
      setCfg(saved);
      setFlash({ kind: 'ok', text: 'Saved — next evaluator tick picks it up automatically.' });
    } catch (err) {
      setFlash({ kind: 'err', text: humanize(err) });
    } finally {
      setBusy(false);
    }
  };

  const mins = (sec: number) => `${Math.round(sec / 60)} min`;

  return (
    <div>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Age-based escalation</h2>
      <p style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 18, lineHeight: 1.55 }}>
        An open Problem nobody acks climbs the severity ladder on its own:
        info &rarr; warning &rarr; critical. Each bump re-fires the notify channel, so
        severity-gated pagers hear about it again. This applies to <b>every</b> Problem,
        not just promoted anomalies &mdash; turn it off if &quot;still open after 30
        minutes&quot; is normal for your fleet.
      </p>

      {!cfg ? (
        flash ? <FlashBox kind={flash.kind}>{flash.text}</FlashBox> : <Spinner />
      ) : (
        <>
          <label style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 16 }}>
            <input type="checkbox" checked={cfg.enabled}
              onChange={e => setCfg({ ...cfg, enabled: e.target.checked })} />
            <span style={{ fontSize: 13, color: 'var(--text)' }}>
              Escalate open Problems as they age
            </span>
          </label>

          <div style={{ display: 'grid', gap: 12, opacity: cfg.enabled ? 1 : 0.5 }}>
            <Field label="info → warning after (seconds)">
              <input type="number" min={60} max={604800} step={60}
                value={cfg.infoToWarningSec}
                onChange={e => setCfg({ ...cfg, infoToWarningSec: Number(e.target.value) })}
                disabled={!cfg.enabled} />
              <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                Currently {mins(cfg.infoToWarningSec)}. Default 900s (15 min).
              </div>
            </Field>

            <Field label="warning → critical after (seconds)">
              <input type="number" min={cfg.infoToWarningSec} max={604800} step={60}
                value={cfg.warningToCriticalSec}
                onChange={e => setCfg({ ...cfg, warningToCriticalSec: Number(e.target.value) })}
                disabled={!cfg.enabled} />
              <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                Currently {mins(cfg.warningToCriticalSec)}. Default 1800s (30 min).
                Must be &ge; the info &rarr; warning window.
              </div>
            </Field>
          </div>

          <div style={{ marginTop: 18, display: 'flex', gap: 8, alignItems: 'center' }}>
            <Button variant="primary" onClick={save} disabled={busy}>
              {busy ? 'Saving…' : 'Save'}
            </Button>
            {flash && <FlashBox kind={flash.kind}>{flash.text}</FlashBox>}
          </div>
        </>
      )}
    </div>
  );
}
