import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { Spinner } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
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
  name: string;
  url: string;
  authType: ThanosAuthType;
  token: string;    // only ever holds a NEW token; '' = keep stored
  hasToken: boolean;
  namespaceFilter: string;
  insecureSkipVerify: boolean;
  enabled: boolean;
}

function fromSnapshot(c: ThanosClusterSnapshot): EditRow {
  return {
    name: c.name, url: c.url,
    authType: (c.authType || 'none') as ThanosAuthType,
    token: '', hasToken: c.hasToken,
    namespaceFilter: c.namespaceFilter || '',
    insecureSkipVerify: !!c.insecureSkipVerify,
    enabled: c.enabled,
  };
}

const EMPTY_ROW: EditRow = {
  name: '', url: '', authType: 'bearer', token: '', hasToken: false,
  namespaceFilter: '', insecureSkipVerify: false, enabled: true,
};

export function ClustersTab() {
  const [loaded, setLoaded] = useState(false);
  const [rows, setRows] = useState<EditRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  // Observed cluster names from telemetry (last 24h) — the
  // suggestion source for the join-key warning. timeRange math
  // stays inside useMemo (v0.5.184 rule).
  const [fromNs, toNs] = useMemo(() => {
    const now = Date.now() * 1e6;
    return [now - 24 * 3600 * 1e9, now];
  }, []);
  const observedQ = useClusters(fromNs, toNs);
  const observed = useMemo(() => new Set(observedQ.data ?? []), [observedQ.data]);

  useEffect(() => {
    api.getThanosSettings().then(s => {
      setRows((s.clusters ?? []).map(fromSnapshot));
      setLoaded(true);
    }).catch(() => setLoaded(true));
  }, []);

  const patch = (i: number, p: Partial<EditRow>) =>
    setRows(rs => rs.map((r, j) => (j === i ? { ...r, ...p } : r)));

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const next = await api.putThanosSettings({
        clusters: rows.map(r => ({
          name: r.name.trim(), url: r.url.trim(), authType: r.authType,
          token: r.token, // '' keeps stored (server contract, name-matched)
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

  if (!loaded) return <Spinner />;

  return (
    <div style={{ maxWidth: 760 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Remote clusters (Thanos)</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Each entry points at an OpenShift cluster's Thanos Querier route.
        The <strong>/clusters</strong> page pulls per-pod CPU + memory from
        every enabled entry; the cluster <strong>name</strong> must match the
        <code style={{ background: 'var(--bg0)', padding: '1px 5px', borderRadius: 3, margin: '0 4px' }}>k8s.cluster.name</code>
        value the cluster's telemetry reports so service pages can pivot into it.
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
          const nameKnown = r.name.trim() === '' || observed.size === 0 || observed.has(r.name.trim());
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
                    {!nameKnown && (
                      <span className="badge b-warn" style={{ marginLeft: 8 }}
                        title="Bu ad son 24 saatin telemetrisinde görülmedi — Thanos verisi servis sayfalarıyla eşleşmeyecek. Cluster telemetri göndermeye başlayınca uyarı kalkar.">
                        telemetride görülmüyor
                      </span>
                    )}
                  </div>
                  <input value={r.name} list="observed-clusters" required
                    onChange={e => patch(i, { name: e.target.value })}
                    placeholder="prod-ist" style={{ width: '100%' }} />
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

        <datalist id="observed-clusters">
          {[...observed].sort().map(c => <option key={c} value={c} />)}
        </datalist>

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
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : 'Save all'}
          </Button>
        </div>
      </form>
    </div>
  );
}
