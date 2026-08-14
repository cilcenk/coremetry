import { useEffect, useState, type FormEvent } from 'react';
import { Spinner, Empty } from '@/components/Spinner';
import { Modal, Button, Stack } from '@/components/ui';
import { api, type PipelineRule } from '@/lib/api';
import type { MetricExclusionRule } from '@/lib/types';
import { Field, FlashBox, humanize } from './shared';
import { buildPipelineRuleBody, describeCondition } from './pipelineRuleBody';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';

// v0.9.875 (tutarlılık denetimi MT6) — kural tablosu paylaşılan primitife.
// v0.9.797'den beri türetilmiş-metrik kuralları da buraya yazılıyor ve liste
// SINIRSIZ büyüyor; sıralama yoktu.
//
// Predicate kolonu birleşik bir gösterim (when + AND'ler + setAttributes /
// rate). Sıralama görüneni takip etsin diye anahtar+op+değer üzerinden
// yapılıyor — aynı attr'ı hedefleyen kurallar yan yana geliyor, ki bu
// listedeki en sık soru ("bu key'e dokunan kaç kural var").
const RULE_COLS: DataTableColumn<PipelineRule>[] = [
  { id: 'name',      label: 'Name',      sortValue: r => r.name,   naturalDir: 'asc', width: 200 },
  { id: 'kind',      label: 'Kind',      sortValue: r => r.kind,   naturalDir: 'asc', width: 110 },
  { id: 'signal',    label: 'Signal',    sortValue: r => r.signal, naturalDir: 'asc', width: 110 },
  { id: 'predicate', label: 'Predicate',
    sortValue: r => `${r.when.key} ${r.when.op} ${r.when.value}`, naturalDir: 'asc', flex: true },
  { id: 'enabled',   label: 'Enabled',   sortValue: r => (r.enabled ? 1 : 0), width: 95 },
];

// ── Pipeline tab (v0.5.263; logs + metrics v0.8.282) ────────────────────────
//
// Ingest-time drop / enrich / sample rules for spans, logs, AND metrics.
// Operator picks e.g. "service.name = frontend" and any matching record of
// the chosen signal gets dropped (or enriched / sampled) before the
// consumer sees it — no CH write. Per-signal drop counts are exposed on
// /admin/stats so the effect is observable without log-grepping.
// SPAN_ROUTE_PREFILL — "Metrik dışlamaları" kartındaki span köprüsünün
// açtığı yeni-kural taslağı (v0.9.802). Kart RE2 desenleriyle çalıştığı
// için operatör aynı dilde devam etsin diye `=~` seçili gelir; anahtar
// `http.route`, ki v0.9.802'den beri TÜRETİLMİŞ route alanından eşleşir
// (url.path basan servisler dahil).
const SPAN_ROUTE_PREFILL: Partial<PipelineRule> = {
  kind: 'drop',
  signal: 'spans',
  when: { key: 'http.route', op: '=~', value: '' },
};

export function PipelineTab() {
  const [rules, setRules] = useState<PipelineRule[] | null | undefined>(undefined);
  const [editing, setEditing] = useState<PipelineRule | null>(null);
  const [creating, setCreating] = useState(false);
  // spanPrefill — creating ile birlikte yaşar; hangi taslakla açıldığını
  // söyler ("+ New rule" boş, dışlama kartının köprüsü span/route dolu).
  const [spanPrefill, setSpanPrefill] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const dt = useDataTable<PipelineRule>({
    storageKey: 'settings-pipeline-rules', columns: RULE_COLS, rows: rules ?? [],
    initialSort: { id: 'name', dir: 'asc' },
  });

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
        <Button variant="primary" onClick={() => setCreating(true)}>+ New rule</Button>
      </div>

      {msg && (
        <div style={{
          marginBottom: 10, padding: '6px 10px', borderRadius: 4, fontSize: 13,
          color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)',
          background: msg.kind === 'ok' ? 'color-mix(in srgb, var(--ok) 8%, transparent)' : 'color-mix(in srgb, var(--err) 8%, transparent)',
          border: `1px solid ${msg.kind === 'ok' ? 'color-mix(in srgb, var(--ok) 30%, transparent)' : 'color-mix(in srgb, var(--err) 30%, transparent)'}`,
        }}>{msg.text}</div>
      )}

      {rules.length === 0 ? (
        <Empty icon="⇉" title="No pipeline rules yet">
          Create one to drop noisy span traffic at ingest.
        </Empty>
      ) : (
        <div className="table-wrap">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} trailing={[160]} />
            <DataTableHead dt={dt} trailing={<th style={{ textAlign: 'right' }}>Actions</th>} />
            <tbody>
              {dt.sortedRows.map(r => (
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
                    {/* v0.9.803 — EK koşullar listede de görünür. Yalnız
                        `when`'i basmak, tek-metrik kısıtlı türetilmiş bir
                        kuralı "her metrik" gibi okutuyordu. */}
                    {(r.and ?? []).map((c, i) => (
                      <span key={i}>
                        {' '}<b style={{ color: 'var(--text3)' }}>AND</b>{' '}
                        {c.key} <b>{c.op}</b>{' '}
                        <span style={{ color: 'var(--text2)' }}>"{c.value}"</span>
                      </span>
                    ))}
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
          prefill={spanPrefill ? SPAN_ROUTE_PREFILL : null}
          onClose={() => { setCreating(false); setEditing(null); setSpanPrefill(false); }}
          onSaved={() => { setCreating(false); setEditing(null); setSpanPrefill(false); load(); }}
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
        <MetricExclusionsSection
          onChanged={load}
          onSpanRule={() => { setSpanPrefill(true); setCreating(true); }}
        />
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
function MetricExclusionsSection({ onChanged, onSpanRule }: {
  onChanged: () => void;
  onSpanRule: () => void;
}) {
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

      {/* v0.9.802 — SPAN KÖPRÜSÜ. Kart yalnız metrik datapoint'lerini
          kapsıyor ve operatör buraya "healthcheck gürültüsü gitsin"
          diye geliyor: span tarafının nerede olduğunu SÖYLEMEZSEK kartı
          yarım bir çözüm sanıp span kuralını hiç kurmuyor.

          Karta span kural TİPİ EKLENMEDİ (yalnız yönlendirme): iki
          motorun sahibi pipeline, ve aynı kuralı iki yerden yazılabilir
          yapmak UI çoğaltması olurdu. Link yukarıdaki tabloya taslak
          açar — tek sahip, tek liste. */}
      <div style={{
        display: 'flex', gap: 10, alignItems: 'baseline', flexWrap: 'wrap',
        marginBottom: 14, padding: '8px 12px', borderRadius: 6,
        border: '1px solid var(--border)', background: 'var(--bg2)',
        fontSize: 12, color: 'var(--text2)', lineHeight: 1.55,
      }}>
        <span style={{ flex: '1 1 340px' }}>
          <b>Span&apos;ler için:</b> yukarıdaki Pipeline kuralları tablosunda
          {' '}<code>kind=drop</code> bir kural (<code>http.route</code> ya da span
          adı eşleşmesi). Span tarafında OKUMA filtresi <b>yok</b>: dar rollup ve
          {' '}<code>operation_summary_5m</code> route bilmiyor, okuma filtresi
          entry-RED&apos;i her istekte ham <code>spans</code> taramasına düşürürdü.
          Bu yüzden span dışlaması <b>yalnız ingest&apos;te</b> uygulanır &mdash;
          geçmiş veriyi etkilemez, ileriye dönük yazılmaz.
        </span>
        <Button variant="secondary" size="sm" onClick={onSpanRule}>
          + Span drop kuralı
        </Button>
      </div>

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
        <Button variant="primary" onClick={save} loading={busy}>
          Kaydet
        </Button>
        {flash && <FlashBox kind={flash.kind}>{flash.text}</FlashBox>}
      </div>
    </div>
  );
}

// prefill — YALNIZ yeni kuralda (existing === null) okunur; var olan bir
// kuralı düzenlerken taslağın kaydı ezmesi olurdu (v0.9.802).
function PipelineRuleModal({ existing, prefill, onClose, onSaved }: {
  existing: PipelineRule | null;
  prefill?: Partial<PipelineRule> | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const draft = existing ? null : prefill ?? null;
  const [name,    setName]    = useState(existing?.name ?? '');
  const [kind,    setKind]    = useState<PipelineRule['kind']>(existing?.kind ?? draft?.kind ?? 'drop');
  const [signal,  setSignal]  = useState<PipelineRule['signal']>(existing?.signal ?? draft?.signal ?? 'spans');
  const [enabled, setEnabled] = useState(existing?.enabled ?? true);
  const [whenKey, setWhenKey] = useState(existing?.when.key ?? draft?.when?.key ?? 'service.name');
  const [whenOp,  setWhenOp]  = useState<PipelineRule['when']['op']>(existing?.when.op ?? draft?.when?.op ?? '=');
  const [whenVal, setWhenVal] = useState(existing?.when.value ?? draft?.when?.value ?? '');
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

  // v0.9.802 — bozuk RE2 deseni backend'de 400 ile reddediliyor
  // (validateCondition, v0.9.797). O hatayı formun DİBİNDEKİ genel
  // kutuya bırakmak, operatörü hatayla düzelteceği alan arasında
  // gezdirir; `=~` seçiliyken desen hatası alanın ALTINA düşer ve
  // genel kutuda TEKRAR gösterilmez.
  const patternError =
    error && whenOp === '=~' && /RE2|pattern|desen/i.test(error) ? error : null;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      // v0.9.803 — gövde SAF fonksiyondan (pipelineRuleBody.ts, tablo-testli).
      // Satır içinde kurulduğunda formun bilmediği alanlar sessizce
      // düşüyordu; `and` kaybı tek-metrik dışlamasını bütün metriklere
      // yayan geri alınamaz bir veri kaybıydı.
      const body = buildPipelineRuleBody(existing, {
        name, kind, signal, enabled,
        whenKey, whenOp, whenValue: whenVal,
        enrichKey, enrichValue: enrichVal, rate,
      });
      await api.upsertPipelineRule(body);
      onSaved();
    } catch (err) {
      // v0.9.802 — humanize: "HTTP 400: {"error":"invalid RE2 pattern …"}"
      // yerine sade mesaj. Desen hatasının alan altında okunabilir
      // görünmesi buna bağlı.
      setError(humanize(err));
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
          <Button variant="primary" type="submit" form="pipeline-form" loading={busy}>Save</Button>
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
          <div className="grid-2" style={{ display: 'grid', gap: 10 }}>
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
                {/* v0.9.802 — motor `=~` destekliyordu (v0.9.797, RE2 +
                    validateCondition) ama dropdown sunmuyordu; operatör
                    contains ile idare ediyordu. */}
                <option value="=~">=~ (regex)</option>
              </select>
              <input value={whenVal} onChange={e => setWhenVal(e.target.value)} required
                placeholder={whenOp === '=~' ? 'RE2 pattern, e.g. ^/health' : 'value'}
                style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }} />
            </div>
            {/* v0.9.802 — bozuk desen backend'de 400 döner (validateCondition).
                O hata genel kutuda değil DESENİN ALTINDA görünsün: operatör
                düzelteceği alana bakıyor. */}
            {patternError && (
              <div style={{ fontSize: 11, color: 'var(--err)', marginTop: 4 }}>{patternError}</div>
            )}
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
                {' '}<code>name</code>, <code>kind</code>, <code>status_code</code>,
                {' '}<code>http.route</code>.</>
              )}
              {' '}Custom attributes via <code>attr.foo</code> / <code>resource.foo</code> prefix.
              {' '}<code>=~</code> is an <b>unanchored RE2</b> pattern — <code>/health</code>{' '}
              matches anywhere in the value; use <code>^…$</code> for a full match.
              {/* v0.9.802 — türetilmiş route + trace bütünlüğü. İkisi de
                  spans'e özgü, o yüzden yalnız bu dalda. */}
              {signal === 'spans' && (
                <>
                  <div style={{ marginTop: 6 }}>
                    <code>http.route</code> matches the <b>derived</b> route, not the raw
                    attribute: services that only emit <code>url.path</code> (no route
                    templating) still match, via the id-stripped template
                    (<code>/api/accounts/12345</code> → <code>/api/accounts/:id</code>).
                    Use <code>attr.http.route</code> if you need the raw attribute.
                  </div>
                  <div style={{ marginTop: 6 }}>
                    Trace integrity: a drop rule removes <b>the matching span only</b> —
                    the rest of its trace is still written, so a child can be left without
                    its parent. Match on the entry span of a self-contained trace
                    (health checks / probes) rather than mid-trace spans.
                  </div>
                </>
              )}
            </div>
          </div>

          {/* v0.9.803 — EK KOŞULLAR, salt-okunur.
              Form bunları düzenlemiyor ama kayıtta AYNEN geri yazılıyor
              (buildPipelineRuleBody). Görünür olmaları şart: bir operatör
              "when: http.route =~ ^/health" satırını görüp kuralı "her
              route" sanmamalı — kısıtın ikinci yarısı burada. */}
          {existing?.and && existing.and.length > 0 && (
            <div>
              <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
                Ek koşullar (AND) — salt okunur
              </label>
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
                {existing.and.map((c, i) => (
                  <code key={i} style={{
                    fontSize: 11, padding: '3px 8px', borderRadius: 4,
                    border: '1px solid var(--border)', background: 'var(--bg2)',
                  }}>{describeCondition(c)}</code>
                ))}
              </div>
              <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                Kural yalnız <b>hepsi</b> sağlandığında eşleşir. Bu koşullar bu
                formda düzenlenmez ve kaydederken <b>olduğu gibi korunur</b> —
                genelde türetilmiş bir kuralın (<code>metric-excl-…</code>) metrik
                kısıtıdır. Kaldırmak için kuralı üreten kaydı düzenleyin.
              </div>
            </div>
          )}

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
          {error && !patternError && (
            <div style={{
              color: 'var(--err)', fontSize: 12,
              padding: '4px 8px', background: 'color-mix(in srgb, var(--err) 8%, transparent)',
              border: '1px solid color-mix(in srgb, var(--err) 30%, transparent)', borderRadius: 4,
            }}>{error}</div>
          )}
        </Stack>
      </form>
    </Modal>
  );
}
