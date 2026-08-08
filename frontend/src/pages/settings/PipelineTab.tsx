import { useEffect, useState, type FormEvent } from 'react';
import { Spinner, Empty } from '@/components/Spinner';
import { Modal, Button, Stack } from '@/components/ui';
import { api, type PipelineRule } from '@/lib/api';
import type { MetricExclusionRule } from '@/lib/types';
import { Field, FlashBox, humanize } from './shared';

// ── Pipeline tab (v0.5.263; logs + metrics v0.8.282) ────────────────────────
//
// Ingest-time drop / enrich / sample rules for spans, logs, AND metrics.
// Operator picks e.g. "service.name = frontend" and any matching record of
// the chosen signal gets dropped (or enriched / sampled) before the
// consumer sees it — no CH write. Per-signal drop counts are exposed on
// /admin/stats so the effect is observable without log-grepping.
export function PipelineTab() {
  const [rules, setRules] = useState<PipelineRule[] | null | undefined>(undefined);
  const [editing, setEditing] = useState<PipelineRule | null>(null);
  const [creating, setCreating] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const load = () => {
    setRules(undefined);
    api.listPipelineRules().then(r => setRules(r.rules ?? [])).catch(() => setRules(null));
  };
  useEffect(load, []);

  const toggle = async (r: PipelineRule) => {
    try {
      await api.upsertPipelineRule({ ...r, enabled: !r.enabled });
      setMsg({ kind: 'ok', text: `${r.name}: ${!r.enabled ? 'enabled' : 'disabled'}` });
      load();
    } catch (e) {
      setMsg({ kind: 'err', text: e instanceof Error ? e.message : String(e) });
    }
  };

  const remove = async (r: PipelineRule) => {
    if (!confirm(`Delete pipeline rule "${r.name}"? Records matching this rule will no longer be affected.`)) return;
    try {
      await api.deletePipelineRule(r.id);
      setMsg({ kind: 'ok', text: `Deleted "${r.name}"` });
      load();
    } catch (e) {
      setMsg({ kind: 'err', text: e instanceof Error ? e.message : String(e) });
    }
  };

  if (rules === undefined) return <Spinner />;
  if (rules === null) return <Empty icon="!" title="Failed to load pipeline rules" />;

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 12, marginBottom: 12 }}>
        <span style={{ fontSize: 12, color: 'var(--text2)', flex: 1 }}>
          Ingest-time rules evaluated <b>before</b> the store write. A "drop"
          rule that matches removes the record entirely — no CH write. Use
          for noisy health-check spans, DEBUG logs, high-cardinality debug
          metrics, or whole services you've decided to drop for cost.
          Choose the <b>signal</b> (spans / logs / metrics) each rule scopes to.
        </span>
        <Button onClick={() => setCreating(true)}>+ New rule</Button>
      </div>

      {msg && (
        <div style={{
          marginBottom: 10, padding: '6px 10px', borderRadius: 4, fontSize: 13,
          color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)',
          background: msg.kind === 'ok' ? 'rgba(34,197,94,0.08)' : 'rgba(220,38,38,0.08)',
          border: `1px solid ${msg.kind === 'ok' ? 'rgba(34,197,94,0.3)' : 'rgba(220,38,38,0.3)'}`,
        }}>{msg.text}</div>
      )}

      {rules.length === 0 ? (
        <Empty icon="⇉" title="No pipeline rules yet">
          Create one to drop noisy span traffic at ingest.
        </Empty>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Kind</th>
                <th>Signal</th>
                <th>Predicate</th>
                <th>Enabled</th>
                <th style={{ textAlign: 'right' }}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {rules.map(r => (
                <tr key={r.id}>
                  <td style={{ fontWeight: 600 }}>{r.name}</td>
                  <td>
                    <span className={r.kind === 'drop' ? 'badge b-err' : 'badge b-info'}>
                      {r.kind.toUpperCase()}
                    </span>
                  </td>
                  <td>
                    <code style={{ fontSize: 11 }}>{r.signal}</code>
                  </td>
                  <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 11 }}>
                    {r.when.key} <b>{r.when.op}</b>{' '}
                    <span style={{ color: 'var(--text2)' }}>"{r.when.value}"</span>
                    {r.kind === 'enrich' && r.setAttributes && Object.entries(r.setAttributes).map(([k, v]) => (
                      <span key={k} style={{ marginLeft: 8, color: 'var(--accent2)' }}>
                        → {k}=<b>"{v}"</b>
                      </span>
                    ))}
                    {r.kind === 'sample' && r.rate != null && (
                      <span style={{ marginLeft: 8, color: 'var(--accent2)' }}>
                        keep <b>{(r.rate * 100).toFixed(1)}%</b>
                      </span>
                    )}
                  </td>
                  <td>
                    <input type="checkbox" checked={r.enabled} onChange={() => toggle(r)} />
                  </td>
                  <td style={{ textAlign: 'right' }}>
                    <Button variant="secondary" size="sm" onClick={() => setEditing(r)} style={{ marginRight: 6 }}>
                      Edit
                    </Button>
                    <Button variant="secondary" size="sm" onClick={() => remove(r)}>
                      Delete
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {(creating || editing) && (
        <PipelineRuleModal
          existing={editing}
          onClose={() => { setCreating(false); setEditing(null); }}
          onSaved={() => { setCreating(false); setEditing(null); load(); }}
        />
      )}

      {/* v0.9.797 — metrik route dışlamaları. AYNI SEKMEDE, çünkü
          operatör buraya tek bir soruyla geliyor: "bu gürültü ne olsun?"
          Üstteki tablo ham ingest kurallarını yönetiyor; bu kart onun
          metrik-grafik yüzeyindeki hâli — ve dropAtIngest işaretlendiğinde
          ÜSTTEKİ tabloda görünen türetilmiş bir kural yazıyor (tek drop
          motoru). Ayrı bir sekmeye koysaydık iki liste birbirinden
          habersiz görünürdü. */}
      <div style={{ marginTop: 28, paddingTop: 20, borderTop: '1px solid var(--border)' }}>
        <MetricExclusionsSection onChanged={load} />
      </div>
    </div>
  );
}

// ── Metrik dışlamaları (v0.9.797) ───────────────────────────────────
//
// İSTEK: healthcheck / probe route'ları metrik grafiklerinden düşsün.
// Yüksek hacimli ve tekdüze oldukları için route kırılımlı panelin ilk
// 10 serisini yiyorlar ve GRUPSUZ toplamda ortalamayı aşağı çekiyorlar —
// "Toplam" çizgisi sağlıklı görünürken gerçek endpoint'ler yavaşlıyor
// olabiliyor.
//
// İKİ KADEME ve farkları operatöre AÇIK YAZILIYOR: okuma filtresi geri
// alınabilir (kural silinince geçmiş veri aynen döner), ingest drop'u
// geri alınamaz (yazılmayan datapoint yok).
function MetricExclusionsSection({ onChanged }: { onChanged: () => void }) {
  const [rules, setRules] = useState<MetricExclusionRule[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [flash, setFlash] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    api.getMetricExclusions()
      .then(c => setRules(c.rules ?? []))
      .catch(err => setFlash({ kind: 'err', text: humanize(err) }));
  }, []);

  const save = async () => {
    if (!rules) return;
    setBusy(true); setFlash(null);
    try {
      const saved = await api.putMetricExclusions({ rules });
      setRules(saved.rules ?? []);
      setFlash({
        kind: 'ok',
        text: 'Kaydedildi — grafikler bir sonraki okumada (≤30 sn) filtreli gelir.',
      });
      // İngest ikizleri değişmiş olabilir: üstteki Pipeline listesini
      // tazele ki operatör türetilen kuralı GÖRSÜN.
      onChanged();
    } catch (err) {
      setFlash({ kind: 'err', text: humanize(err) });
    } finally {
      setBusy(false);
    }
  };

  const patch = (i: number, p: Partial<MetricExclusionRule>) =>
    setRules(rs => (rs ?? []).map((r, j) => (j === i ? { ...r, ...p } : r)));

  if (rules === null) {
    return flash ? <FlashBox kind={flash.kind}>{flash.text}</FlashBox> : <Spinner />;
  }

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 12, marginBottom: 8 }}>
        <h2 style={{ fontSize: 14, fontWeight: 600, flex: 1 }}>Metrik dışlamaları</h2>
        <Button variant="secondary" size="sm"
          onClick={() => setRules([...(rules ?? []), { metric: '*', pattern: '', dropAtIngest: false }])}>
          + Kural
        </Button>
      </div>
      <p style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 14, lineHeight: 1.55 }}>
        Healthcheck route&apos;larını metrik grafiklerinden düşürmek için. Kural
        eklendiği anda <b>geçmiş dahil</b> tüm metrik okumaları filtrelenir;
        kural silinince veri aynen geri gelir &mdash; hiçbir şey kaybolmaz.
        Desen <b>RE2</b> ve ankorsuz: <code>/health</code> yolun herhangi bir
        yerinde eşleşir, tam eşleşme için <code>^...$</code> yazın. Metrik alanı
        tam ad ya da <code>*</code> (her metrik). Kapsam yalnız <b>metrik
        datapoint&apos;leri</b>: trace&apos;ler, servis RED&apos;i ve hata oranı
        span türevli, onlara dokunulmaz.
      </p>

      {rules.length === 0 ? (
        <Empty icon="⊘" title="Dışlama kuralı yok">
          Healthcheck route&apos;larını grafiklerden düşürmek için bir kural ekleyin.
        </Empty>
      ) : (
        <div style={{ display: 'grid', gap: 10 }}>
          {rules.map((r, i) => (
            <div key={i} style={{
              display: 'flex', gap: 10, alignItems: 'flex-start', flexWrap: 'wrap',
              padding: '10px 12px', border: '1px solid var(--border)', borderRadius: 6,
              background: 'var(--bg2)',
            }}>
              <div style={{ flex: '1 1 220px' }}>
                <Field label="Metrik">
                  <input value={r.metric} placeholder="* veya http.server.duration"
                    onChange={e => patch(i, { metric: e.target.value })} style={{ width: '100%' }} />
                </Field>
              </div>
              <div style={{ flex: '2 1 280px' }}>
                <Field label="http.route deseni (RE2)">
                  <input value={r.pattern} placeholder="^/health"
                    onChange={e => patch(i, { pattern: e.target.value })} style={{ width: '100%' }} />
                </Field>
              </div>
              <div style={{ flex: '1 1 260px', paddingTop: 18 }}>
                <label style={{ display: 'flex', alignItems: 'flex-start', gap: 8, fontSize: 12.5 }}>
                  <input type="checkbox" checked={!!r.dropAtIngest}
                    onChange={e => patch(i, { dropAtIngest: e.target.checked })} />
                  <span>
                    ingest&apos;te düşür
                    <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>
                      Pipeline kuralı olarak uygulanır &mdash; yukarıdaki listede de
                      görünür. Yazılmayan datapoint <b>geri gelmez</b>.
                    </div>
                  </span>
                </label>
              </div>
              <div style={{ paddingTop: 18 }}>
                <Button variant="secondary" size="sm"
                  onClick={() => setRules(rules.filter((_, j) => j !== i))}>Sil</Button>
              </div>
            </div>
          ))}
        </div>
      )}

      <div style={{ marginTop: 16, display: 'flex', gap: 8, alignItems: 'center' }}>
        <Button variant="primary" onClick={save} disabled={busy}>
          {busy ? 'Kaydediliyor…' : 'Kaydet'}
        </Button>
        {flash && <FlashBox kind={flash.kind}>{flash.text}</FlashBox>}
      </div>
    </div>
  );
}

function PipelineRuleModal({ existing, onClose, onSaved }: {
  existing: PipelineRule | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [name,    setName]    = useState(existing?.name ?? '');
  const [kind,    setKind]    = useState<PipelineRule['kind']>(existing?.kind ?? 'drop');
  const [signal,  setSignal]  = useState<PipelineRule['signal']>(existing?.signal ?? 'spans');
  const [enabled, setEnabled] = useState(existing?.enabled ?? true);
  const [whenKey, setWhenKey] = useState(existing?.when.key ?? 'service.name');
  const [whenOp,  setWhenOp]  = useState<PipelineRule['when']['op']>(existing?.when.op ?? '=');
  const [whenVal, setWhenVal] = useState(existing?.when.value ?? '');
  // v0.5.270 — enrich + sample fields. Enrich uses a single
  // key/value pair for the MVP (multi-attr could come later
  // via a chip list — start narrow).
  const [enrichKey, setEnrichKey] = useState<string>(() => {
    const m = existing?.setAttributes ?? {};
    return Object.keys(m)[0] ?? '';
  });
  const [enrichVal, setEnrichVal] = useState<string>(() => {
    const m = existing?.setAttributes ?? {};
    const k = Object.keys(m)[0];
    return k ? m[k] : '';
  });
  const [rate, setRate] = useState<number>(existing?.rate ?? 0.1);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      const body: PipelineRule = {
        id: existing?.id ?? '',
        name: name.trim(),
        kind, signal, enabled,
        when: { key: whenKey.trim(), op: whenOp, value: whenVal.trim() },
      };
      if (kind === 'enrich') {
        body.setAttributes = enrichKey.trim()
          ? { [enrichKey.trim()]: enrichVal.trim() }
          : {};
      }
      if (kind === 'sample') {
        body.rate = rate;
      }
      await api.upsertPipelineRule(body);
      onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={existing ? `Edit rule — ${existing.name}` : 'New pipeline rule'}
      size="md"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button type="submit" form="pipeline-form" loading={busy}>Save</Button>
        </>
      }>
      <form id="pipeline-form" onSubmit={submit}>
        <Stack gap={3}>
          <div>
            <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Rule name</label>
            <input value={name} onChange={e => setName(e.target.value)} required
              placeholder="e.g. drop frontend health-checks"
              style={{ width: '100%' }} />
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
            <div>
              <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Action</label>
              <select value={kind} onChange={e => setKind(e.target.value as PipelineRule['kind'])}
                style={{ width: '100%' }}>
                <option value="drop">Drop — discard the matching signal</option>
                <option value="enrich">Enrich — set a resource attribute</option>
                <option value="sample">Sample — keep at probability</option>
              </select>
            </div>
            <div>
              <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Signal</label>
              <select value={signal} onChange={e => setSignal(e.target.value as PipelineRule['signal'])}
                style={{ width: '100%' }}>
                <option value="spans">spans</option>
                <option value="logs">logs</option>
                <option value="metrics">metrics</option>
              </select>
            </div>
          </div>
          <div>
            <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
              When (predicate)
            </label>
            <div style={{ display: 'grid', gridTemplateColumns: '2fr 1fr 2fr', gap: 8 }}>
              <input value={whenKey} onChange={e => setWhenKey(e.target.value)} required
                placeholder={
                  signal === 'logs' ? 'service.name | severity_text | body | attr.X'
                  : signal === 'metrics' ? 'metric | unit | instrument | service.name'
                  : 'service.name | name | kind | attr.X | resource.X'}
                style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }} />
              <select value={whenOp} onChange={e => setWhenOp(e.target.value as PipelineRule['when']['op'])}
                style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>
                <option value="=">=</option>
                <option value="!=">!=</option>
                <option value="contains">contains</option>
                <option value="startsWith">startsWith</option>
                <option value="endsWith">endsWith</option>
              </select>
              <input value={whenVal} onChange={e => setWhenVal(e.target.value)} required
                placeholder="value"
                style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }} />
            </div>
            <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
              {signal === 'logs' ? (
                <>Well-known log fields branch directly: <code>service.name</code>,
                {' '}<code>severity_text</code>, <code>severity_number</code>,
                {' '}<code>body</code>, <code>host.name</code>, <code>trace_id</code>.</>
              ) : signal === 'metrics' ? (
                <>Well-known metric fields branch directly: <code>metric</code>,
                {' '}<code>instrument</code>, <code>unit</code>, <code>service.name</code>,
                {' '}<code>host.name</code>.</>
              ) : (
                <>Well-known span fields branch directly: <code>service.name</code>,
                {' '}<code>name</code>, <code>kind</code>, <code>status_code</code>.</>
              )}
              {' '}Custom attributes via <code>attr.foo</code> / <code>resource.foo</code> prefix.
            </div>
          </div>

          {/* v0.5.270 — enrich-only fields */}
          {kind === 'enrich' && (
            <div>
              <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
                Set resource attribute
              </label>
              <div style={{ display: 'grid', gridTemplateColumns: '2fr 3fr', gap: 8 }}>
                <input value={enrichKey} onChange={e => setEnrichKey(e.target.value)}
                  placeholder="e.g. team, region, cluster"
                  style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }} />
                <input value={enrichVal} onChange={e => setEnrichVal(e.target.value)}
                  placeholder="value"
                  style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }} />
              </div>
              <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                Sets a resource attribute on every matching record. Existing keys are
                overridden. Multi-attribute support coming later — start with one.
              </div>
            </div>
          )}

          {/* v0.5.270 — sample-only fields */}
          {kind === 'sample' && (
            <div>
              <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
                Keep rate ({(rate * 100).toFixed(1)}%)
              </label>
              <input type="range" min={0} max={1} step={0.01}
                value={rate} onChange={e => setRate(Number(e.target.value))}
                style={{ width: '100%' }} />
              <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                Probability of keeping each matching record. 1.0 = no-op; 0.0 = use a
                drop rule instead.
                {signal === 'metrics' && (
                  <span style={{ color: 'var(--warn, var(--err))' }}>
                    {' '}Note: sampling metrics makes sums / counts / histograms an
                    estimate — prefer drop for whole noisy series.
                  </span>
                )}
              </div>
            </div>
          )}

          <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13 }}>
            <input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)} />
            Enabled
          </label>
          {error && (
            <div style={{
              color: 'var(--err)', fontSize: 12,
              padding: '4px 8px', background: 'rgba(220,38,38,0.08)',
              border: '1px solid rgba(220,38,38,0.3)', borderRadius: 4,
            }}>{error}</div>
          )}
        </Stack>
      </form>
    </Modal>
  );
}
