import { useMemo } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { useEffect } from 'react';
import { endpointDetailHref } from '@/pages/endpoints/endpointParam';
import { encodeRange } from '@/lib/urlState';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/ui/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { EndpointRow, TimeRange } from '@/lib/types';

// TopEndpointsCard — v0.9.377, redesign D1 (operatör şartı; mockup
// af7419e5). Overview'un sol geniş hücresinde OpsCard'ın yerini alır:
// GİRİŞ span'lerinden (server+consumer; HTTP + RPC birleşik, bundle'ın
// endpoints slotu) toplam wall-clock payına göre sıralı ilk 10 satır —
// "en pahalı giriş noktası üstte", DB panelinin zihinsel modeli.
// OpsCard SİLİNMEDİ: endpoints boş dönerse (giriş span'i olmayan
// servis, eski backend) Overview eski karta düşer — tek-commit revert
// sözleşmesinin dosya hali. Satır tıkı /traces'a (OpsCard davranışı);
// D3 dilimi peek drawer'ı bunun ÜSTÜNE ekleyecek.

function errBadge(rate: number): string {
  return `badge ${rate > 5 ? 'b-err' : rate > 1 ? 'b-warn' : 'b-ok'}`;
}

const totalTimeOf = (r: EndpointRow) => r.calls * r.avgMs;

const EP_COLS: DataTableColumn<EndpointRow>[] = [
  { id: 'path', label: 'Endpoint', sortValue: r => r.path, naturalDir: 'asc', width: 250 },
  { id: 'calls', label: 'Calls', sortValue: r => r.calls, numeric: true, width: 74 },
  { id: 'err', label: 'Err %', sortValue: r => r.errorRate, numeric: true, width: 72 },
  { id: 'p50', label: 'P50', sortValue: r => r.p50Ms ?? 0, numeric: true, width: 70 },
  { id: 'p99', label: 'P99', sortValue: r => r.p99Ms, numeric: true, width: 74 },
  { id: 'share', label: 'Süre payı', sortValue: totalTimeOf, numeric: true, width: 110 },
];

function fmtCount(n: number): string {
  return n >= 1_000_000 ? `${(n / 1_000_000).toFixed(1)}M`
    : n >= 1000 ? `${(n / 1000).toFixed(1)}K` : String(n);
}

export function TopEndpointsCard({ service, range, endpoints }: {
  service: string; range: TimeRange; endpoints: EndpointRow[];
}) {
  const rangeParam = encodeRange(range);
  // v0.9.1086 (operatör isteği): satır tıkı artık DOĞRUDAN /endpoint
  // detay sayfasına gider — v0.9.379'un peek drawer'ı ("drawer açılıp
  // tam analiz demek gerekiyor" iki-adımı) kaldırıldı. Sayfanın env/
  // cluster kapsamı ve range detaya aynen taşınır (v0.9.965 pencere
  // taşıma sınıfı). Bu liste GİRİŞ span'lerinin ham yollarıdır — sig
  // (imza-gruplu) DEĞİL.
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const gotoEp = (path: string) => navigate(endpointDetailHref(
    { service, path, sig: false },
    {
      range: rangeParam,
      env: params.get('env') ?? undefined,
      cluster: params.get('cluster') ?? undefined,
    }));
  // Eski ?ep= derin linkleri (v0.9.379-1085 arası kopyalananlar) drawer
  // yerine detaya YÖNLENDİRİLİR — legacyEndpointTarget emsali: redirect,
  // compat shim değil; drawer'ı yazan hiçbir kod kalmadı.
  const epParam = params.get('ep');
  useEffect(() => {
    if (epParam) gotoEp(epParam);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [epParam]);
  const dt = useDataTable<EndpointRow>({
    storageKey: 'svc-ov-endpoints',
    columns: EP_COLS,
    rows: endpoints,
    initialSort: { id: 'share', dir: 'desc' },
    onOpen: (r) => gotoEp(r.path),
  });
  // Bar ölçeği görünen kümenin maksimumuna göre — pay göreli okunur.
  const maxTime = useMemo(
    () => endpoints.reduce((m, r) => Math.max(m, totalTimeOf(r)), 0),
    [endpoints]);
  return (
    <div className="card">
      <div className="ov-card-h">
        <h3>Top endpoints</h3>
        <span className="badge b-gray" style={{ marginLeft: 8 }}
          title="Yalnız giriş span'leri (server + consumer) — servisin dışarı yaptığı client çağrıları bu listede değildir. Sıralama: pencere içi toplam süre (calls × avg).">
          giriş span&#39;leri
        </span>
        <span className="ov-right">
          <Link className="ov-sub" to={`/endpoints?service=${encodeURIComponent(service)}&range=${rangeParam}`}>
            View all →
          </Link>
        </span>
      </div>
      <div style={{ overflowX: 'auto' }}>
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={dt} />
          <DataTableHead dt={dt} />
          <tbody>
            {dt.sortedRows.slice(0, 10).map((r, i) => {
              const share = maxTime > 0 ? totalTimeOf(r) / maxTime : 0;
              return (
                <tr key={r.path} {...dt.rowProps(i)} style={{ cursor: 'pointer' }}
                    onClick={() => gotoEp(r.path)}>
                  <td><span className="mono" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', display: 'block' }} title={r.path}>{r.path}</span></td>
                  <td className="num">{fmtCount(r.calls)}</td>
                  <td className="num"><span className={errBadge(r.errorRate)}>{r.errorRate.toFixed(1)}%</span></td>
                  <td className="num mono">{r.p50Ms != null ? `${r.p50Ms.toFixed(0)} ms` : '—'}</td>
                  <td className="num mono">{r.p99Ms.toFixed(0)} ms</td>
                  <td>
                    <div title={`pencere içi toplam süre ≈ ${(totalTimeOf(r) / 60000).toFixed(1)} dk`}
                      style={{
                        height: 7, borderRadius: 2,
                        width: `${Math.max(4, share * 100)}%`,
                        background: r.errorRate > 1
                          ? 'linear-gradient(90deg, var(--warn), color-mix(in srgb, var(--warn) 35%, transparent))'
                          : 'linear-gradient(90deg, var(--teal), color-mix(in srgb, var(--teal) 35%, transparent))',
                      }} />
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}
