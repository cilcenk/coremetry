// AnomalyWindowTable — v0.10.162 (etüt «anomali işaretleri» seçenek C'nin
// tablosu, A'nın bantlarına aşılandı; dilim 1, sıfır backend). Servis
// sayfasındaki RED grafiklerinin altında, PENCEREDEKİ anomaliler: Tür ·
// Desen · Başlangıç · Son görülme · Tepe · Durum · Geri bildirim · eylemler.
// Çakışan anomaliler iki satır — lane/hit-test kodu gerekmez, tablo ayırt
// eder. v0.10.181 (dilim 2): «Anomali» / «Değil» KARARI anomaly_verdicts'e
// (PUT /api/anomalies/{id}/verdict); «Değil» ayrıca mevcut susturmayı zincirler
// (onMute → çağıran hem kararı hem susturmayı yazar). → mevcut
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
  { id: 'fb', label: 'Geri bildirim', width: 210, sortValue: r => (r.e.verdict === 'anomaly' ? 2 : r.e.verdict === 'not_anomaly' || r.silence ? 1 : 0) },
  { id: 'act', label: '', width: 260 },
];

export function AnomalyWindowTable({ events, silences, canEdit, onOpen, onMute, onVerdict, truncated }: {
  events: AnomalyEvent[];
  silences: AnomalySilence[] | undefined;
  canEdit: boolean;
  onOpen: (id: string) => void;
  onMute: (e: AnomalyEvent, durationSec: number) => void;
  /** v0.10.181 — «Anomali» kararı (not_anomaly kararını onMute zinciri yazar) */
  onVerdict: (e: AnomalyEvent, verdict: 'anomaly' | 'not_anomaly') => void;
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
                <td style={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                  {e.verdict === 'anomaly' && <Badge tone="success" title={`${e.verdictBy ?? ''}${e.verdictAt ? ` · ${fmtDateTime(new Date(e.verdictAt / 1e6))}` : ''}`}>anomali ✓</Badge>}
                  {e.verdict === 'not_anomaly' && <Badge tone="neutral" title={`${e.verdictBy ?? ''}${e.verdictAt ? ` · ${fmtDateTime(new Date(e.verdictAt / 1e6))}` : ''}`}>değil ✗</Badge>}
                  {silence && <Badge tone="neutral" style={e.verdict ? { marginLeft: 6 } : undefined} title={`${silence.createdBy} · ${fmtDateTime(new Date(silence.createdAt / 1e6))}${silence.reason ? ` · ${silence.reason}` : ''}`}>sessiz{silence.untilAt > 0 ? ` · ${fmtDateTime(new Date(silence.untilAt / 1e6))}'e dek` : ''}</Badge>}
                  {!e.verdict && !silence && <span className="field-hint">—</span>}
                </td>
                <td style={{ whiteSpace: 'nowrap' }}>
                  <LinkButton onClick={() => onOpen(e.id)} title="Anomali çekmecesini aç">→ detay</LinkButton>
                  {canEdit && e.verdict !== 'anomaly' && (
                    <span style={{ marginLeft: 8 }}>
                      <LinkButton onClick={() => onVerdict(e, 'anomaly')} title="Gerçek anomali: kararı kaydet (dedektör hassasiyeti için sinyal)">Anomali</LinkButton>
                    </span>
                  )}
                  {canEdit && !silence && e.verdict !== 'not_anomaly' && (
                    <span style={{ marginLeft: 8 }}>
                      <SnoozeButton label="Değil → sessize al" title="Bu bir anomali değil: kararı kaydet + parmak izini sustur" onMute={sec => onMute(e, sec)} />
                    </span>
                  )}
                  {/* v0.10.184 (inceleme #5) — zaten susturulmuş olayda «değil» kararı yine verilebilsin (susturma değil, yalnız kayıt) */}
                  {canEdit && silence && e.verdict !== 'not_anomaly' && (
                    <span style={{ marginLeft: 8 }}>
                      <LinkButton onClick={() => onVerdict(e, 'not_anomaly')} title="Bu bir anomali değil: kararı kaydet (zaten susturulmuş)">Değil</LinkButton>
                    </span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="pod-cap">
        <code className="mono">GET /api/anomalies/events</code> (son 24 saat, en yeni 200{truncated ? ' — KESİLDİ, liste tam değil' : ''}) · servis süzgeci istemcide · bant = [başlangıç, son görülme] (endedAt yok) · güven puanı yok (tepe = peakRatio) · «Anomali»/«Değil» = karar (anomaly_verdicts, olay başına son karar), «Değil» ayrıca susturur (kanonik parmak izi; akış + terfi kapısı okur). <ResetLayoutButton dt={dt} />
      </div>
    </>
  );
}
