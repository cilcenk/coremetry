import { useEffect, useMemo, useRef, useState } from 'react';
import { Link, Navigate, useNavigate, useSearchParams } from 'react-router-dom';
import { metricsViewFromParam, metricsViewUrlValue, shouldRedirectLegacyMetric } from './metricsView';
import { useQuery } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { Button } from '@/components/ui/Button';
import { ServicePicker } from '@/components/ServicePicker';
import { MetricQueryEditor } from '@/components/viz/MetricQueryEditor';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { useDebouncedValue } from '@/lib/perf/useDebouncedValue';
import { api } from '@/lib/api';
import { useUrlRange } from '@/lib/useUrlRange';
import { classifyMetric } from '@/lib/metricTemplates';
import { metricCatalogueHref } from './explore/urlCodec';
import { fmtAgoNs, tsLong } from '@/lib/utils';
import {
  METRIC_FACETS, CATALOG_PAGE, metricGroup, decodeCatalogParams, applyCatalogParams,
  catalogCountLabel, facetCountsComplete, nextCatalogLimit, metricIsStale, type MFacet,
} from './metricsCatalog';
import type { DataTableColumn } from '@/lib/dataTable';
import type { MetricInfo } from '@/lib/types';
import { PageControls } from '@/components/ui/PageControls';
import { QueryError } from '@/components/QueryError';
import { PageShell } from '@/components/ui/PageShell';

// Metrics — v0.8.x Phase-5 collapse. /metrics is now a CATALOGUE: a
// server-side-searchable, sortable index of every metric name (name / type /
// unit / description). Picking a row opens it in Explore as a real builder
// query A (source=metric) via metricCatalogueHref — Explore is the one place a
// metric is actually charted, so the old in-page builder + dual-mode explorer
// are gone. Two escape valves remain:
//   • ?editor=1 — the full MetricQueryEditor as a page (power users).
//   • legacy /metrics?metric=&service=&agg= bookmarks / saved views collapse
//     to the canonical /explore?q= seed (Navigate replace).

// v0.9.832 — facet sınıflandırması + URL kodeki + dürüst sayaç metinleri
// pages/metricsCatalog.ts'e taşındı (saf + tablo testli).

// v0.9.833 — "Last seen" + "Services" kolonları. sortValue'lar HAM
// sayıyı döndürür (biçimlenmiş metni değil): "5m ago" metnine göre
// sıralamak alfabetik saçmalık üretirdi.
//
// Services kolonu servis filtresi açıkken GİZLENİR: o kapsamda sayı
// tanım gereği 1'dir ve sabit bir "1" kolonu yer kaplamaktan başka bir
// şey yapmaz.
function catalogColumns(showServices: boolean): DataTableColumn<MetricInfo>[] {
  const cols: DataTableColumn<MetricInfo>[] = [
    { id: 'name', label: 'Metric',    sortValue: m => m.name,             naturalDir: 'asc', width: 340 },
    { id: 'type', label: 'Type',      sortValue: m => m.type,             naturalDir: 'asc', width: 110 },
    { id: 'unit', label: 'Unit',      sortValue: m => m.unit || '',       naturalDir: 'asc', width: 90 },
    { id: 'seen', label: 'Last data', sortValue: m => m.lastSeenNs ?? 0,  naturalDir: 'desc', width: 110, numeric: true },
  ];
  if (showServices) {
    cols.push({ id: 'svcs', label: 'Services', sortValue: m => m.serviceCount ?? 0, naturalDir: 'desc', width: 90, numeric: true });
  }
  cols.push({ id: 'desc', label: 'Description', sortValue: m => m.description || '', naturalDir: 'asc', width: 400 });
  return cols;
}

export default function MetricsPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const navigate = useNavigate();

  // v0.9.561 — varsayılan görünüm EDİTÖR (operatör talebi). Okuma ve
  // yazma tek kodekten geçiyor (metricsView.ts): varsayılan iki yerde
  // ayrı yazılırsa kullanıcının seçimi sessizce yutulur.
  const editorParam = searchParams.get('editor');
  const editor = metricsViewFromParam(editorParam) === 'editor';
  const legacyMetric = searchParams.get('metric') ?? '';

  // v0.9.832 — aralık artık URL'in kendisinde (ev kuralı: her operatör
  // seçimi paylaşılabilir). Eskiden useState idi: gelen `?range=` bir
  // kez okunuyor, seçilen aralık URL'e HİÇ yazılmıyordu — kopyalanan
  // link operatörün baktığı pencereyi taşımıyordu.
  const [range, setRange] = useUrlRange('30m');

  // Katalog seçimleri URL'de: paylaşılabilir + geri/ileri tutarlı.
  // Arama kutusu tek istisna — yazarken her tuşta URL yazmak yerine
  // yerel state tutulur ve DEBOUNCE'lu değer URL'e işlenir (aşağıdaki
  // efekt); okuma ilk render'da URL'den tohumlanır.
  const urlParams = decodeCatalogParams(searchParams);
  const facet = urlParams.facet;
  const service = urlParams.service;
  const [search, setSearch] = useState(urlParams.search);
  const dq = useDebouncedValue(search.trim(), 250);
  // Yüklenen prefix uzunluğu. "Load more" bunu bir sayfa büyütür;
  // dilim (servis/arama) değişince 200'e döner.
  const [limit, setLimit] = useState(CATALOG_PAGE);

  // Seçim → URL (replace:true, yabancı paramlar korunur). Aynı değere
  // yazmaz: setSearchParams'ı koşulsuz çağırmak render döngüsü kurar.
  const writeParams = (p: Partial<{ search: string; facet: MFacet; service: string }>) => {
    setSearchParams(prev => applyCatalogParams(prev, { ...decodeCatalogParams(prev), ...p }), { replace: true });
  };
  // Arama: yerel kutu → (debounce) → URL. `lastWroteRef` bizim yazdığımız
  // son değeri tutar; URL bundan FARKLI bir değere giderse değişiklik
  // DIŞARIDAN gelmiştir (geri/ileri, paylaşılan link) ve kutu ona
  // senkronlanır. Bu ref olmasa ya kutu bayat kalırdı (tek-yönlü yazma)
  // ya da iki taraf birbirini ezerdi — bu repoda dört kez bug olan
  // sınıfın ikisi de (v0.8.253/256/265/267).
  const urlSearch = urlParams.search;
  const lastWroteRef = useRef(urlSearch.trim());
  useEffect(() => {
    if (dq === lastWroteRef.current) return;
    lastWroteRef.current = dq;
    setSearchParams(prev => applyCatalogParams(prev, { ...decodeCatalogParams(prev), search: dq }), { replace: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dq]);
  useEffect(() => {
    const u = urlSearch.trim();
    if (u === lastWroteRef.current) return;
    lastWroteRef.current = u;
    setSearch(urlSearch);
  }, [urlSearch]);

  // Dilim değişti → yüklenen prefix'i başa sar. Aksi halde dar bir
  // servise geçen operatör 600'lük bir pencereyi boşuna ister.
  const sliceKey = `${service}|${dq}`;
  const lastSliceRef = useRef(sliceKey);
  useEffect(() => {
    if (lastSliceRef.current === sliceKey) return;
    lastSliceRef.current = sliceKey;
    setLimit(CATALOG_PAGE);
  }, [sliceKey]);

  // Legacy deep-link → canonical Explore seed. Computed pre-render; the
  // Navigate return sits AFTER every hook so rules-of-hooks holds.
  // Yönlendirme kararı VARSAYILANA DEĞİL, kullanıcının açıkça editör
  // isteyip istemediğine bakar. Eskiden `!editor` yazıyordu; varsayılan
  // editöre çevrilince o koşul hep false olur ve eski `?metric=`
  // linkleri sessizce boş bir sorgu editörüne düşerdi.
  const redirectTo = shouldRedirectLegacyMetric(legacyMetric, editorParam)
    ? metricCatalogueHref(legacyMetric, {
        service: searchParams.get('service') || undefined,
        agg: searchParams.get('agg') || undefined,
        // v0.9.746 — ?by=http.route (virgüllü çoklu) kırılımı Explore
        // seed'ine taşır; Overview route paneli geçişi bunu kullanır.
        splitBy: searchParams.get('by')?.split(',').filter(Boolean) || undefined,
      }) + (searchParams.get('range') ? `&range=${encodeURIComponent(searchParams.get('range')!)}` : '')
    : null;

  // SERVER-SIDE search (scale-audit #10) — debounced + bounded. The eager
  // api.metricNames('') full-catalogue load is fatal at 10k+ names.
  //
  // v0.9.832 — SERVİS FİLTRESİ. Backend zaten hazırdı (metric_catalog
  // ORDER BY (service_name, metric) — service en ucuz filtre) ama bu
  // çağrı sabit '' geçiyordu, yani sayfa binlerce servisin metriklerini
  // tek listede karıştırıp gösteriyordu.
  //
  // Sayfalama PREFIX BÜYÜTEREK yapılır (limit 200→400→…→1000), sayfaları
  // biriktirerek değil: biriktirilen sayfalar üstünde istemci sıralaması
  // bu sürümün kaldırdığı yalanın aynısını üretir. Büyüyen prefix her
  // zaman sunucu sırasının gerçek bir öneki, dolayısıyla facet sayıları
  // ve sıralama kendi içinde tutarlı; başlık da neyin ekranda olduğunu
  // açıkça söylüyor.
  const catalogQ = useQuery({
    queryKey: ['metric-catalog', service, dq, limit],
    queryFn: () => api.metricNamesSearch(service, dq || undefined, limit, 0),
    staleTime: 60_000,
    enabled: !redirectTo && !editor,
  });
  const catalog = useMemo<MetricInfo[]>(() => catalogQ.data?.names ?? [], [catalogQ.data]);
  // Bayatlık referans anı VERİYLE birlikte sabitlenir, her render'da
  // değil: Date.now()'ı doğrudan JSX'te çağırmak aynı listeyi iki
  // render'da farklı boyayabilir ve eşiğe komşu bir satır titrer.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const nowMs = useMemo(() => Date.now(), [catalogQ.data]);
  const total = catalogQ.data?.total ?? catalog.length;
  const hasMore = catalogQ.data?.hasMore ?? false;
  const nextLimit = nextCatalogLimit(limit, hasMore);
  const countsComplete = facetCountsComplete(total, catalog.length);
  const counts = useMemo(() => {
    const c: Record<string, number> = {};
    for (const m of catalog) { const g = metricGroup(m.name); c[g] = (c[g] ?? 0) + 1; }
    return c;
  }, [catalog]);
  const filtered = useMemo(
    () => catalog.filter(m => facet === 'all' || metricGroup(m.name) === facet),
    [catalog, facet]);

  const showServices = !service;
  const columns = useMemo(() => catalogColumns(showServices), [showServices]);
  const dt = useDataTable<MetricInfo>({
    storageKey: 'metric-catalog',
    columns,
    rows: filtered,
    initialSort: { id: 'name', dir: 'asc' },
    // mT3 — `rowProps` bu tabloda ZATEN yayılıyordu ama `onOpen`
    // olmadığı için klavye gezinmesi İNERT'ti: data-row-idx basılıyor,
    // hiçbir tuş bağlanmıyordu. Yazılmış-ama-bağlanmamış kod.
    onOpen: m => navigate(metricHref(m)),
  });

  if (redirectTo) return <Navigate replace to={redirectTo} />;

  // classifyMetric picks the default agg (e.g. p99 for a histogram) so the
  // chart lands right; metricCatalogueHref encodes it into the ?q= seed.
  // v0.9.801 — birim de tohuma girer. Katalog satırı zaten elimizde;
  // geçmezsek Explore aynı birimi bir ağ turuyla yeniden çözmek zorunda
  // kalır ve ilk boyamada süre metrikleri çıplak sayı basar.
  //
  // v0.9.832 — bu artık bir HREF. Satır adı gerçek bir <Link> oldu:
  // ⌘-tık yeni sekmede açar, sağ-tık "bağlantıyı kopyala" çalışır,
  // klavye Tab+Enter satırı açar. onClick'li <tr> bunların hiçbirini
  // vermiyordu — katalog gezinme sayfasıdır, tarayıcı davranışını
  // ondan saklamak ucuz değil.
  // Servis filtresi açıkken tohum o servise KAPSANIR (scope): operatör
  // "api-gateway'in metrikleri"ne bakıyorsa, açılan grafik de o servisin
  // olmalı — yoksa katalog daraltması Explore'a geçerken sessizce kaybolur.
  //
  // v0.9.833 — KAPI ONARIMI. classifyMetric bir ŞABLON döndürür (agg +
  // groupBy + threshold + unit) ama bu çağrı yalnız `agg`ı taşıyordu:
  // "p99 of http.server.request.duration BY http.route" öneren kayıt,
  // Explore'a yalnız "p99" olarak varıyordu. Kırılım şablonun asıl
  // değeriydi ve 16 şablonun 15'inde groupBy dolu, yani öneri
  // altyapısının neredeyse tamamı ölü kod olarak duruyordu. Artık
  // splitBy de tohuma giriyor.
  //
  // Seri patlaması yok: panel varsayılanı alana göre top-10
  // (explore/model.ts PANEL_SERIES_CAP), yani 99 servisli bir sayaç
  // 99 çizgi değil 10 çizgi çiziyor. Metrikte olmayan bir anahtar
  // hata da vermiyor — groupKeyExprMetric attr yoksa boş dizge döner
  // (tek seri), yani şablon yanlış eşleşse bile grafik bozulmuyor.
  //
  // `unit` BİLEREK ham katalog birimi (m.unit) kalıyor, şablonunki
  // değil: urlCodec sözleşmesi q.unit'in HAM OTLP yuvası olduğunu
  // söylüyor (çeviri queryUnit → otlpUnitToGrafana'da). Şablonun unit
  // alanı bir GÖRÜNTÜ ipucudur ('bytes', '%'), OTLP yuvası değil
  // ('By', '1'); onu geçirmek v0.9.801'in kurduğu zinciri bozardı.
  //
  // `threshold` taşınmıyor — builder'ın eşik alanı yok. Şablonun o
  // parçası hâlâ kullanılmıyor (bilinçli, ayrı kapı).
  const metricHref = (m: MetricInfo) => {
    const t = classifyMetric(m);
    return metricCatalogueHref(m.name, {
      agg: t?.agg,
      unit: m.unit,
      splitBy: t?.groupBy,
      service: service || undefined,
    });
  };

  return (
    <>
      <Topbar title="Metrics" range={range} onRangeChange={setRange} />
      <PageShell>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)' }}>
            Metric catalogue — pick one to open it in Explore.
          </div>
          <div style={{ flex: 1 }} />
          <Button variant={editor ? 'primary' : 'secondary'} size="sm"
            onClick={() => {
              // Yabancı paramlar KORUNUR (range!). Eskiden tam URL
              // yazılıyordu ve görünüm değiştiren operatörün zaman
              // aralığı sıfırlanıyordu.
              const next = new URLSearchParams(searchParams);
              const v = metricsViewUrlValue(editor ? 'catalogue' : 'editor');
              if (v !== null) next.set('editor', v); else next.delete('editor');
              navigate({ pathname: '/metrics', search: next.toString() }, { replace: true });
            }}>
            {editor ? '← Catalogue' : 'Advanced query editor →'}
          </Button>
        </div>

        {editor ? (
          <MetricQueryEditor range={range} />
        ) : (
          <>
            <PageControls sticky style={{ marginBottom: 10 }}>
              <input className="field" placeholder="Search metrics…" value={search}
                onChange={e => setSearch(e.target.value)} style={{ width: 240 }} autoFocus />
              {/* Servis filtresi (v0.9.832) — sunucu-taraflı picker (ev
                  kuralı: asla eager katalog). Backend bunu metric_catalog'un
                  ORDER BY ilk kolonuna basar, yani en ucuz daraltma. */}
              <ServicePicker value={service} onChange={v => writeParams({ service: v })}
                placeholder="All services" width={220} />
              <div className="ov-logbar" style={{ gap: 4, marginBottom: 0 }}
                title={countsComplete
                  ? 'Facet counts cover every matching metric.'
                  : 'Facet counts cover the LISTED rows only — the catalogue is longer than what is loaded. Load more, or narrow with search / service.'}>
                {METRIC_FACETS.map(g => (
                  <span key={g.key}
                    className={'ov-facet' + (facet === g.key ? ' on' : '')}
                    role="button" tabIndex={0}
                    onClick={() => writeParams({ facet: g.key })}
                    onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); writeParams({ facet: g.key }); } }}>
                    {g.label}{g.key !== 'all' && <span className="n">{counts[g.key] ?? 0}</span>}
                  </span>
                ))}
              </div>
              <div style={{ flex: 1 }} />
              {/* DÜRÜST SAYAÇ (v0.9.832). Sunucu `total`ı zaten dönüyordu ve
                  atılıyordu; ekrandaki 200 satır tüm katalogmuş gibi
                  okunuyordu. Artık ne kadarının indiği yazıyor. */}
              <span style={{ fontSize: 11, color: 'var(--text3)', fontVariantNumeric: 'tabular-nums' }}
                title={countsComplete
                  ? undefined
                  : `The server orders by metric name and returns a prefix; sorting and facet counts on this page apply to those ${catalog.length} rows.`}>
                {catalogCountLabel(total, catalog.length)}
                {!countsComplete && <span style={{ color: 'var(--warn)' }}> · facet counts = listed only</span>}
              </span>
            </PageControls>

            {catalogQ.isLoading ? <Spinner />
              /* v0.9.858 (UX denetimi K6) — hata "No metrics match" olarak
                 sunuluyordu: katalog sorgusu düştüğünde operatör arama
                 terimini/servisini suçluyordu. */
              : catalogQ.isError ? (
                <QueryError
                  message={catalogQ.error instanceof Error ? catalogQ.error.message : undefined}
                  onRetry={() => catalogQ.refetch()}>
                  The metric catalogue could not be loaded — this is a failed
                  read, not an empty catalogue.
                </QueryError>
              )
              : filtered.length === 0 ? (
                <Empty icon="∿" title="No metrics match">
                  {service
                    ? <>No metric in the catalogue for <code>{service}</code>{facet !== 'all' ? ' in this facet' : ''}
                      {dq ? <> matching “{dq}”</> : null}. Clear the service filter, or check that it is pushing metrics.</>
                    : <>Try a different search, or check <code>OTEL_EXPORTER_OTLP_ENDPOINT</code> apps are pushing.</>}
                </Empty>
              ) : (
                <>
                  <div className="table-wrap is-fit">
                    <table style={{ tableLayout: 'fixed', width: '100%' }}>
                      <DataTableColgroup dt={dt} />
                      <DataTableHead dt={dt} />
                      <tbody>
                        {dt.sortedRows.map((m, i) => (
                          <tr key={m.name} {...dt.rowProps(i)}
                            // Değiştirici tuşlu tık satırda YOK SAYILIR: ⌘-tık
                            // "yeni sekme" demektir ve satır bir link değil, o
                            // yüzden aynı sekmede gezinmek operatörün istediğinin
                            // TAM TERSİ olurdu. Yeni sekme isteyen ada tıklar.
                            onClick={e => {
                              if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
                              navigate(metricHref(m));
                            }}
                            style={{ cursor: 'pointer', ...(dt.sortedRows.length > 100 ? { contentVisibility: 'auto', containIntrinsicSize: 'auto 33px' } : null) }}
                            title={`Open ${m.name} in Explore`}>
                            <td className="mono" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                              {/* Gerçek <a>: ⌘-tık yeni sekme, sağ-tık menüsü,
                                  Tab+Enter. draggable=false olmasa metin
                                  seçmeye çalışan operatör linki SÜRÜKLER —
                                  metrik adını kopyalamak bu sayfanın en sık
                                  işi. stopPropagation satırın onClick'inin
                                  ikinci kez gezinmesini engeller. */}
                              <Link to={metricHref(m)} draggable={false}
                                onClick={e => e.stopPropagation()}
                                style={{ color: 'inherit', textDecoration: 'none' }}>
                                {m.name}
                              </Link>
                            </td>
                            <td>{m.type}</td>
                            <td className="mono">{m.unit || '·'}</td>
                            {/* Son veri (v0.9.833). Bilinmeyen "—" basar,
                                "0s ago" değil — v0.9.833 öncesi bir
                                sunucu alanı hiç göndermez ve uydurma bir
                                tazelik en kötü yalandır. */}
                            <td style={{
                              color: metricIsStale(m.lastSeenNs, nowMs) ? 'var(--text3)' : 'var(--text2)',
                              opacity: metricIsStale(m.lastSeenNs, nowMs) ? 0.65 : 1,
                              fontVariantNumeric: 'tabular-nums',
                            }}
                              title={m.lastSeenNs
                                ? tsLong(m.lastSeenNs)
                                  + (metricIsStale(m.lastSeenNs, nowMs) ? ' — no data in the last 24h' : '')
                                : 'The server did not report a last-seen timestamp for this metric.'}>
                              {m.lastSeenNs ? fmtAgoNs(m.lastSeenNs) : '—'}
                            </td>
                            {showServices && (
                              <td style={{ fontVariantNumeric: 'tabular-nums' }}
                                title={m.serviceCount
                                  ? `${m.serviceCount.toLocaleString()} service${m.serviceCount === 1 ? '' : 's'} reported ${m.name} in the last 7 days`
                                  : undefined}>
                                {m.serviceCount ? m.serviceCount.toLocaleString() : '—'}
                              </td>
                            )}
                            <td style={{ color: 'var(--text2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                              title={m.description}>
                              {m.description || '—'}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                  {/* v0.9.832 — "refine your search" ARTIK TEK SEÇENEK DEĞİL.
                      Sunucu prefix'i sayfa sayfa büyür; tavana (1000)
                      varınca buton kaybolur ve daraltma tavsiyesi kalır. */}
                  <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 4px', color: 'var(--text3)', fontSize: 11 }}>
                    {nextLimit !== null && (
                      <Button variant="secondary" size="sm"
                        onClick={() => setLimit(nextLimit)}
                        title={`Fetch the next ${CATALOG_PAGE} catalogue rows`} loading={catalogQ.isFetching}>
                        ↓ Load more
                      </Button>
                    )}
                    <span>{catalogCountLabel(total, catalog.length)}</span>
                    {hasMore && nextLimit === null && (
                      <span style={{ color: 'var(--warn)' }}>
                        · page cap reached — narrow with search or a service to see the rest
                      </span>
                    )}
                  </div>
                </>
              )}
          </>
        )}
      </PageShell>
    </>
  );
}
