import { useMemo, useRef, useState, FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { Button, Field, IconButton, Modal, SearchField, Stack } from '@/components/ui';
import { Sparkline } from '@/components/Sparkline';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/ui/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { api } from '@/lib/api';
import { parseDashboardImport } from '@/lib/dashboardIO';
import { toast } from '@/lib/toast';
import { tsLong, fmtNum } from '@/lib/utils';
import type { DashboardSummary } from '@/lib/types';
import { PageControls } from '@/components/ui/PageControls';
import { PageShell } from '@/components/ui/PageShell';

// STAR_PAGE (v0.9.780) — yıldızlar saved_views'te yaşıyor, YENİ ŞEMA YOK
// (CLAUDE.md: kullanıcı-kaydı state için saved_views). Satır başına
// page='dashboard-star', queryString=<pano id>, name=<pano adı>.
// Kullanıcı-başı olması doğru davranış: yıldız kişisel bir kısayol,
// etiketler ise panonun kendisine ait ve paylaşılan.
//
// /api/views'te rol kapısı yok — viewer da yıldızlayabilir, öyle olmalı.
// `shared` ASLA gönderilmiyor: true olsaydı yıldız herkese yapışır ve
// admin olmayan kimse söküp atamazdı.
const STAR_PAGE = 'dashboard-star';

// Columns for the shared sortable + resizable DataTable primitive. The
// list is a small fetched array (saved dashboards), so client-side sort
// applies.
//
// Sıralama (v0.9.780): varsayılan yıldız DESC. Sunucu zaten
// updated_at DESC döndürüyor ve sortRows STABLE olduğundan sonuç
// "yıldızlılar üstte, her grup içinde en son güncellenen önce" —
// yani eski varsayılan grupların İÇİNDE aynen korunuyor.
function dashCols(starMap: Map<string, string>): DataTableColumn<DashboardSummary>[] {
  return [
    { id: 'star',        label: '',           sortValue: r => (starMap.has(r.id) ? 1 : 0), numeric: true, width: 36, minWidth: 36 },
    { id: 'name',        label: 'Dashboard',  sortValue: r => r.name,        naturalDir: 'asc', width: 240 },
    { id: 'tags',        label: 'Tags',       sortValue: r => (r.tags ?? []).join(','), naturalDir: 'asc', width: 180 },
    { id: 'description', label: 'Description', sortValue: r => r.description, naturalDir: 'asc', width: 300 },
    { id: 'updatedAt',   label: 'Updated',    sortValue: r => r.updatedAt,   numeric: true,     width: 150 },
  ];
}

export default function DashboardsPage() {
  const { user } = useAuth();
  const navigate = useNavigate();
  const searchRef = useRef<HTMLInputElement>(null);
  const [showNew, setShowNew] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const [importing, setImporting] = useState(false);
  const [q, setQ] = useState('');

  // v0.6.50 — import a dashboard from a previously-exported JSON
  // file. Reuses POST /api/dashboards (createDashboard) so the
  // imported board lands as a fresh dashboard with a new id; no
  // new backend route needed. Validation lives in
  // parseDashboardImport so it's unit-testable + shared with any
  // future drag-drop surface.
  const onImportFile = async (file: File) => {
    setImporting(true);
    try {
      const text = await file.text();
      const payload = parseDashboardImport(text); // throws on bad shape
      const d = await api.createDashboard(payload);
      toast.success(`Imported "${payload.name}"`);
      navigate(`/dashboard?id=${d.id}&edit=1`);
    } catch (err) {
      toast.error('Import failed: ' + (err instanceof Error ? err.message : String(err)));
    } finally {
      setImporting(false);
      if (fileRef.current) fileRef.current.value = ''; // allow re-import of same file
    }
  };

  const dashboardsQ = useQuery<DashboardSummary[]>({
    queryKey: ['dashboards', 'list'],
    queryFn: async () => (await api.listDashboards()) ?? [],
    staleTime: 60_000,
  });
  const items = dashboardsQ.isLoading
    ? undefined
    : dashboardsQ.isError
      ? null
      : dashboardsQ.data ?? [];

  // Substring filter over name + description + tags, case-insensitive.
  // Etiketler filtreye v0.9.780'de girdi: bir etiketi görüp aramak
  // ("prod" yazmak) etiketlerin var olma sebebi.
  const filtered = useMemo(() => {
    if (!items) return items;
    const needle = q.trim().toLowerCase();
    if (!needle) return items;
    return items.filter(d =>
      d.name.toLowerCase().includes(needle) ||
      (d.description ?? '').toLowerCase().includes(needle) ||
      (d.tags ?? []).some(t => t.toLowerCase().includes(needle)));
  }, [items, q]);

  // ── Yıldızlar (v0.9.780) ──────────────────────────────────────────
  // saved_views(page='dashboard-star') → Map<pano id, saved view id>.
  const starsQ = useQuery({
    queryKey: ['dashboard-stars'],
    queryFn: async () => (await api.savedViews(STAR_PAGE)) ?? [],
    staleTime: 60_000,
  });
  const starMap = useMemo(() => {
    const m = new Map<string, string>();
    // TUZAK: createSavedView UPSERT DEĞİL — her POST yeni satır üretir
    // (Alerts.tsx'teki "server dedups" yorumu yanlış). Çift-tık yarışında
    // aynı pano için iki satır kalabilir; okuma tarafı Map'e indirgeyerek
    // zararsızlaştırıyor, silme ilk bulduğu satırı kaldırır.
    for (const v of starsQ.data ?? []) {
      if (v.queryString && !m.has(v.queryString)) m.set(v.queryString, v.id);
    }
    return m;
  }, [starsQ.data]);
  const [starBusy, setStarBusy] = useState<string | null>(null);
  const toggleStar = async (d: DashboardSummary) => {
    if (starBusy) return;
    setStarBusy(d.id);
    try {
      const viewId = starMap.get(d.id);
      if (viewId) {
        await api.deleteSavedView(viewId);
      } else {
        await api.createSavedView({
          // name BOŞ OLAMAZ: boş ad saved_views'te tombstone demek
          // (okuma yolu name != '' filtreliyor), yani yıldız daha
          // doğarken silinmiş sayılırdı.
          name: d.name || d.id,
          page: STAR_PAGE,
          queryString: d.id,
          // `shared` bilinçli GÖNDERİLMİYOR — bkz. STAR_PAGE notu.
        });
      }
      await starsQ.refetch();
    } catch (err) {
      toast.error('Yıldız güncellenemedi: ' + (err instanceof Error ? err.message : String(err)));
    } finally {
      setStarBusy(null);
    }
  };

  // Single global "spans/min over last 1h" series. Every row
  // renders the same sparkline because the metric is system-
  // wide; fetching once and sharing avoids N parallel requests
  // when the dashboard list is long. Refresh every minute.
  const activityQ = useQuery({
    queryKey: ['dashboards', 'activity'],
    queryFn: async () => {
      const now = Date.now() * 1e6;
      const from = now - 60 * 60 * 1e9; // last 1h
      const series = await api.spanMetric({
        agg: 'count',
        from, to: now,
        step: 60, // 1-min buckets, ~60 points
      });
      return series?.[0]?.points ?? [];
    },
    staleTime: 60_000,
    refetchInterval: 60_000,
  });
  const activity = (activityQ.data ?? []).map(p => p.value);
  const totalSpans = activity.reduce((a, b) => a + b, 0);

  const isAdmin = user?.role === 'admin' || user?.role === 'editor';

  // Shared sortable + resizable table with operator-speed keyboard nav
  // (j/k move, Enter/o open, "/" focuses the filter). Called
  // unconditionally (hooks rule) with [] while loading.
  const columns = useMemo(() => dashCols(starMap), [starMap]);
  const dt = useDataTable<DashboardSummary>({
    storageKey: 'dashboards',
    columns,
    rows: filtered ?? [],
    // v0.9.780 — yıldızlılar üstte. Sunucu sırası zaten updated_at DESC
    // ve sortRows stable, dolayısıyla grup İÇİ sıra eski varsayılanın
    // aynısı kalıyor.
    initialSort: { id: 'star', dir: 'desc' },
    onOpen: d => navigate(`/dashboard?id=${d.id}`),
    searchRef,
  });

  return (
    <>
      <Topbar title="Dashboards" />
      <PageShell>
        <PageControls sticky>
          {/* v0.9.1012 (M8) — ayrı "Clear" butonu atomun kutu-içi ✕'ine
              devredildi: temizleme affordance'ı aramanın KENDİSİNDE. */}
          <SearchField ref={searchRef} value={q} onChange={setQ}
            placeholder="Filter dashboards…" aria-label="Filter dashboards"
            hint="/" width={220} />
          {isAdmin && (
            <>
              <Button variant="primary" onClick={() => setShowNew(true)}>+ New dashboard</Button>
              <Button variant="secondary" onClick={() => fileRef.current?.click()}
                title="Import a dashboard from an exported JSON file" loading={importing}>
                ↑ Import JSON
              </Button>
              <input ref={fileRef} type="file" accept="application/json,.json"
                style={{ display: 'none' }}
                onChange={e => {
                  const f = e.target.files?.[0];
                  if (f) onImportFile(f);
                }} />
            </>
          )}
          <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
            {filtered?.length ?? 0} dashboard{(filtered?.length ?? 0) === 1 ? '' : 's'}
          </span>
        </PageControls>

        {items === undefined && <Spinner />}
        {items === null && <Empty icon="⚠" title="Failed to load dashboards" />}
        {items && items.length === 0 && (
          <Empty icon="◫" title="No dashboards yet"
            action={isAdmin ? <Button variant="primary" onClick={() => setShowNew(true)}>+ New dashboard</Button> : undefined}>
            {isAdmin ? 'Create one to combine metrics, traces and logs into a single view.'
                     : 'Ask an admin to create dashboards.'}
          </Empty>
        )}
        {items && items.length > 0 && filtered && filtered.length === 0 && (
          <Empty icon="◇" title="No matching dashboards">
            No saved dashboard matches “{q}”.
          </Empty>
        )}
        {filtered && filtered.length > 0 && (
          <div className="table-wrap is-fit">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} />
              <DataTableHead dt={dt} />
              <tbody>
                {dt.sortedRows.map((d, i) => (
                  <tr key={d.id}
                      {...dt.rowProps(i)}
                      onMouseEnter={() => dt.nav.setSelected(i)}
                      onClick={() => navigate(`/dashboard?id=${d.id}`)}
                      style={{ cursor: 'pointer' }}>
                    {/* <td> SIRASI dashCols() ile BİREBİR olmak zorunda —
                        tableLayout:fixed + colgroup, kayma tsc'ye
                        görünmez. Sıra: star · name · tags · description
                        · updatedAt. */}
                    <td>
                      <IconButton
                        aria-label={starMap.has(d.id) ? `${d.name} yıldızını kaldır` : `${d.name} panosunu yıldızla`}
                        active={starMap.has(d.id)}
                        variant="bare" size="xs" className="ib-star"
                        title={starMap.has(d.id) ? 'Yıldızı kaldır' : 'Yıldızla — listenin başına gelir'}
                        disabled={starBusy === d.id}
                        // Satırın kendi onClick'i panoya gidiyor;
                        // durdurulmazsa her yıldız tıklaması sayfayı
                        // değiştirirdi.
                        onClick={e => { e.stopPropagation(); void toggleStar(d); }}
                        icon={starMap.has(d.id) ? '★' : '☆'} />
                    </td>
                    <td>
                      <span style={{ fontWeight: 600, color: 'var(--text)' }}>{d.name}</span>
                    </td>
                    <td title={(d.tags ?? []).join(', ') || undefined}>
                      <TagBadges tags={d.tags} />
                    </td>
                    <td style={{ color: 'var(--text2)' }} title={d.description || undefined}>
                      {d.description || <span style={{ color: 'var(--text3)' }}>—</span>}
                    </td>
                    <td className="num mono" style={{ color: 'var(--text2)' }}
                        title={`Updated ${tsLong(d.updatedAt)}`}>
                      {tsLong(d.updatedAt)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {/* Shared system-wide activity strip — same data as the
            former per-card thumbnails, lifted out so the table rows
            stay scannable. Tells the operator at a glance whether
            ingest traffic is steady, ramping, or quiet. */}
        {filtered && filtered.length > 0 && (
          <div className="row gap-3" style={{
            marginTop: 12, padding: '8px 12px',
            border: '1px solid var(--border)', borderRadius: 8,
            background: 'var(--bg1)',
          }}>
            <span style={{ fontSize: 11, color: 'var(--text3)' }}>Ingest · last 1h</span>
            {activity.length > 1 ? (
              <Sparkline values={activity} width={180} height={28}
                title={`Spans/min · last 1h · total ${fmtNum(totalSpans)}`} />
            ) : (
              <span style={{ color: 'var(--text3)', fontSize: 11 }}>—</span>
            )}
            <span style={{ flex: 1 }} />
            <span style={{ fontSize: 11, color: 'var(--text2)' }} className="mono">
              {fmtNum(totalSpans)} spans/h
            </span>
          </div>
        )}

        {showNew && isAdmin && (
          <NewDashboardModal
            onClose={() => setShowNew(false)}
            onCreated={(id) => { setShowNew(false); navigate(`/dashboard?id=${id}&edit=1`); }}
          />
        )}
      </PageShell>
    </>
  );
}

// TagBadges (v0.9.780) — etiket rozetleri. İlk 3 + "+N": kolon
// genişliği sabit (tableLayout:fixed) ve taşan içerik sessizce
// kırpılırdı; sayı görünür kalsın diye kalanı tek rozette toplanıyor,
// tamamı hücrenin title'ında.
function TagBadges({ tags }: { tags?: string[] }) {
  const list = tags ?? [];
  if (list.length === 0) return <span style={{ color: 'var(--text3)' }}>—</span>;
  const shown = list.slice(0, 3);
  const rest = list.length - shown.length;
  return (
    <span className="row gap-1" style={{ flexWrap: 'nowrap', minWidth: 0 }}>
      {shown.map(t => (
        <span key={t} className="badge b-gray" style={{
          maxWidth: 90, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
        }}>{t}</span>
      ))}
      {rest > 0 && <span className="badge b-gray" title={list.join(', ')}>+{rest}</span>}
    </span>
  );
}

function NewDashboardModal({ onClose, onCreated }: {
  onClose: () => void; onCreated: (id: string) => void;
}) {
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      const d = await api.createDashboard({ name, description, panels: [] });
      onCreated(d.id);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open={true}
      onClose={onClose}
      title="New dashboard"
      size="sm"
      initialFocus="input[name=name]"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button variant="primary" type="submit" form="new-dashboard-form" loading={busy}>Create</Button>
        </>
      }>
      <form id="new-dashboard-form" onSubmit={submit}>
        <Stack gap={3}>
          <Field
            label="Name"
            name="name"
            required
            value={name}
            onChange={e => setName(e.target.value)} />
          <Field
            label="Description (optional)"
            value={description}
            onChange={e => setDescription(e.target.value)} />
          {error && (
            <div style={{ color: 'var(--err)', fontSize: 12 }}>{error}</div>
          )}
        </Stack>
      </form>
    </Modal>
  );
}
