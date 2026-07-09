import { useEffect, useState } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServicePicker } from '@/components/ServicePicker';
import { Button } from '@/components/ui';
import { useAuth } from '@/components/AuthProvider';
import {
  useAlertRules,
  useCreateAlertRule, useUpdateAlertRule,
  useDeleteAlertRule, useEnableAlertRule, useDisableAlertRule,
} from '@/lib/queries';
import { api } from '@/lib/api';
import { useUrlRange } from '@/lib/useUrlRange';
import type { AlertRule } from '@/lib/types';
import {
  METRICS, COMPARATORS, SEVERITIES, WINDOWS, emptyDraft, TEMPLATES,
  type UserPreset,
} from './alerts/constants';
import { ThresholdField } from './alerts/ThresholdField';
import { ConditionPreview } from './alerts/ConditionPreview';
import { NoisyRulesPanel } from './alerts/NoisyRulesPanel';

export default function AlertsPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin';
  // v0.8.424 — operator-reported: viewers saw + New rule / Edit /
  // Enable / Disable / Delete (the backend already 403s via
  // editorRoles, but the UI advertised write access). Invariant #7:
  // viewer SEES state read-only — rules render, mutations hide.
  const canEdit = user?.role === 'admin' || user?.role === 'editor';
  const [range, setRange] = useUrlRange('30m');
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
  const rulesAll = rulesQ.isLoading ? undefined : rulesQ.data ?? [];
  // v0.5.305 — filter chip strip: All / Metric / Watcher.
  // Watchers = saved-search log alerts (metric='log_query');
  // operators asked to see them on this page alongside metric
  // rules with a clear visual scope.
  const [ruleKind, setRuleKind] = useState<'all' | 'metric' | 'watcher'>('all');
  const rules = !rulesAll ? undefined : rulesAll.filter(r => {
    if (ruleKind === 'all') return true;
    const isWatcher = r.metric === 'log_query';
    return ruleKind === 'watcher' ? isWatcher : !isWatcher;
  });
  const watcherCount = rulesAll?.filter(r => r.metric === 'log_query').length ?? 0;
  const metricCount  = (rulesAll?.length ?? 0) - watcherCount;
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

  // Open the edit form pre-filled from a noisy-rules suggestion.
  // NoisyRulesPanel owns the report + bulk-apply state; this single
  // callback is the one cross-cutting concern (it drives the parent's
  // form), so behaviour matches the pre-refactor inline applySuggestion.
  const editFromSuggestion = (d: Partial<AlertRule>, ruleId: string) => {
    setDraft(d);
    setEditingId(ruleId);
    setShowForm(true);
    // Scroll the form into view so the operator sees the
    // suggested values immediately.
    setTimeout(() => {
      window.scrollTo({ top: 0, behavior: 'smooth' });
    }, 0);
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
          {canEdit && (
            <Button variant="primary" onClick={() => showForm ? cancelForm() : setShowForm(true)}
                    style={{ marginLeft: 'auto' }}>
              {showForm ? 'Cancel' : '+ New alert rule'}
            </Button>
          )}
        </div>

        {/* Noisy-rules report — surfaces rules that have opened
            problems most often in the last 24h with a one-click
            "Apply" affordance that pre-fills the edit form with
            the suggested dampening values. Self-hides when no rule
            has a suggestion. */}
        {/* v0.8.424 — editors only: the panel is pure mutation surface
            (Apply/Disable suggestions pre-filling the edit form a
            viewer doesn't have). */}
        {canEdit && (
          <NoisyRulesPanel rules={rules} onEditFromSuggestion={editFromSuggestion} />
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
            {/* Live condition preview — the rule's metric over the last hour with
                the threshold line + a "would have fired N×" count, so the
                operator tunes the threshold against real data before saving. */}
            <ConditionPreview draft={draft} />
            <div style={{ marginTop: 10, display: 'flex', gap: 8, alignItems: 'center' }}>
              <Button variant="primary" onClick={save}>{editingId ? 'Update rule' : 'Save rule'}</Button>
              {!editingId && (
                <Button variant="secondary" type="button" onClick={saveAsPreset}
                  disabled={!draft.metric}
                  title={isAdmin
                    ? 'Save this draft as a reusable preset — admins can share with the team'
                    : 'Save this draft as a personal preset'}>
                  ★ Save as preset
                </Button>
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
        {/* v0.5.305 — kind filter chips so operators can scope
            the table to just their watchers (saved log alerts)
            without scrolling past 50+ metric rules. */}
        {rulesAll && rulesAll.length > 0 && (
          <div style={{ display: 'flex', gap: 6, marginBottom: 8, alignItems: 'center' }}>
            {([
              { key: 'all',     label: 'All',     count: rulesAll.length },
              { key: 'metric',  label: 'Metric',  count: metricCount },
              { key: 'watcher', label: 'Watcher', count: watcherCount },
            ] as const).map(t => (
              <button key={t.key}
                onClick={() => setRuleKind(t.key)}
                className={ruleKind === t.key ? '' : 'sec'}
                style={{ fontSize: 12, padding: '3px 10px' }}
                title={t.key === 'watcher'
                  ? 'Saved log-search alerts created via /logs Create watcher'
                  : t.key === 'metric'
                  ? 'Metric-threshold alerts (RPS / error rate / p99 / etc.)'
                  : 'All alert rules'}>
                {t.label}
                <span style={{
                  marginLeft: 6, fontSize: 10, color: 'var(--text3)',
                  fontFamily: 'ui-monospace, monospace',
                }}>{t.count}</span>
              </button>
            ))}
          </div>
        )}
        {rules && rules.length === 0 && (
          <Empty icon="🔔" title="No alert rules"
            action={canEdit
              ? <Button variant="primary" onClick={() => setShowForm(true)}>+ New rule</Button>
              : undefined}>
            <div style={{ marginTop: 6, color: 'var(--text2)' }}>
              Alert rules turn anomaly detectors and threshold checks into
              named, routable problems — or import from your existing config
              via the SQL playground.
            </div>
          </Empty>
        )}
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
                {rules.map(r => {
                  // v0.5.305 — Watchers (Logs → Create watcher;
                  // saved-search alerts) live in the same
                  // alert_rules table with metric='log_query'.
                  // Surface them with their own badge + render
                  // the saved query in the Condition column so
                  // the operator can tell at a glance which row
                  // is a watcher vs a metric alert.
                  const isWatcher = r.metric === 'log_query';
                  return (
                  <tr key={r.id} style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 40px' }}>
                    <td><b>{r.name}</b></td>
                    <td className="mono">{r.service || (isWatcher ? '— logs —' : '— all —')}</td>
                    <td className="mono" style={{ maxWidth: 380 }}>
                      {isWatcher ? (
                        <>
                          <code title={r.logQuery}
                            style={{
                              display: 'inline-block', maxWidth: '100%',
                              overflow: 'hidden', textOverflow: 'ellipsis',
                              whiteSpace: 'nowrap', verticalAlign: 'middle',
                              padding: '1px 4px', borderRadius: 3,
                              background: 'var(--bg3)', fontSize: 11,
                            }}>
                            {r.logQuery || '(empty)'}
                          </code>
                          <span style={{ color: 'var(--text3)', marginLeft: 6 }}>
                            count {r.comparator} {r.threshold}
                          </span>
                        </>
                      ) : (
                        <>{r.metric} {r.comparator} {r.threshold}</>
                      )}
                    </td>
                    <td>{r.windowSec / 60} min</td>
                    <td><SeverityBadge s={r.severity} /></td>
                    <td>{r.enabled
                      ? <span className="badge b-ok">ON</span>
                      : <span className="badge b-gray">OFF</span>}</td>
                    <td>
                      {isWatcher
                        ? <span className="badge b-info" title="Saved log-search alert created via /logs Create watcher">WATCHER</span>
                        : r.builtIn
                          ? <span className="badge b-info">BUILT-IN</span>
                          : <span className="badge b-gray">metric</span>}
                    </td>
                    <td style={{ display: 'flex', gap: 6, justifyContent: 'flex-end' }}>
                      {isWatcher && (
                        <a className="sec"
                          href={`/logs?q=${encodeURIComponent(r.logQuery || '')}`}
                          title="Open the saved log search in /logs"
                          style={{ textDecoration: 'none' }}>
                          ↗ logs
                        </a>
                      )}
                      {canEdit && (
                        <>
                          <Button variant="secondary" size="sm" onClick={() => startEdit(r)}>Edit</Button>
                          {r.enabled
                            ? <Button variant="secondary" size="sm" onClick={() => disable(r.id)}
                                title="Silence the rule without removing its definition">
                                Disable
                              </Button>
                            : <Button variant="secondary" size="sm" onClick={() => enable(r.id)}>Enable</Button>}
                          <Button variant="danger" size="sm" onClick={() => remove(r.id)}
                            title="Remove the rule entirely from ClickHouse">
                            Delete
                          </Button>
                        </>
                      )}
                    </td>
                  </tr>
                  );
                })}
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
