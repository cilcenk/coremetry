// AnomalyWindowTable — v0.10.162 (etüt «anomali işaretleri» seçenek C'nin
// tablosu, A'nın bantlarına aşılandı; dilim 1, sıfır backend). Servis
// sayfasındaki RED grafiklerinin altında, PENCEREDEKİ anomaliler: Tür ·
// Desen · Başlangıç · Son görülme · Tepe · Durum · Geri bildirim · eylemler.
// Çakışan anomaliler iki satır — lane/hit-test kodu gerekmez, tablo ayırt
// eder. «Değil → sessize al» = MEVCUT susturma (POST /api/anomalies/silences);
// «Evet» kararı için depo YOK (dilim 2) — çizilmez. → mevcut
// AnomalyDetailDrawer (?anomaly=). Yalnız pencerede anomali varsa çizilir.
import { Badge, LinkButton } from '@/components/ui';
import { useDataTable, DataTableHead, DataTableColgroup, ResetLayoutButton } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { fmtDateTime } from '@/lib/utils';
import { ANOMALY_KIND_COLOR, ANOMALY_KIND_TR, silenceKey } from '@/lib/anomalyRegions';
import { SnoozeButton } from './SnoozeButton';
import type { AnomalyEvent, AnomalySilence } from '@/lib/types';

interface Row { e: AnomalyEvent; silence?: AnomalySilence }

const COLS: DataTableColumn<Row>[] = [
  { id: 'kind', label: 'Tür', width: 130, sortValue: r => r.e.kind, naturalDir: 'asc' },
  { id: 'pattern', label: 'Desen', width: 300, sortValue: r => r.e.pattern, naturalDir: 'asc' },
  { id: 'started', label: 'Başlangıç', width: 150, sortValue: r => r.e.startedAt },
  { id: 'last', label: 'Son görülme', width: 150, sortValue: r => r.e.lastSeen },
  { id: 'peak', label: 'Tepe ×', width: 80, numeric: true, sortValue: r => r.e.peakRatio },
  { id: 'status', label: 'Durum', width: 90, sortValue: r => r.e.status },
  { id: 'fb', label: 'Geri bildirim', width: 200, sortValue: r => (r.silence ? 1 : 0) },
  { id: 'act', label: '', width: 200 },
];

export function AnomalyWindowTable({ events, silences, canEdit, onOpen, onMute, truncated }: {
  events: AnomalyEvent[];
  silences: AnomalySilence[] | undefined;
  canEdit: boolean;
  onOpen: (id: string) => void;
  onMute: (e: AnomalyEvent, durationSec: number) => void;
  /** /api/anomalies/events limit=200'e dayandı — liste tam değil */
  truncated: boolean;
}) {
  const byFp = new Map<string, AnomalySilence>();
  for (const s of silences ?? []) if (s.active) byFp.set(s.fingerprint, s);
  const rows: Row[] = events.map(e => ({ e, silence: byFp.get(e.id) ?? byFp.get(silenceKey(e)) }));
  const dt = useDataTable<Row>({ storageKey: 'svc-anomaly-window', columns: COLS, rows, initialSort: { id: 'started', dir: 'desc' } });
  if (rows.length === 0) return null;
  return (
    <>
      <div className="table-wrap">
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={dt} />
          <DataTableHead dt={dt} />
          <tbody>
            {dt.sortedRows.map(({ e, silence }) => (
              <tr key={e.id} style={silence ? { color: 'var(--text3)' } : undefined}>
                <td>
                  <span aria-hidden="true" style={{ display: 'inline-block', width: 8, height: 8, borderRadius: 2, background: silence ? 'var(--text3)' : (ANOMALY_KIND_COLOR[e.kind] ?? 'var(--warn)'), marginRight: 6, verticalAlign: 'middle' }} />
                  {ANOMALY_KIND_TR[e.kind] ?? e.kind}
                </td>
                <td className="mono" title={e.pattern} style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{e.pattern}</td>
                <td className="mono">{fmtDateTime(new Date(e.startedAt / 1e6))}</td>
                <td className="mono">{fmtDateTime(new Date(e.lastSeen / 1e6))}</td>
                <td className="num mono">×{e.peakRatio.toFixed(1)}</td>
                <td>{e.status === 'active' ? <Badge tone="danger">active</Badge> : <Badge tone="neutral">cleared</Badge>}</td>
                <td>
                  {silence
                    ? <Badge tone="neutral" title={`${silence.createdBy} · ${fmtDateTime(new Date(silence.createdAt / 1e6))}${silence.reason ? ` · ${silence.reason}` : ''}`}>sessiz{silence.untilAt > 0 ? ` · ${fmtDateTime(new Date(silence.untilAt / 1e6))}'e dek` : ''}</Badge>
                    : <span className="field-hint">—</span>}
                </td>
                <td style={{ whiteSpace: 'nowrap' }}>
                  <LinkButton onClick={() => onOpen(e.id)} title="Anomali çekmecesini aç">→ detay</LinkButton>
                  {canEdit && !silence && (
                    <span style={{ marginLeft: 8 }}>
                      <SnoozeButton label="Değil → sessize al" title="Bu bir anomali değil: parmak izini sustur (mevcut susturma; karar deposu yok)" onMute={sec => onMute(e, sec)} />
                    </span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="pod-cap">
        <code className="mono">GET /api/anomalies/events</code> (son 24 saat, en yeni 200{truncated ? ' — KESİLDİ, liste tam değil' : ''}) · servis süzgeci istemcide · bant = [başlangıç, son görülme] (endedAt yok) · güven puanı yok (tepe = peakRatio) · «Değil» = mevcut susturma (kanonik parmak izi; akış + terfi kapısı okur), «Evet» kararı için depo yok. <ResetLayoutButton dt={dt} />
      </div>
    </>
  );
}
