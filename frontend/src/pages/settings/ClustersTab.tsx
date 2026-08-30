import { useMemo, useState, type FormEvent } from 'react';
import { fmtDateTime } from '@/lib/utils';
import { SpanClusterValuesPanel } from './SpanClusterValuesPanel';
import { Combobox } from '@/components/Combobox';
import { Spinner } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import { useSettingsLoad, SettingsLoadError } from './shared';
import { useClusters } from '@/lib/queries';
import type { ThanosAuthType, ThanosClusterSnapshot } from '@/lib/types';

// ClustersTab — remote OpenShift clusters whose Thanos Querier
// feeds the /clusters page (v0.8.577, audit: docs/audit/
// thanos-multicluster-metrics-audit.md §2/§7.5). Whole list is
// saved atomically; per-cluster tokens follow the Tempo secret
// contract (never echoed, empty input keeps the stored one).
//
// The cluster NAME is the APM join key: it must equal the
// k8s.cluster.name / openshift.cluster.name value the cluster's
// spans report, or the service→cluster pivot won't light up. The
// name field therefore suggests OBSERVED cluster names (from
// telemetry, last 24h) and warns — without blocking — when the
// typed name isn't among them (Thanos-first onboarding order is
// legitimate).

interface EditRow {
  id: string;       // server-owned; '' until first save
  name: string;
  url: string;
  thanosLabelName: string;
  thanosLabelValue: string;
  spanClusterValue: string;
  /** v0.10.139 — virgülle ayrılmış çoklu değer; kaydetmede listeye çevrilir. */
  spanClusterValues: string;
  thanosLabelSource?: 'auto' | 'manual';
  thanosLabelDetectedAt?: number;
  labelCheck?: { ok: boolean; series: number; checkedAt: string; error?: string };
  authType: ThanosAuthType;
  token: string;    // only ever holds a NEW token; '' = keep stored
  hasToken: boolean;
  namespaceFilter: string;
  insecureSkipVerify: boolean;
  enabled: boolean;
}

function fromSnapshot(c: ThanosClusterSnapshot): EditRow {
  return {
    id: c.id || '', name: c.name, url: c.url,
    thanosLabelName: c.thanosLabelName || '',
    thanosLabelValue: c.thanosLabelValue || '',
    spanClusterValue: c.spanClusterValue || '',
    spanClusterValues: (c.spanClusterValues && c.spanClusterValues.length ? c.spanClusterValues : [c.spanClusterValue || '']).filter(Boolean).join(', '),
    thanosLabelSource: c.thanosLabelSource, thanosLabelDetectedAt: c.thanosLabelDetectedAt, labelCheck: c.labelCheck,
    authType: (c.authType || 'none') as ThanosAuthType,
    token: '', hasToken: c.hasToken,
    namespaceFilter: c.namespaceFilter || '',
    insecureSkipVerify: !!c.insecureSkipVerify,
    enabled: c.enabled,
  };
}

const EMPTY_ROW: EditRow = {
  id: '', name: '', url: '', authType: 'bearer', token: '', hasToken: false,
  thanosLabelName: '', thanosLabelValue: '', spanClusterValue: '', spanClusterValues: '',
  namespaceFilter: '', insecureSkipVerify: false, enabled: true,
};

export function ClustersTab() {
  const [rows, setRows] = useState<EditRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  // v0.10.128 — per-row "Test label" result (probe endpoint, on click only).
  const [probe, setProbe] = useState<Record<string, string>>({});
  // v0.10.140 — "Algıla": sunucu enjeksiyonsuz sorguyla etiketi bulur ve
  // (belirsiz değilse) kaydeder; belirsizlikte adaylar satırın altında.
  const [detect, setDetect] = useState<Record<string, { text: string; candidates?: Record<string, string[]> }>>({});
  const runDetect = async (r: EditRow) => {
    const ref = r.id || r.name.trim();
    if (!ref) return;
    setDetect(p => ({ ...p, [ref]: { text: '…' } }));
    try {
      const res = await api.thanosClusterDetect(ref, true);
      const d = res.detection;
      if (res.error) setDetect(p => ({ ...p, [ref]: { text: `✗ ${res.error}`, candidates: d?.candidates } }));
      else if (res.applied) {
        setDetect(p => ({ ...p, [ref]: { text: d?.label ? `✓ ${d.label}="${d.value}" (auto, saved)` : '✓ aday etiket yok — matcher gerekmiyor (saved)' } }));
        const next = await api.getThanosSettings();
        setRows((next.clusters ?? []).map(fromSnapshot));
      } else setDetect(p => ({ ...p, [ref]: { text: d?.ambiguous ? `? ${d.label}: birden çok değer — birini seçin` : 'algılanamadı', candidates: d?.candidates } }));
    } catch (err) {
      setDetect(p => ({ ...p, [ref]: { text: `✗ ${err instanceof Error ? err.message : 'detect failed'}` } }));
    }
  };
  const runProbe = async (r: EditRow) => {
    const ref = r.id || r.name.trim();
    if (!ref) return;
    setProbe(p => ({ ...p, [ref]: '…' }));
    try {
      const res = await api.thanosClusterProbe(ref);
      setProbe(p => ({ ...p, [ref]: res.ok
        ? `✓ ${res.series} node series${res.label ? ` with ${res.label}="${res.value}"` : ''}`
        : `✗ ${res.error || 'no series'}` }));
    } catch (err) {
      setProbe(p => ({ ...p, [ref]: `✗ ${err instanceof Error ? err.message : 'probe failed'}` }));
    }
  };

  // Observed cluster names from telemetry (last 24h) — the
  // suggestion source for the join-key warning. timeRange math
  // stays inside useMemo (v0.5.184 rule).
  const [fromNs, toNs] = useMemo(() => {
    const now = Date.now() * 1e6;
    return [now - 24 * 3600 * 1e9, now];
  }, []);
  const observedQ = useClusters(fromNs, toNs);
  const observed = useMemo(() => new Set(observedQ.data ?? []), [observedQ.data]);
  // Same list, array-shaped + sorted — the Combobox suggestion source.
  // Sorting stays here (not in render) so the array identity is stable
  // across re-renders and the dropdown's filter memo doesn't rebuild.
  const observedNames = useMemo(() => [...observed].sort(), [observed]);

  const { loaded, error: loadErr, retry } = useSettingsLoad(
    () => api.getThanosSettings(),
    s => {
      setRows((s.clusters ?? []).map(fromSnapshot));
    },
  );

  const patch = (i: number, p: Partial<EditRow>) =>
    setRows(rs => rs.map((r, j) => (j === i ? { ...r, ...p } : r)));

  const save = async (e: FormEvent) => {
    e.preventDefault();
    // The name field carried `required` while it was a native <input>.
    // The Combobox atom renders its own input and takes no `required`
    // prop, so the invariant moves to the submit path — where it always
    // belonged for a repeated-row form: the browser bubble points at a
    // row that may be scrolled out of view and says only "fill this in",
    // never WHY an empty join key is fatal.
    const nameless = rows.findIndex(r => !r.name.trim());
    if (nameless >= 0) {
      setMsg({ kind: 'err', text: `Cluster #${nameless + 1} has no name — the name is the join key and cannot be empty.` });
      return;
    }
    setBusy(true); setMsg(null);
    try {
      const next = await api.putThanosSettings({
        clusters: rows.map(r => ({
          id: r.id || undefined, // server-owned: lets a rename keep its id + token
          name: r.name.trim(), url: r.url.trim(), authType: r.authType,
          token: r.token, // '' keeps stored (server contract, id/name-matched)
          thanosLabelName: r.thanosLabelName.trim() || undefined,
          thanosLabelValue: r.thanosLabelValue.trim() || undefined,
          spanClusterValue: r.spanClusterValue.trim() || undefined,
          spanClusterValues: r.spanClusterValues.split(',').map(v => v.trim()).filter(Boolean),
          namespaceFilter: r.namespaceFilter.trim() || undefined,
          insecureSkipVerify: r.insecureSkipVerify, enabled: r.enabled,
        })),
      });
      setRows((next.clusters ?? []).map(fromSnapshot));
      const on = (next.clusters ?? []).filter(c => c.enabled).length;
      setMsg({ kind: 'ok', text: `Saved — ${on} cluster(s) enabled.` });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Save failed' });
    } finally {
      setBusy(false);
    }
  };

  if (loadErr) return <SettingsLoadError error={loadErr} onRetry={retry} />;
  if (!loaded) return <Spinner />;

  return (
    <div style={{ maxWidth: 760 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Remote clusters (Thanos)</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Each entry points at an OpenShift cluster's Thanos Querier route.
        The <strong>/clusters</strong> page pulls per-pod CPU + memory from
        every enabled entry. Service pages pivot into a cluster when its <strong>name</strong> matches the
        <code style={{ background: 'var(--bg0)', padding: '1px 5px', borderRadius: 3, margin: '0 4px' }}>k8s.cluster.name</code>
        value the telemetry reports — <em>or</em> when the span values are mapped to it (Detect label / assign values below, v0.10.139-141).
        Typical auth: a ServiceAccount token bound to the
        <code style={{ background: 'var(--bg0)', padding: '1px 5px', borderRadius: 3, margin: '0 4px' }}>cluster-monitoring-view</code>
        ClusterRole. Read-only — Coremetry never writes to Thanos.
      </p>

      <form onSubmit={save}>
        {rows.length === 0 && (
          <div style={{ padding: 14, fontSize: 12, color: 'var(--text3)',
            border: '1px dashed var(--border)', borderRadius: 8, marginBottom: 12 }}>
            No clusters yet — add the first one below.
          </div>
        )}
        {rows.map((r, i) => {
          // v0.10.187 (F12) — ad telemetride yoksa bile EŞLENMİŞ span değerlerinden biri görülüyorsa «not in telemetry» yanlış olurdu (139-141 otomatik eşleme).
          const mapped = r.spanClusterValues.split(',').map(v => v.trim()).filter(Boolean);
          const nameKnown = r.name.trim() === '' || observed.size === 0 || observed.has(r.name.trim()) || mapped.some(v => observed.has(v));
          return (
            <div key={i} style={{
              marginBottom: 12, padding: 14, borderRadius: 8,
              background: 'var(--bg2)', border: '1px solid var(--border)',
              opacity: r.enabled ? 1 : 0.65,
            }}>
              <div style={{ display: 'flex', gap: 10, marginBottom: 10, alignItems: 'flex-end' }}>
                <label style={{ flex: 1 }}>
                  <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
                    Cluster name (join key)
                    {r.id && (
                      <span className="badge mono" style={{ marginLeft: 8 }}
                        title="Opaque, immutable cluster id — the root of the entity hierarchy. Renaming the cluster keeps it.">
                        {r.id}
                      </span>
                    )}
                    {!nameKnown && (
                      <span className="badge b-warn" style={{ marginLeft: 8 }}
                        title="Name not seen in the last 24h of telemetry — Thanos data will not match service pages. The warning clears once the cluster starts reporting.">
                        not in telemetry
                      </span>
                    )}
                  </div>
                  <Combobox value={r.name} onChange={v => patch(i, { name: v })}
                    options={observedNames} placeholder="prod-ist" width="100%" />
                </label>
                <label style={{ display: 'flex', alignItems: 'center', gap: 6, paddingBottom: 6 }}>
                  <input type="checkbox" checked={r.enabled}
                    onChange={e => patch(i, { enabled: e.target.checked })} />
                  <span style={{ fontSize: 12 }}>Enabled</span>
                </label>
                <Button type="button" variant="ghost" size="sm"
                  onClick={() => setRows(rs => rs.filter((_, j) => j !== i))}>
                  Remove
                </Button>
              </div>
              <label style={{ display: 'block', marginBottom: 10 }}>
                <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Thanos Querier URL</div>
                <input value={r.url} required={r.enabled}
                  onChange={e => patch(i, { url: e.target.value })}
                  placeholder="https://thanos-querier-openshift-monitoring.apps.prod-ist.example.com"
                  style={{ width: '100%' }} />
              </label>
              <div style={{ display: 'flex', gap: 10, marginBottom: 10 }}>
                <label style={{ width: 180 }}>
                  <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Auth</div>
                  <select value={r.authType}
                    onChange={e => patch(i, { authType: e.target.value as ThanosAuthType })}
                    style={{ width: '100%' }}>
                    <option value="bearer">Bearer token</option>
                    <option value="none">None (in-mesh / mTLS)</option>
                  </select>
                </label>
                {r.authType === 'bearer' && (
                  <label style={{ flex: 1 }}>
                    <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
                      Token
                      {r.hasToken && <span style={{ color: 'var(--ok)', marginLeft: 8 }}>· stored</span>}
                    </div>
                    <input type="password" value={r.token}
                      onChange={e => patch(i, { token: e.target.value })}
                      placeholder={r.hasToken ? '(leave empty to keep stored value)' : 'paste ServiceAccount token…'}
                      style={{ width: '100%' }} />
                  </label>
                )}
              </div>
              {/* v0.10.128 — identity mapping (entity layer). Label name empty = no
                  matcher = one Thanos URL per cluster (the classic model). */}
              <div style={{ display: 'flex', gap: 10, marginBottom: 10 }}>
                <label style={{ width: 200 }}>
                  <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Thanos label name</div>
                  <input value={r.thanosLabelName}
                    onChange={e => patch(i, { thanosLabelName: e.target.value })}
                    placeholder='cluster  ·  empty = per-cluster URL'
                    style={{ width: '100%' }} />
                </label>
                <label style={{ flex: 1 }}>
                  <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Thanos label value</div>
                  <input value={r.thanosLabelValue}
                    onChange={e => patch(i, { thanosLabelValue: e.target.value })}
                    placeholder={r.name.trim() ? `empty = ${r.name.trim()}` : 'empty = cluster name'}
                    disabled={!r.thanosLabelName.trim()}
                    style={{ width: '100%' }} />
                </label>
                <label style={{ flex: 1 }}>
                  <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
                    Span cluster values
                    {r.thanosLabelSource && (
                      <span className={`badge ${r.thanosLabelSource === 'auto' ? 'b-info' : 'b-gray'}`} style={{ marginLeft: 6 }}
                        title={r.thanosLabelSource === 'auto' && r.thanosLabelDetectedAt ? `Thanos etiketi otomatik algılandı · ${fmtDateTime(new Date(r.thanosLabelDetectedAt))}` : 'Thanos etiketi elle girildi'}>
                        label: {r.thanosLabelSource}
                      </span>
                    )}
                  </div>
                  {/* v0.10.139 — bir kayıt birden çok span değeri taşır (virgülle); bir
                      değer aynı anda TEK kayda — çakışmayı sunucu reddeder ve bağlı kaydı söyler. */}
                  <input value={r.spanClusterValues}
                    onChange={e => patch(i, { spanClusterValues: e.target.value, spanClusterValue: e.target.value.split(',')[0]?.trim() ?? '' })}
                    placeholder={r.name.trim() ? `empty = ${r.name.trim()} · comma-separated` : 'empty = cluster name · comma-separated'}
                    style={{ width: '100%' }} />
                </label>
                <div style={{ display: 'flex', flexDirection: 'column', justifyContent: 'flex-end', gap: 4, minWidth: 140 }}>
                  <Button type="button" variant="ghost" size="sm" disabled={!r.id}
                    title={r.id ? 'Query count(kube_node_info) with this cluster\'s label matcher (saved settings)' : 'Save first'}
                    onClick={() => runProbe(r)}>
                    Test label
                  </Button>
                  <Button type="button" variant="secondary" size="sm" disabled={!r.id}
                    title={r.id ? 'Thanos external label\'ını enjeksiyonsuz sorguyla algıla ve kaydet (auto)' : 'Save first'}
                    onClick={() => runDetect(r)}>
                    Detect label
                  </Button>
                  {r.labelCheck && !r.labelCheck.ok && (
                <div style={{ fontSize: 12, color: 'var(--err)', marginTop: 4 }} title={r.labelCheck.checkedAt}>
                  ⚠ periyodik doğrulama: {r.labelCheck.error || 'etiket eşleşmiyor'} · {fmtDateTime(new Date(r.labelCheck.checkedAt))}
                </div>
              )}
              {detect[r.id || r.name.trim()] && (
                <div style={{ fontSize: 12, color: 'var(--text2)', marginTop: 4 }}>
                  {detect[r.id || r.name.trim()].text}
                  {detect[r.id || r.name.trim()].candidates && Object.entries(detect[r.id || r.name.trim()].candidates!).map(([label, vals]) => (
                    <span key={label} style={{ marginLeft: 8 }}>
                      {label}: {vals.slice(0, 8).map(v => (
                        <Button key={v} type="button" variant="ghost" size="xs" title={`Bu değeri seç: ${label}="${v}" (kaydetmeyi unutma)`}
                          onClick={() => patch(i, { thanosLabelName: label, thanosLabelValue: v })}>{v}</Button>
                      ))}
                    </span>
                  ))}
                </div>
              )}
              {probe[r.id || r.name.trim()] && (
                    <span style={{ fontSize: 11, color: 'var(--text2)' }}>{probe[r.id || r.name.trim()]}</span>
                  )}
                </div>
              </div>
              <div style={{ display: 'flex', gap: 10, alignItems: 'flex-end' }}>
                <label style={{ flex: 1 }}>
                  <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
                    Namespace filter (PromQL regex — cardinality shield)
                  </div>
                  <input value={r.namespaceFilter}
                    onChange={e => patch(i, { namespaceFilter: e.target.value })}
                    placeholder='^(app-|payments-)  ·  empty = all namespaces (top 500 pods)'
                    style={{ width: '100%' }} />
                </label>
                <label style={{ display: 'flex', alignItems: 'center', gap: 6, paddingBottom: 6, whiteSpace: 'nowrap' }}>
                  <input type="checkbox" checked={r.insecureSkipVerify}
                    onChange={e => patch(i, { insecureSkipVerify: e.target.checked })} />
                  <span style={{ fontSize: 12 }}>Skip TLS verify</span>
                </label>
              </div>
            </div>
          );
        })}

        {msg && (
          <div style={{ marginBottom: 12, fontSize: 12,
            color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)' }}>
            {msg.text}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8 }}>
          <Button type="button" variant="secondary"
            onClick={() => setRows(rs => [...rs, { ...EMPTY_ROW }])}>
            + Add cluster
          </Button>
          <Button type="submit" variant="primary" loading={busy}>
            Save all
          </Button>
        </div>
      </form>
      {/* v0.10.141 — span cluster değerleri: eşleşmemişleri bir kayda ata (teklik sunucuda). */}
      <SpanClusterValuesPanel clusters={rows.map(r => ({ id: r.id || undefined, name: r.name }))}
        onAssigned={(clusterId, values) => setRows(rs => rs.map(r => r.id === clusterId
          ? { ...r, spanClusterValues: values.join(', '), spanClusterValue: values[0] ?? '' }
          : r))} />
    </div>
  );
}
