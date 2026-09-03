// TraceFacetsTab.tsx — v0.10.303 (trace arama Dilim 2b, Datadog "facets").
//
// Yaygın-değerli attribute'lar (channel, tenant…) için terfi kolonu +
// set(0) indeksi: Dilim 1'in hash indeksi nadir değerde budar, yaygın
// değerde budamaz — orası burası. Kayıt system_settings'te; app-managed
// kurulumda DDL boot/PUT'ta kendisi, dış Distributed prod'da üretilen SQL
// elle (0013 emsali). Durum sütunları BU pod'un gördüğüdür ("kolon VAR ≠
// DOLU": routed = probe doğruladı, filtre kolona gidiyor).
import { useState } from 'react';
import { Button } from '@/components/ui/Button';
import { Field } from '@/components/ui/Field';
import { Stack, Row } from '@/components/ui';
import { Badge } from '@/components/ui/Badge';
import { Spinner, Empty } from '@/components/Spinner';
import { CopyButton } from '@/components/CopyButton';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { api } from '@/lib/api';
import type { TraceFacet, TraceFacetStatus, TraceFacetsResponse } from '@/lib/types';
import { FlashBox, humanize, useSettingsLoad, SettingsLoadError } from './shared';

type FacetRow = TraceFacet & { status?: TraceFacetStatus };

const COLS: DataTableColumn<FacetRow>[] = [
  { id: 'key', label: 'Attribute', sortValue: r => r.key, naturalDir: 'asc', width: 180 },
  { id: 'spellings', label: 'Yazımlar', sortValue: r => (r.spellings ?? []).join(','), naturalDir: 'asc', flex: true },
  { id: 'scope', label: 'Kapsam', sortValue: r => r.scope ?? 'span', naturalDir: 'asc', width: 90 },
  { id: 'type', label: 'Tip', sortValue: r => r.type ?? 'lc', naturalDir: 'asc', width: 70 },
  { id: 'column', label: 'Kolon', sortValue: r => r.status?.column ?? '', naturalDir: 'asc', width: 170 },
  { id: 'state', label: 'Durum (bu pod)', sortValue: r => (r.status?.routed ? 2 : r.status?.columnExists ? 1 : 0), numeric: true, width: 150 },
  { id: 'act', label: '', sortValue: () => 0, width: 60 },
];

export function facetStateLabel(st?: TraceFacetStatus): { text: string; tone: 'success' | 'warning' | 'neutral' } {
  if (!st) return { text: 'kaydedilmedi', tone: 'neutral' };
  if (st.routed) return { text: 'kolona yönleniyor', tone: 'success' };
  if (st.columnExists) return { text: st.indexExists ? 'kolon var · doğrulanmadı' : 'kolon var · indeks yok', tone: 'warning' };
  return { text: 'kolon yok', tone: 'warning' };
}

export function TraceFacetsTab() {
  const [facets, setFacets] = useState<TraceFacet[]>([]);
  const [status, setStatus] = useState<TraceFacetStatus[]>([]);
  const [bootManaged, setBootManaged] = useState(true);
  const [sql, setSql] = useState('');
  const [note, setNote] = useState('');
  const [showSql, setShowSql] = useState(false);
  const [draft, setDraft] = useState<{ key: string; spellings: string; scope: 'span' | 'resource'; type: 'lc' | 'string' }>({ key: '', spellings: '', scope: 'span', type: 'lc' });
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const apply = (d: TraceFacetsResponse) => {
    setFacets(d.facets ?? []); setStatus(d.status ?? []); setBootManaged(!!d.bootManaged); setSql(d.migrationSql ?? ''); setNote(d.note ?? '');
  };
  const { loaded, error: loadErr, retry } = useSettingsLoad(() => api.getTraceFacets(), apply);

  const rows: FacetRow[] = facets.map(f => ({ ...f, status: status.find(s => s.key === f.key) }));
  const dt = useDataTable<FacetRow>({ storageKey: 'trace-facets', columns: COLS, rows, initialSort: { id: 'key', dir: 'asc' } });

  const save = async (next: TraceFacet[]) => {
    setBusy(true); setMsg(null);
    try {
      const d = await api.putTraceFacets(next);
      apply(d);
      setMsg({ kind: 'ok', text: d.note ?? 'Kaydedildi.' });
    } catch (e) {
      setMsg({ kind: 'err', text: humanize(e) });
    } finally { setBusy(false); }
  };
  const add = () => {
    const key = draft.key.trim();
    if (!key) { setMsg({ kind: 'err', text: 'Attribute anahtarı boş.' }); return; }
    const spellings = draft.spellings.split(',').map(s => s.trim()).filter(Boolean);
    void save([...facets, { key, spellings: spellings.length ? spellings : undefined, scope: draft.scope, type: draft.type }]);
    setDraft({ key: '', spellings: '', scope: 'span', type: 'lc' });
  };
  const remove = (key: string) => void save(facets.filter(f => f.key !== key));

  if (loadErr) return <SettingsLoadError error={loadErr} onRetry={retry} />;
  if (!loaded) return <Spinner />;
  return (
    <Stack gap={4}>
      <p className="field-hint">
        Yaygın değerli attribute'lar (channel, tenant, region…) için terfi kolonu + <code>set(0)</code> indeksi — Datadog "facet" karşılığı.
        Nadir değerler (id, txn_ref) zaten hash indeksiyle (attr_kvh) hızlı; buraya yalnız her span'de görülen anahtarları ekleyin.
        {bootManaged
          ? ' Bu kurulum uygulama yönetimli: kolon/indeks DDL\'i kaydetmeyle gönderilir (küme kipinde ertelenebilir); filtreler, probe kolonu DOLU görünce kolona yönlenir.'
          : ' Bu kurulum dış Distributed ClickHouse: üretilen SQL\'i elle koşun, sonra pod\'ları yeniden başlatın.'}
      </p>
      {msg && <FlashBox kind={msg.kind}>{msg.text}</FlashBox>}
      {note && !msg && <FlashBox kind="ok">{note}</FlashBox>}
      <Row gap={4} wrap>
        <Field label="Attribute anahtarı" value={draft.key} placeholder="tenant" autoComplete="off"
          onChange={e => setDraft(d => ({ ...d, key: e.target.value }))} />
        <Field label="Ek yazımlar (virgülle)" hint="aynı anahtarın büyük harf / eski adları" value={draft.spellings} placeholder="TENANT_ID, tenantId" autoComplete="off"
          onChange={e => setDraft(d => ({ ...d, spellings: e.target.value }))} />
        <label className="field" style={{ minWidth: 120 }}>
          <span>Kapsam</span>
          <select value={draft.scope} onChange={e => setDraft(d => ({ ...d, scope: e.target.value as 'span' | 'resource' }))}>
            <option value="span">span</option>
            <option value="resource">resource</option>
          </select>
        </label>
        <label className="field" style={{ minWidth: 120 }}>
          <span>Tip</span>
          <select value={draft.type} onChange={e => setDraft(d => ({ ...d, type: e.target.value as 'lc' | 'string' }))}>
            <option value="lc">LowCardinality (≤10k değer)</option>
            <option value="string">String</option>
          </select>
        </label>
        <Button variant="primary" type="button" disabled={busy || !draft.key.trim()} onClick={add} className="tf-add">Facet ekle</Button>
      </Row>
      {rows.length === 0 ? (
        <Empty icon="≡" title="Kayıtlı facet yok" compact>Yerleşik terfi kolonları (channel_code, function_code, k8s.*) kodda; burası operatörün eklediği ek facet'ler.</Empty>
      ) : (
        <div className="table-wrap is-fit">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} />
            <DataTableHead dt={dt} />
            <tbody>
              {dt.sortedRows.map(r => {
                const st = facetStateLabel(r.status);
                return (
                  <tr key={r.key} className="tf-row">
                    <td className="mono">{r.key}</td>
                    <td className="mono" style={{ fontSize: 11, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={(r.spellings ?? [r.key]).join(', ')}>{(r.spellings ?? [r.key]).join(', ')}</td>
                    <td>{r.scope ?? 'span'}</td>
                    <td>{r.type ?? 'lc'}</td>
                    <td className="mono" style={{ fontSize: 11 }}>{r.status?.column ?? ''}</td>
                    <td><Badge tone={st.tone}>{st.text}</Badge></td>
                    <td><Button variant="secondary" size="xs" className="tf-remove" disabled={busy} onClick={() => remove(r.key)} title="Kaydı sil (kolon/indeks ClickHouse'ta kalır; rollback SQL'i elle)">Sil</Button></td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      <Row gap={4}>
        <Button variant="secondary" type="button" onClick={() => setShowSql(v => !v)} aria-expanded={showSql}>
          {showSql ? 'SQL\'i gizle' : 'Prod SQL\'ini göster'}
        </Button>
        {showSql && <CopyButton value={sql} title="SQL'i kopyala" />}
      </Row>
      {showSql && (
        <pre style={{ margin: 0, padding: 10, fontSize: 11, background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 6, overflowX: 'auto', whiteSpace: 'pre' }}>{sql}</pre>
      )}
    </Stack>
  );
}
