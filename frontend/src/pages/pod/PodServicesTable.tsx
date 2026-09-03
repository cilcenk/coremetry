// PodServicesTable — v0.10.160 (A anatomisi §4). Bu pod'dan geçen servisler:
// /api/entity/services (entity_seen_5m MV). v0.10.135 PodEntityPanel'in link
// listesi tabloya terfi etti (operatör kuralı: tablo > kart/çip). «Pod» sütunu
// YOK — pod kapsamında services[].pods hep 1 (inceleme: B mockup hatası).
// Son görülme rows[] (pod × servis) üzerinden. Her satır → servis sayfası +
// bu pod'a süzülmüş Traces.
import { Link } from 'react-router-dom';
import { Spinner, Empty } from '@/components/Spinner';
import { useDataTable, DataTableHead, DataTableColgroup, ResetLayoutButton } from '@/components/ui/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { fmtNum, fmtDateTime } from '@/lib/utils';
import { serviceHref } from '@/lib/serviceHref';
import { tracesPivotHref } from '@/lib/pivotHref';
import type { EntityServicesResponse, TimeRange } from '@/lib/types';

interface Row { service: string; spans: number; errors: number; avgMs: number; lastSeen?: string }

const COLS: DataTableColumn<Row>[] = [
  { id: 'service', label: 'Servis', width: 260, sortValue: r => r.service, naturalDir: 'asc' },
  { id: 'spans', label: 'Span', width: 100, numeric: true, sortValue: r => r.spans },
  { id: 'errors', label: 'Hata', width: 90, numeric: true, sortValue: r => r.errors },
  { id: 'errpct', label: 'Hata %', width: 90, numeric: true, sortValue: r => (r.spans ? r.errors / r.spans : 0) },
  { id: 'avg', label: 'Ort. ms', width: 90, numeric: true, sortValue: r => r.avgMs },
  { id: 'last', label: 'Son görülme', width: 160, sortValue: r => r.lastSeen ?? '' },
  { id: 'links', label: '', width: 120 },
];

function ms(v: number): string {
  if (v <= 0) return '—';
  return v < 10 ? v.toFixed(1) : v.toFixed(0);
}

export function PodServicesTable({ data, pending, error, pod, spanCluster, pageRange }: {
  data?: EntityServicesResponse;
  pending: boolean;
  error: unknown;
  pod: string;
  /** span tarafı cluster değeri (Traces linki için; boşsa anahtar yazılmaz) */
  spanCluster: string;
  pageRange: TimeRange;
}) {
  const lastByService = new Map<string, string>();
  for (const r of data?.rows ?? []) if (r.pod === pod) lastByService.set(r.service, r.lastSeen);
  const rows: Row[] = (data?.services ?? []).map(s => ({ ...s, lastSeen: lastByService.get(s.service) }));
  const dt = useDataTable<Row>({ storageKey: 'pod-services', columns: COLS, rows, initialSort: { id: 'spans', dir: 'desc' } });
  if (pending) return <Spinner />;
  if (error) return <Empty icon="!" title="Servisler yüklenemedi">{String(error)}</Empty>;
  if (rows.length === 0) {
    // v0.10.190 — sebep listesi dürüst: MV kurulu değilse (dış Distributed
    // prod'da 0011 operatör migration'ı) de satır yoktur; alttaki trace
    // listesi span gösteriyorsa sorun span değil MV'dir.
    return <Empty icon="∅" title="Bu pencerede bu pod'dan geçen servis yok">entity_seen_5m'de satır yok — boş pencere, k8s.pod.name'siz span'ler, ya da tablo var ama MV beslemiyor (dış Distributed'da 0011 ADIM 6). Alttaki trace listesi bu pod'dan span gösteriyorsa sorun MV'dedir.</Empty>;
  }
  const filters = JSON.stringify([{ k: 'k8s.pod.name', op: '=', v: [pod] }]);
  return (
    <>
      {/* v0.10.190 — namespace'siz span satırları pod adıyla eşlendi; varsayım ilan edilir */}
      {(data?.nsMissingRows ?? 0) > 0 && (
        <div className="pod-cap">{data!.nsMissingRows} satır namespace'siz span'lerden (bu cluster'ın collector'ı k8s.namespace.name basmıyor) — pod adı cluster içinde tek varsayıldı.</div>
      )}
      <div className="table-wrap">
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={dt} />
          <DataTableHead dt={dt} />
          <tbody>
            {dt.sortedRows.map(r => {
              // Pencere tracesPivotHref'te ZORUNLU (inceleme #9) — ham ?range= boşken /traces kendi 30m'sine düşerdi.
              const tracesHref = tracesPivotHref({ window: pageRange, service: r.service, cluster: spanCluster || undefined, filters });
              const errPct = r.spans ? (100 * r.errors) / r.spans : 0;
              return (
                <tr key={r.service}>
                  <td><Link to={serviceHref(r.service, { range: pageRange })} className="sec">{r.service}</Link></td>
                  <td className="num">{fmtNum(r.spans)}</td>
                  <td className="num" style={r.errors > 0 ? { color: 'var(--err)' } : undefined}>{fmtNum(r.errors)}</td>
                  <td className="num">{errPct.toFixed(2)}%</td>
                  <td className="num mono">{ms(r.avgMs)}</td>
                  <td className="mono">{r.lastSeen ? fmtDateTime(new Date(r.lastSeen)) : '—'}</td>
                  <td><Link to={tracesHref} className="sec" title="Bu servisin bu pod'daki trace'leri">Traces →</Link></td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <div className="pod-cap">
        <code className="mono">GET /api/entity/services</code> · entity_seen_5m · seçili pencere · Hata % ve Ort. ms bu pod'daki span'lerin; yüzdelik yok (pod-başı p95 ancak servis sayfasının Pods sekmesinde). <ResetLayoutButton dt={dt} />
      </div>
    </>
  );
}
