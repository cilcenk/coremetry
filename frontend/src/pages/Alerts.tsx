import { useEffect, useState } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServicePicker } from '@/components/ServicePicker';
import { useAuth } from '@/components/AuthProvider';
import {
  useAlertRules,
  useCreateAlertRule, useUpdateAlertRule,
  useDeleteAlertRule, useEnableAlertRule, useDisableAlertRule,
} from '@/lib/queries';
import { api } from '@/lib/api';
import { tsLong } from '@/lib/utils';
import type { AlertRule, NoisyRule, TimeRange } from '@/lib/types';

// User-saved presets (v0.5.157) — same shape as the built-in
// TEMPLATES but persisted server-side via saved_views (page='
// alert-template'). The queryString column holds the draft as
// JSON; reusing the existing saved-views table avoids inventing
// a new persistence path.
type UserPreset = {
  id: string;
  name: string;
  shared: boolean;
  draft: Partial<AlertRule>;
};

const METRICS = [
  { v: 'error_rate',   label: 'Error rate (%)' },
  { v: 'p99_ms',       label: 'P99 latency (ms)' },
  { v: 'p95_ms',       label: 'P95 latency (ms)' },
  { v: 'p50_ms',       label: 'P50 latency (ms)' },
  { v: 'avg_ms',       label: 'Avg latency (ms)' },
  { v: 'request_rate', label: 'Request rate (/s)' },
  { v: 'error_count',  label: 'Error count' },
];

const COMPARATORS = ['>', '>=', '<', '<='];
const SEVERITIES = ['info', 'warning', 'critical'];

const WINDOWS = [
  { v: 60,    label: '1 min' },
  { v: 300,   label: '5 min' },
  { v: 600,   label: '10 min' },
  { v: 1800,  label: '30 min' },
  { v: 3600,  label: '1 hour' },
];

// Empty draft used for both "+ New" and as the reset value after save.
// Kept at module scope so the create/edit reset paths share one source.
const emptyDraft: Partial<AlertRule> = {
  name: '', service: '', metric: 'error_rate', comparator: '>',
  threshold: 5, windowSec: 300, severity: 'warning', enabled: true,
};

// Alert rule templates — Datadog "monitor templates" pattern.
// Each is a one-click pre-fill of the new-rule form for a
// scenario operators wire up over and over. Tweaks (specific
// service, custom threshold) happen after the operator clicks
// the template; the form stays editable.
//
// Naming convention: `{Severity} · {Scenario}` so the picker
// reads as "what kind of problem is this watching for".
const TEMPLATES: { id: string; label: string; description: string; draft: Partial<AlertRule> }[] = [
  {
    id: 'tpl-high-error-rate',
    label: 'High error rate (>5%)',
    description: 'Fires when a service\'s span error rate stays above 5% for 5 minutes — the canonical "something is broken" alarm.',
    draft: { ...emptyDraft, name: 'High error rate', metric: 'error_rate', comparator: '>', threshold: 5,  windowSec: 300, severity: 'critical' },
  },
  {
    id: 'tpl-warn-error-rate',
    label: 'Warning error rate (>1%)',
    description: 'Lower-severity counterpart — early warning before the critical alarm trips.',
    draft: { ...emptyDraft, name: 'Elevated error rate', metric: 'error_rate', comparator: '>', threshold: 1, windowSec: 600, severity: 'warning' },
  },
  {
    id: 'tpl-slow-p99',
    label: 'Slow P99 (>1s)',
    description: 'Tail-latency regression catcher. Most user-visible slowness lives in P99, not the median.',
    draft: { ...emptyDraft, name: 'Slow P99 latency', metric: 'p99_ms', comparator: '>', threshold: 1000, windowSec: 600, severity: 'warning' },
  },
  {
    id: 'tpl-very-slow-p99',
    label: 'Very slow P99 (>5s)',
    description: 'Critical latency band — typical "user is staring at a spinner" threshold.',
    draft: { ...emptyDraft, name: 'Critical P99 latency', metric: 'p99_ms', comparator: '>', threshold: 5000, windowSec: 300, severity: 'critical' },
  },
  {
    id: 'tpl-traffic-drop',
    label: 'Service disappeared (RPS = 0)',
    description: 'Triggers when request rate drops to zero — useful for detecting a crashed service even when no errors are emitted.',
    draft: { ...emptyDraft, name: 'Service disappeared', metric: 'request_rate', comparator: '<', threshold: 0.01, windowSec: 300, severity: 'critical' },
  },
  {
    id: 'tpl-error-burst',
    label: 'Error burst (>100 errors in 5m)',
    description: 'Absolute count threshold — catches a sudden spike independent of traffic ratio.',
    draft: { ...emptyDraft, name: 'Error count burst', metric: 'error_count', comparator: '>', threshold: 100, windowSec: 300, severity: 'warning' },
  },
];

export default function AlertsPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin';
  const [range, setRange] = useState<TimeRange>({ preset: '15m' });
  const [services, setServices] = useState<string[]>([]);
  const [showForm, setShowForm] = useState(false);
  const [draft, setDraft] = useState<Partial<AlertRule>>(emptyDraft);
  // User-saved presets (v0.5.157). Loaded once per mount; mutations
  // re-fetch eagerly so the strip stays accurate without polling.
  const [presets, setPresets] = useState<UserPreset[]>([]);
  const reloadPresets = () => {
    if (!user) { setPresets([]); return; }
    api.savedViews('alert-template')
      .then(rows => setPresets((rows ?? []).flatMap(v => {
        // queryString holds JSON.stringify(draft). Malformed
        // payloads (manual CH tampering, schema drift) collapse
        // to a dropped entry rather than breaking the whole strip.
        try {
          const draft = JSON.parse(v.queryString) as Partial<AlertRule>;
          return [{ id: v.id, name: v.name, shared: v.ownerId === '', draft }];
        } catch { return []; }
      })))
      .catch(() => setPresets([]));
  };
  useEffect(() => { reloadPresets(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [user]);
  const saveAsPreset = async () => {
    if (!user) {
      alert('Sign in to save presets.');
      return;
    }
    const name = window.prompt('Save preset as:', draft.name || '');
    if (!name) return;
    const wantShared = isAdmin
      && window.confirm(`Save "${name}" as a team-shared preset?\n\nOK = visible to everyone in the org\nCancel = personal only`);
    try {
      // Strip the per-rule `service` field so the preset is
      // reusable across services. The operator can re-pick a
      // service after loading. Everything else (thresholds,
      // dampening, severity, runbook) carries through.
      const { service: _svc, id: _id, builtIn: _bi, enabled: _en, ...templateDraft } = draft;
      void _svc; void _id; void _bi; void _en;
      await api.createSavedView({
        name,
        page: 'alert-template',
        queryString: JSON.stringify(templateDraft),
        shared: wantShared,
      });
      reloadPresets();
    } catch (e) {
      alert('Failed to save preset: ' + (e instanceof Error ? e.message : String(e)));
    }
  };
  const deletePreset = async (id: string) => {
    if (!confirm('Delete this preset?')) return;
    try {
      await api.deleteSavedView(id);
      reloadPresets();
    } catch (e) {
      alert('Failed to delete preset: ' + (e instanceof Error ? e.message : String(e)));
    }
  };
  // Export / import (v0.5.172). Export bundles every preset
  // currently visible to the operator — personal + team-shared
  // (the server-side ListSavedViews already filters to "mine
  // OR shared") — into a JSON file. Import re-creates each
  // entry as a personal preset on the current user; the
  // operator can re-share via the save-with-shared path if
  // they have admin role.
  const exportPresets = () => {
    if (presets.length === 0) {
      alert('No presets to export.');
      return;
    }
    const payload = {
      // Versioned envelope so a future format change can
      // refuse old files cleanly instead of producing broken
      // imports.
      schema: 'coremetry-alert-presets/v1',
      exportedAt: new Date().toISOString(),
      presets: presets.map(p => ({
        name: p.name,
        draft: p.draft,
        // shared flag is informational — re-importing on a
        // different install always starts as personal.
        shared: p.shared,
      })),
    };
    const blob = new Blob([JSON.stringify(payload, null, 2)], {
      type: 'application/json',
    });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `coremetry-alert-presets-${new Date().toISOString().slice(0, 10)}.json`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  };
  const importPresets = async (file: File) => {
    let text: string;
    try { text = await file.text(); }
    catch (e) { alert('Failed to read file: ' + (e instanceof Error ? e.message : String(e))); return; }
    let parsed: unknown;
    try { parsed = JSON.parse(text); }
    catch { alert('File is not valid JSON.'); return; }
    // Loose shape match — we accept both the envelope above
    // AND a bare array (e.g. an operator hand-edits a
    // sub-set), so the export round-trip is forgiving.
    let entries: Array<{ name: string; draft: Partial<AlertRule> }> = [];
    const env = parsed as {
      schema?: string;
      presets?: Array<{ name?: string; draft?: Partial<AlertRule> }>;
    };
    if (env && Array.isArray(env.presets)) {
      entries = env.presets.flatMap(p => p.name && p.draft ? [{ name: p.name, draft: p.draft }] : []);
    } else if (Array.isArray(parsed)) {
      entries = (parsed as Array<{ name?: string; draft?: Partial<AlertRule> }>)
        .flatMap(p => p.name && p.draft ? [{ name: p.name, draft: p.draft }] : []);
    }
    if (entries.length === 0) {
      alert('No valid presets found in the file.');
      return;
    }
    if (!confirm(`Import ${entries.length} preset${entries.length === 1 ? '' : 's'}?`)) {
      return;
    }
    // Parallel create — the server-side endpoint dedups by
    // (name, page) per owner so re-importing is idempotent.
    const results = await Promise.allSettled(entries.map(e =>
      api.createSavedView({
        name: e.name,
        page: 'alert-template',
        queryString: JSON.stringify(e.draft),
      })
    ));
    const failed = results.filter(r => r.status === 'rejected').length;
    reloadPresets();
    if (failed > 0) {
      alert(`Imported ${entries.length - failed} preset${entries.length - failed === 1 ? '' : 's'}; ${failed} failed.`);
    }
  };
  // Non-null while editing — `id` of the row we're editing. Drives the
  // form's "Update" vs "Save" copy and decides between PUT and POST on
  // submit.
  const [editingId, setEditingId] = useState<string | null>(null);

  // Rules query + 4 mutations. Each mutation auto-invalidates
  // the rules cache on success — no manual refresh() coordinator.
  const rulesQ = useAlertRules();
  const rules = rulesQ.isLoading ? undefined : rulesQ.data ?? [];
  const createRule = useCreateAlertRule();
  const updateRule = useUpdateAlertRule();
  const deleteRule  = useDeleteAlertRule();
  const enableRule  = useEnableAlertRule();
  const disableRule = useDisableAlertRule();

  // Service list for the picker — kept on raw fetch since
  // it's a one-shot lookup and the cache value doesn't really
  // need sharing across pages.
  useEffect(() => {
    api.services({ from: 0, to: 0 })
      .then(s => setServices((s ?? []).map(x => x.name)))
      .catch(() => {});
  }, []);

  // Noisy-rules report (v0.5.131). 24h window by default; server
  // caches the heavy GROUP BY for 5 min so a fleet of operators
  // hitting /alerts at the same time shares one round-trip.
  const [noisy, setNoisy] = useState<NoisyRule[] | null>(null);
  // Bulk-apply selection set (v0.5.151). One operator complaint we
  // kept hitting: 5+ rules need the same flap-suppression treatment
  // and clicking Apply → save → close for each one is annoying.
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [bulkBusy, setBulkBusy] = useState(false);
  useEffect(() => {
    api.alertTuningNoisyRules('24h', 10)
      .then(r => setNoisy((r?.rules ?? []).filter(n => n.suggestion !== '')))
      .catch(() => setNoisy(null));
  }, []);
  const applySuggestion = (n: NoisyRule) => {
    const base = (rules ?? []).find(r => r.id === n.ruleId);
    if (!base) return;
    setDraft({
      ...base,
      forSec:      n.suggestedForSec      ?? base.forSec      ?? 0,
      minSamples:  n.suggestedMinSamples  ?? base.minSamples  ?? 0,
      cooldownSec: n.suggestedCooldownSec ?? base.cooldownSec ?? 0,
    });
    setEditingId(n.ruleId);
    setShowForm(true);
    // Scroll the form into view so the operator sees the
    // suggested values immediately.
    setTimeout(() => {
      window.scrollTo({ top: 0, behavior: 'smooth' });
    }, 0);
  };
  // Toggle one row in / out of the bulk selection. Two actions
  // operate on this set: Apply (only rules with concrete knob
  // suggestions are touched) and Disable (every selected rule is
  // flipped off, regardless of suggestion shape). v0.5.154 widened
  // the checkbox eligibility so threshold-only hints can still be
  // bulk-disabled.
  const toggleSelected = (id: string) => {
    setSelected(prev => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  };
  // Rules that have ≥1 concrete dampening value to apply — the
  // "tighten threshold" suggestion sets nothing actionable but the
  // row is still selectable for bulk-disable.
  const actionable = (noisy ?? []).filter(
    n => (n.suggestedForSec ?? 0) > 0
      || (n.suggestedMinSamples ?? 0) > 0
      || (n.suggestedCooldownSec ?? 0) > 0,
  );
  const actionableSelectedCount = actionable.filter(n => selected.has(n.ruleId)).length;
  const allNoisyIDs = (noisy ?? []).map(n => n.ruleId);
  const allSelected = allNoisyIDs.length > 0
    && allNoisyIDs.every(id => selected.has(id));
  const toggleAll = () => {
    setSelected(allSelected ? new Set() : new Set(allNoisyIDs));
  };
  // Bulk-apply path. Builds a patch per rule that ONLY touches the
  // knob the suggestion targets, preserving any operator-set value
  // the suggestion doesn't address. Patches run in parallel since
  // each rule is independent — the React Query mutation invalidates
  // the list eagerly so the table refreshes once all settle.
  const applySelected = async () => {
    if (actionableSelectedCount === 0 || !rules) return;
    setBulkBusy(true);
    try {
      const patches: Promise<unknown>[] = [];
      for (const n of actionable) {
        if (!selected.has(n.ruleId)) continue;
        const base = rules.find(r => r.id === n.ruleId);
        if (!base) continue;
        const patch: Partial<AlertRule> = {
          forSec:      (n.suggestedForSec      ?? 0) || base.forSec      || 0,
          minSamples:  (n.suggestedMinSamples  ?? 0) || base.minSamples  || 0,
          cooldownSec: (n.suggestedCooldownSec ?? 0) || base.cooldownSec || 0,
        };
        patches.push(updateRule.mutateAsync({ id: n.ruleId, patch }));
      }
      await Promise.allSettled(patches);
      // Drop the selection set + refetch the noisy-rules report
      // (a freshly-tightened rule should drop off the list within
      // the server cache window, but a refetch makes the UI feel
      // responsive in the meantime).
      setSelected(new Set());
      api.alertTuningNoisyRules('24h', 10)
        .then(r => setNoisy((r?.rules ?? []).filter(n => n.suggestion !== '')))
        .catch(() => {});
    } finally {
      setBulkBusy(false);
    }
  };
  // Bulk-disable (v0.5.154) — flips `enabled` to false on every
  // selected rule. Reuses deleteAlertRule which is already wired as
  // a soft-disable (SetAlertRuleEnabled false), so re-enabling from
  // the main table works as it did before. One-step confirm because
  // the action affects N rules at once and an operator's misclick
  // would silence everything.
  const disableSelected = async () => {
    if (selected.size === 0) return;
    const ids = Array.from(selected);
    if (!confirm(`Disable ${ids.length} alert rule${ids.length === 1 ? '' : 's'}?\n\nThey can be re-enabled from the rules list.`)) {
      return;
    }
    setBulkBusy(true);
    try {
      await Promise.allSettled(ids.map(id => disableRule.mutateAsync(id)));
      setSelected(new Set());
      api.alertTuningNoisyRules('24h', 10)
        .then(r => setNoisy((r?.rules ?? []).filter(n => n.suggestion !== '')))
        .catch(() => {});
    } finally {
      setBulkBusy(false);
    }
  };

  const startEdit = (r: AlertRule) => {
    setDraft({ ...r });
    setEditingId(r.id);
    setShowForm(true);
  };
  const cancelForm = () => {
    setShowForm(false);
    setEditingId(null);
    setDraft(emptyDraft);
  };

  const save = async () => {
    if (!draft.name || !draft.metric) return;
    if (editingId) {
      await updateRule.mutateAsync({ id: editingId, patch: draft });
    } else {
      await createRule.mutateAsync(draft);
    }
    cancelForm();
  };
  // remove = hard delete (DELETE /api/alert-rules/{id} actually
  // removes the row now, per v0.5.175). `disable` is the soft
  // counterpart for the "I want to silence this but keep the
  // definition" case.
  const remove = async (id: string) => {
    if (!confirm('Delete this rule permanently? The definition will be removed. Use Disable if you want to silence it without losing the rule.')) return;
    await deleteRule.mutateAsync(id);
  };
  const disable = async (id: string) => {
    await disableRule.mutateAsync(id);
  };
  const enable = async (id: string) => {
    await enableRule.mutateAsync(id);
  };

  return (
    <>
      <Topbar title="Alert rules" range={range} onRangeChange={setRange} />
      <div id="content">
        <div className="controls" style={{ marginBottom: 14 }}>
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>
            Evaluator runs every minute. Built-in rules ship pre-configured but
            can be edited or disabled to taste.
          </span>
          <button onClick={() => showForm ? cancelForm() : setShowForm(true)}
                  style={{ marginLeft: 'auto' }}>
            {showForm ? 'Cancel' : '+ New alert rule'}
          </button>
        </div>

        {/* Noisy-rules report — surfaces rules that have opened
            problems most often in the last 24h with a one-click
            "Apply" affordance that pre-fills the edit form with
            the suggested dampening values. Hidden when no rule
            has a suggestion (the report itself fetches the top
            N and we filter to those with a non-empty suggestion). */}
        {noisy && noisy.length > 0 && (
          <div style={{
            background: 'var(--bg1)', border: '1px solid var(--border)',
            borderRadius: 8, padding: 14, marginBottom: 14,
          }}>
            <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 8 }}>
              <span style={{ fontSize: 13, fontWeight: 700 }}>⚡ Noisy rules (last 24h)</span>
              <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                {noisy.length} rule{noisy.length === 1 ? '' : 's'} could be tightened
              </span>
              {selected.size > 0 && (
                <span style={{ marginLeft: 'auto', display: 'inline-flex', gap: 6 }}>
                  <button onClick={applySelected}
                    disabled={bulkBusy || actionableSelectedCount === 0}
                    style={{ fontSize: 11, padding: '4px 12px' }}
                    title={actionableSelectedCount === 0
                      ? 'None of the selected rows ship a concrete value to apply'
                      : `Apply the suggested dampening values to ${actionableSelectedCount} rule${actionableSelectedCount === 1 ? '' : 's'} in one shot`}>
                    {bulkBusy
                      ? 'Working…'
                      : `Apply ${actionableSelectedCount} suggestion${actionableSelectedCount === 1 ? '' : 's'}`}
                  </button>
                  <button onClick={disableSelected}
                    disabled={bulkBusy}
                    className="sec"
                    style={{ fontSize: 11, padding: '4px 12px', color: 'var(--err)' }}
                    title="Disable (soft-delete) every selected rule. Re-enable from the list below.">
                    Disable {selected.size} rule{selected.size === 1 ? '' : 's'}
                  </button>
                </span>
              )}
            </div>
            <div className="table-wrap">
              <table>
                <thead><tr>
                  <th style={{ width: 28 }}>
                    {(noisy ?? []).length > 0 && (
                      <input type="checkbox"
                        checked={allSelected}
                        onChange={toggleAll}
                        title={allSelected ? 'Clear selection' : 'Select all'} />
                    )}
                  </th>
                  <th>Rule</th>
                  <th className="num">Fires/24h</th>
                  <th className="num">Median dur.</th>
                  <th>Last fired</th>
                  <th>Suggestion</th>
                  <th></th>
                </tr></thead>
                <tbody>
                  {noisy.map(n => {
                    const hasKnob = (n.suggestedForSec ?? 0) > 0
                      || (n.suggestedMinSamples ?? 0) > 0
                      || (n.suggestedCooldownSec ?? 0) > 0;
                    return (
                    <tr key={n.ruleId}>
                      <td>
                        <input type="checkbox"
                          checked={selected.has(n.ruleId)}
                          onChange={() => toggleSelected(n.ruleId)}
                          title={hasKnob
                            ? 'Include in bulk-apply OR bulk-disable'
                            : 'Threshold-only hint — Apply has nothing to set, but Disable still works'} />
                      </td>
                      <td><b>{n.ruleName}</b></td>
                      <td className="num mono">{n.openCount}</td>
                      <td className="num mono">
                        {n.medianDurSec >= 60
                          ? `${(n.medianDurSec / 60).toFixed(1)} min`
                          : `${n.medianDurSec.toFixed(0)} s`}
                      </td>
                      <td className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>
                        {tsLong(n.lastFiredNs)}
                      </td>
                      <td style={{ fontSize: 12, color: 'var(--text2)' }}>{n.suggestion}</td>
                      <td>
                        <button className="sec"
                          onClick={() => applySuggestion(n)}
                          style={{ fontSize: 11, padding: '3px 10px' }}
                          title="Open edit form with the suggested dampening values pre-filled">
                          Apply →
                        </button>
                      </td>
                    </tr>
                  );})}
                </tbody>
              </table>
            </div>
          </div>
        )}

        {showForm && (
          <div style={{
            background: 'var(--bg1)', border: '1px solid var(--border)',
            borderRadius: 8, padding: 14, marginBottom: 14,
          }}>
            {/* Template strip — one-click pre-fill for the
                six scenarios operators wire up over and over.
                Picking a template populates the form below;
                the operator can still edit any field
                afterwards (service / threshold / window). */}
            {!editingId && (
              <div style={{ marginBottom: 12 }}>
                <div style={{
                  fontSize: 11, color: 'var(--text2)',
                  fontWeight: 600, letterSpacing: '0.5px',
                  textTransform: 'uppercase', marginBottom: 6,
                }}>
                  Start from template
                </div>
                <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                  {TEMPLATES.map(t => (
                    <button key={t.id} className="sec"
                      onClick={() => setDraft(t.draft)}
                      title={t.description}
                      style={{
                        fontSize: 11, padding: '4px 10px',
                        borderRadius: 14,
                      }}>
                      {t.label}
                    </button>
                  ))}
                </div>
                <div style={{
                  display: 'flex', alignItems: 'baseline', gap: 10,
                  margin: '10px 0 6px',
                }}>
                  <div style={{
                    fontSize: 11, color: 'var(--text2)',
                    fontWeight: 600, letterSpacing: '0.5px',
                    textTransform: 'uppercase',
                  }}>
                    My presets
                  </div>
                  <span style={{ flex: 1 }} />
                  {presets.length > 0 && (
                    <button type="button" className="sec"
                      onClick={exportPresets}
                      style={{ fontSize: 11, padding: '3px 8px' }}
                      title="Download all visible presets as a JSON file">
                      ↓ Export
                    </button>
                  )}
                  <label className="sec"
                    style={{
                      fontSize: 11, padding: '3px 8px',
                      borderRadius: 6, border: '1px solid var(--border)',
                      cursor: 'pointer', background: 'var(--bg3)',
                    }}
                    title="Upload a JSON file exported from another Coremetry install">
                    ↑ Import
                    <input type="file" accept="application/json,.json"
                      style={{ display: 'none' }}
                      onChange={e => {
                        const f = e.target.files?.[0];
                        if (f) { importPresets(f); e.target.value = ''; }
                      }} />
                  </label>
                </div>
                {presets.length > 0 ? (
                  <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                    {presets.map(p => (
                      <span key={p.id} style={{
                        display: 'inline-flex', alignItems: 'center',
                        gap: 4, padding: '4px 4px 4px 10px',
                        borderRadius: 14,
                        background: 'var(--bg2)',
                        border: '1px solid var(--border)',
                        fontSize: 11,
                      }}>
                        <button type="button" onClick={() => setDraft({ ...emptyDraft, ...p.draft })}
                          title={`Load preset · ${p.shared ? 'shared with team' : 'personal'}`}
                          style={{
                            background: 'transparent', border: 'none',
                            padding: 0, color: 'inherit', cursor: 'pointer',
                            fontSize: 11,
                          }}>
                          {p.shared ? '◍ ' : '★ '}{p.name}
                        </button>
                        <button type="button" onClick={() => deletePreset(p.id)}
                          title="Delete preset"
                          style={{
                            background: 'transparent', border: 'none',
                            padding: '0 6px', color: 'var(--err)',
                            cursor: 'pointer', fontSize: 12,
                          }}>×</button>
                      </span>
                    ))}
                  </div>
                ) : (
                  <div style={{ fontSize: 11, color: 'var(--text3)' }}>
                    None yet — save the current draft or import a JSON bundle.
                  </div>
                )}
              </div>
            )}
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 10 }}>
              <Field label="Name">
                <input value={draft.name ?? ''}
                  onChange={e => setDraft({ ...draft, name: e.target.value })}
                  placeholder="e.g. High error rate on api-gateway" />
              </Field>
              <Field label="Service (empty = all)">
                <ServicePicker value={draft.service ?? ''} onChange={v => setDraft({ ...draft, service: v })}
                  placeholder="Service…" width="100%" />
              </Field>
              <Field label="Severity">
                <select value={draft.severity}
                  onChange={e => setDraft({ ...draft, severity: e.target.value })}>
                  {SEVERITIES.map(s => <option key={s} value={s}>{s}</option>)}
                </select>
              </Field>
              <Field label="Metric">
                <select value={draft.metric}
                  onChange={e => setDraft({ ...draft, metric: e.target.value })}>
                  {METRICS.map(m => <option key={m.v} value={m.v}>{m.label}</option>)}
                </select>
              </Field>
              <Field label="Comparator">
                <select value={draft.comparator}
                  onChange={e => setDraft({ ...draft, comparator: e.target.value })}>
                  {COMPARATORS.map(c => <option key={c} value={c}>{c}</option>)}
                </select>
              </Field>
              <Field label="Threshold">
                <ThresholdField
                  value={draft.threshold ?? 0}
                  service={draft.service ?? ''}
                  metric={draft.metric ?? 'error_rate'}
                  comparator={draft.comparator ?? '>'}
                  onChange={v => setDraft({ ...draft, threshold: v })}
                  onApplySeverity={sev => setDraft(d => ({ ...d, severity: sev }))}
                />
              </Field>
              <Field label="Window">
                <select value={draft.windowSec}
                  onChange={e => setDraft({ ...draft, windowSec: Number(e.target.value) })}>
                  {WINDOWS.map(w => <option key={w.v} value={w.v}>{w.label}</option>)}
                </select>
              </Field>
            </div>
            {/* Noise-dampening knobs (v0.5.127-129). All three
                default to 0 = legacy "fire immediately" behaviour.
                Operators tune per-rule when prod sends too many
                alerts:
                  • For — sustained breach gate (Prometheus `for:`)
                  • Min samples — sample-count floor (kills 1/1 = 100%)
                  • Cooldown — post-resolution silence (kills jitter) */}
            <div style={{ marginTop: 10,
              display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 10 }}>
              <Field label="Sustain (sec) — fires only after breach holds this long">
                <input type="number" min={0} step={30}
                  value={draft.forSec ?? 0}
                  onChange={e => setDraft({ ...draft, forSec: Number(e.target.value) })}
                  placeholder="0 = immediate"
                  style={{ width: '100%' }} />
              </Field>
              <Field label="Min samples — require N requests in window">
                <input type="number" min={0} step={10}
                  value={draft.minSamples ?? 0}
                  onChange={e => setDraft({ ...draft, minSamples: Number(e.target.value) })}
                  placeholder="0 = no floor"
                  style={{ width: '100%' }} />
              </Field>
              <Field label="Cooldown (sec) — silence after auto-resolve">
                <input type="number" min={0} step={60}
                  value={draft.cooldownSec ?? 0}
                  onChange={e => setDraft({ ...draft, cooldownSec: Number(e.target.value) })}
                  placeholder="0 = immediate re-open"
                  style={{ width: '100%' }} />
              </Field>
            </div>
            {/* Runbook URL — optional. Surfaces on Problem
                detail when the rule fires so the oncall lands
                on the team's playbook in one click. */}
            <div style={{ marginTop: 10 }}>
              <Field label="Runbook URL (optional)">
                <input value={draft.runbookUrl ?? ''}
                  onChange={e => setDraft({ ...draft, runbookUrl: e.target.value })}
                  placeholder="https://wiki.internal/runbook/high-error-rate"
                  style={{ width: '100%' }} />
              </Field>
            </div>
            <div style={{ marginTop: 10, display: 'flex', gap: 8, alignItems: 'center' }}>
              <button onClick={save}>{editingId ? 'Update rule' : 'Save rule'}</button>
              {!editingId && (
                <button type="button" className="sec" onClick={saveAsPreset}
                  disabled={!draft.metric}
                  title={isAdmin
                    ? 'Save this draft as a reusable preset — admins can share with the team'
                    : 'Save this draft as a personal preset'}>
                  ★ Save as preset
                </button>
              )}
              {editingId && (
                <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                  Editing <code>{editingId}</code>
                  {draft.builtIn && (
                    <span style={{ marginLeft: 6, color: 'var(--text2)' }}>
                      (built-in — edits persist; preset values aren't restored on next boot)
                    </span>
                  )}
                </span>
              )}
            </div>
          </div>
        )}

        {rules === undefined && <Spinner />}
        {rules && rules.length === 0 && <Empty icon="🔔" title="No alert rules" />}
        {rules && rules.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Service</th>
                  <th>Condition</th>
                  <th>Window</th>
                  <th>Severity</th>
                  <th>Enabled</th>
                  <th>Type</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {rules.map(r => (
                  <tr key={r.id}>
                    <td><b>{r.name}</b></td>
                    <td className="mono">{r.service || '— all —'}</td>
                    <td className="mono">{r.metric} {r.comparator} {r.threshold}</td>
                    <td>{r.windowSec / 60} min</td>
                    <td><SeverityBadge s={r.severity} /></td>
                    <td>{r.enabled
                      ? <span className="badge b-ok">ON</span>
                      : <span className="badge b-gray">OFF</span>}</td>
                    <td>{r.builtIn
                      ? <span className="badge b-info">BUILT-IN</span>
                      : <span className="badge b-gray">custom</span>}</td>
                    <td style={{ display: 'flex', gap: 6, justifyContent: 'flex-end' }}>
                      <button className="sec" onClick={() => startEdit(r)}>Edit</button>
                      {r.enabled
                        ? <button className="sec" onClick={() => disable(r.id)}
                            title="Silence the rule without removing its definition">
                            Disable
                          </button>
                        : <button className="sec" onClick={() => enable(r.id)}>Enable</button>}
                      <button className="sec" onClick={() => remove(r.id)}
                        title="Remove the rule entirely from ClickHouse"
                        style={{ color: 'var(--err)' }}>
                        Delete
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 11, color: 'var(--text2)' }}>
      {label}
      {children}
    </label>
  );
}

function SeverityBadge({ s }: { s: string }) {
  const cls = s === 'critical' ? 'b-err' : s === 'warning' ? 'b-warn' : 'b-info';
  return <span className={`badge ${cls}`}>{s.toUpperCase()}</span>;
}

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
function ThresholdField({ value, service, metric, comparator, onChange, onApplySeverity }: {
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
        <button type="button" onClick={suggest} disabled={busy}
          className="sec"
          title="Suggest threshold from the last 7 days of this metric"
          style={{ fontSize: 11, padding: '2px 8px', color: 'var(--accent2)', whiteSpace: 'nowrap' }}>
          {busy ? '…' : '✨ Suggest'}
        </button>
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
            <button type="button" className="sec"
              style={{ fontSize: 11, padding: '2px 6px' }}
              onClick={() => apply(data.p50)} title="Apply p50 to threshold">
              p50: {fmt(data.p50)}
            </button>
            <button type="button" className="sec"
              style={{ fontSize: 11, padding: '2px 6px' }}
              onClick={() => apply(data.p95)} title="Apply p95">
              p95: {fmt(data.p95)}
            </button>
            <button type="button" className="sec"
              style={{ fontSize: 11, padding: '2px 6px' }}
              onClick={() => apply(data.p99)} title="Apply p99">
              p99: {fmt(data.p99)}
            </button>
            <button type="button"
              style={{ fontSize: 11, padding: '2px 6px', background: 'rgba(255,193,7,.15)', border: '1px solid rgba(255,193,7,.4)', color: 'var(--warn)' }}
              onClick={() => apply(data.suggestedWarning, 'warning')}
              title="Apply suggested warning threshold + severity">
              warn: {fmt(data.suggestedWarning)}
            </button>
            <button type="button"
              style={{ fontSize: 11, padding: '2px 6px', background: 'rgba(255,82,82,.12)', border: '1px solid rgba(255,82,82,.4)', color: 'var(--err)' }}
              onClick={() => apply(data.suggestedCritical, 'critical')}
              title="Apply suggested critical threshold + severity">
              crit: {fmt(data.suggestedCritical)}
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
