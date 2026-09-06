// LogPatternsPanel.tsx — v0.10.298 (log-search audit Dilim 2b): /logs
// "Desenler" — pencere içindeki mesajlar NormalizeSignature imzasıyla
// gruplanmış (sunucu, ÖRNEKLEMELİ ≤cap satır). Sayımlar örneğe göredir;
// altbilgi bunu SÖYLER. Fetch yalnız panel açıkken (v0.8.270 disiplini).
// Satır / "Ara" → şablondan türetilen tırnaklı AND sorgusu serbest metne
// yazılır (iki backend de anlar; yaklaşıktır, öyle etiketlenir).
//
// v0.10.310 (Dilim 2c) — ikinci sekme "Şablonlar": Drain templater'ın
// KALICI şablonları (/api/logs/templates). Fark açıkça yazılır: Desenler
// pencerenin örneği, Şablonlar 5 dk'da ≤1000 satırlık örneklemeden
// biriken kalıcı liste; totalCount pencere sayımı DEĞİL. Sekme yerel
// tercih (panelin açık/kapalı durumu gibi); fetch yalnız aktif sekme için.
import { useMemo, useState } from 'react';
import { TabStrip } from '@/components/ui/TabStrip'; // v0.10.456 (D5)
import { rowActivation } from '@/lib/a11y'; // v0.10.455 (dış denetim D3 dilim 3)
import { useLogsPatterns, useLogsTemplates } from '@/lib/queries';
import type { LogsParams } from '@/lib/api';
import type { LogPatternGroup, LogTemplate } from '@/lib/types';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/ui/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { Button } from '@/components/ui/Button';
import { Spinner, Empty } from '@/components/Spinner';
import { sevClass, sevName, tsLong, tsShort } from '@/lib/utils';
import type { LogPatternsResult } from '@/lib/types';
import { getRaw, setRaw } from '@/lib/storage';

export function agoLabel(ns: number, nowMs = Date.now()): string {
  const s = Math.max(0, Math.round((nowMs - ns / 1e6) / 1000));
  if (s < 60) return `${s} sn önce`;
  if (s < 3600) return `${Math.round(s / 60)} dk önce`;
  if (s < 86400) return `${Math.round(s / 3600)} sa önce`;
  return `${Math.round(s / 86400)} g önce`;
}

// coveredLabel — v0.10.441 (log arama denetimi C4): tavan dolduysa
// örneklemenin GERÇEKTEN kapsadığı alt pencere. Kapı sampled >= cap
// (truncated değil — Total güvenilmez olabilir, sunucu clamp'i truncated'ı
// false bırakır). Tavan dolmadıysa null: aralık yalnız veri yayılımıdır,
// "daraltılmış tarama" diye göstermek yeni bir yalan olurdu. Eski
// önbellek gövdesinde alanlar yok → null.
export function coveredLabel(d: Pick<LogPatternsResult, 'sampled' | 'cap' | 'coveredFromNs' | 'coveredToNs'> | null | undefined): string | null {
  if (!d || !d.coveredFromNs || !d.coveredToNs || d.cap <= 0 || d.sampled < d.cap) return null;
  const spanS = Math.max(0, Math.round((d.coveredToNs - d.coveredFromNs) / 1e9));
  const span = spanS < 60 ? `${spanS} sn` : spanS < 3600 ? `${Math.round(spanS / 60)} dk` : `${(spanS / 3600).toFixed(1)} sa`;
  return `kapsanan: ${tsShort(d.coveredFromNs).slice(0, 8)}–${tsShort(d.coveredToNs).slice(0, 8)} (${span}, en yeni uç)`;
}

// templatesSinceRung — v0.10.310: /api/logs/templates `since` sunucu cache
// anahtarına girer → pencere başlangıcı "şimdi"ye göre rung'lanır
// (1h/6h/24h/168h/720h). Kalıcı tablo last_seen ile süzülür; geçmiş bir
// pencere için de "şimdi − from" doğru üst sınırdır. from yoksa 24h.
export function templatesSinceRung(fromNs?: number, nowMs = Date.now()): string {
  if (!fromNs) return '24h';
  const h = (nowMs - fromNs / 1e6) / 3.6e6;
  if (h <= 1) return '1h';
  if (h <= 6) return '6h';
  if (h <= 24) return '24h';
  if (h <= 168) return '168h';
  return '720h';
}

export type PanelTab = 'patterns' | 'templates';
const TAB_KEY = 'logs.patterns.tab';
const TEMPLATES_LIMIT = 200;

const COLS: DataTableColumn<LogPatternGroup>[] = [
  { id: 'template', label: 'Desen', sortValue: r => r.template, naturalDir: 'asc', flex: true },
  { id: 'count', label: 'Sayı', sortValue: r => r.count, numeric: true, width: 110 },
  { id: 'severity', label: 'Seviye', sortValue: r => r.severity, numeric: true, width: 84 },
  { id: 'services', label: 'Servisler', sortValue: r => r.services.join(','), naturalDir: 'asc', width: 170 },
  { id: 'lastSeen', label: 'Son', sortValue: r => r.lastSeen, numeric: true, width: 96 },
  { id: 'act', label: '', sortValue: () => 0, width: 64 },
];

const TCOLS: DataTableColumn<LogTemplate>[] = [
  { id: 'template', label: 'Şablon', sortValue: r => r.template, naturalDir: 'asc', flex: true },
  { id: 'totalCount', label: 'Toplam', sortValue: r => r.totalCount, numeric: true, width: 110 },
  { id: 'services', label: 'Servisler', sortValue: r => r.services.join(','), naturalDir: 'asc', width: 170 },
  { id: 'firstSeen', label: 'İlk', sortValue: r => r.firstSeen, numeric: true, width: 96 },
  { id: 'lastSeen', label: 'Son', sortValue: r => r.lastSeen, numeric: true, width: 96 },
  { id: 'act', label: '', sortValue: () => 0, width: 64 },
];

const cellEllipsis = { fontSize: 11.5, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' } as const;
const cellServices = { fontSize: 11, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' } as const;

export function LogPatternsPanel({ params, open, onSearch, tab: tabProp, onTab }: {
  params: LogsParams;
  /** v0.10.448 (C8) — sekme dışarıdan (URL) sürülebilir; verilmezse localStorage. */
  tab?: PanelTab;
  onTab?: (t: PanelTab) => void;
  open: boolean;
  onSearch: (query: string) => void;
}) {
  const [localTab, setLocalTab] = useState<PanelTab>(() => (getRaw(TAB_KEY) === 'templates' ? 'templates' : 'patterns'));
  const tab = tabProp ?? localTab;
  const switchTab = (t: PanelTab) => { setRaw(TAB_KEY, t); setLocalTab(t); onTab?.(t); };
  const since = useMemo(() => templatesSinceRung(params.from), [params.from]);

  const q = useLogsPatterns({ ...params, limit: 50 }, open && tab === 'patterns');
  const tq = useLogsTemplates(
    { since, limit: TEMPLATES_LIMIT, sort: 'last_seen', service: params.service || undefined },
    open && tab === 'templates',
  );
  const rows = useMemo(() => q.data?.groups ?? [], [q.data]);
  const trows = useMemo(() => tq.data ?? [], [tq.data]);
  const dt = useDataTable<LogPatternGroup>({
    storageKey: 'logs-patterns',
    columns: COLS,
    rows,
    initialSort: { id: 'count', dir: 'desc' },
    onOpen: r => { if (r.query) onSearch(r.query); },
  });
  const tdt = useDataTable<LogTemplate>({
    storageKey: 'logs-templates',
    columns: TCOLS,
    rows: trows,
    initialSort: { id: 'lastSeen', dir: 'desc' },
    onOpen: r => { if (r.query) onSearch(r.query); },
  });
  if (!open) return null;
  const d = q.data;
  const maxCount = rows.reduce((m, r) => Math.max(m, r.count), 0);
  const maxTotal = trows.reduce((m, r) => Math.max(m, r.totalCount), 0);
  const rowStyle = (has: boolean) => ({ cursor: has ? 'pointer' : 'default', contentVisibility: 'auto', containIntrinsicSize: '0 26px' } as const);
  return (
    <div className="card lp-panel" style={{ padding: '10px 12px', marginBottom: 10 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 6 }}>
        <TabStrip ariaLabel="Log desenleri paneli" value={tab} onChange={switchTab} tabs={[
          { key: 'patterns', label: 'Desenler', title: 'Penceredeki mesajlar imzaya göre gruplanır (örneklemeli)' },
          { key: 'templates', label: 'Şablonlar', title: "Drain templater'ın kalıcı şablonları (5 dk'da ≤1000 satır örneklenir)" },
        ]} />
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          {tab === 'patterns' ? (d ? (
            <>
              {d.sampled.toLocaleString()} örnek satır{d.truncated ? ` (tavan ${d.cap.toLocaleString()})` : ''}
              {' · '}pencere toplamı {d.total.toLocaleString()}{' · '}{d.distinct} desen
              {coveredLabel(d) && <span title="Tavan doldu: sayımlar yalnız bu alt pencereyi anlatır, seçili pencerenin tamamını değil (v0.10.441)">{' · '}{coveredLabel(d)}</span>}
              {d.degraded && <span className="badge b-warn" style={{ marginLeft: 6 }}>{d.reason ?? 'degraded'}</span>}
            </>
          ) : 'sayımlar en yeni örnek satırlara göredir') : (
            <>
              {trows.length} kalıcı şablon{trows.length >= TEMPLATES_LIMIT ? ` (tavan ${TEMPLATES_LIMIT})` : ''}
              {' · '}son {since} içinde görülen{params.service ? ` · ${params.service}` : ''}
              {' · '}toplam = 5 dk'da ≤1000 satırlık örneklemeden biriken gözlem, pencere sayımı değil
            </>
          )}
        </span>
      </div>
      {tab === 'patterns' && (
        <>
          {q.isPending && <Spinner label="Desenler çıkarılıyor…" />}
          {q.isError && <Empty icon="⚠" title="Desenler alınamadı" compact>{q.error instanceof Error ? q.error.message : ''}</Empty>}
          {d && rows.length === 0 && !q.isPending && <Empty icon="≡" title="Bu pencerede desen yok" compact />}
          {rows.length > 0 && (
            <div className="table-wrap is-fit">
              <table style={{ tableLayout: 'fixed', width: '100%' }}>
                <DataTableColgroup dt={dt} />
                <DataTableHead dt={dt} />
                <tbody>
                  {dt.sortedRows.map(r => {
                    const share = maxCount > 0 ? (r.count / maxCount) * 100 : 0;
                    return (
                      <tr key={r.hash} className="lp-row" title={r.sample}
                        {...rowActivation(() => { if (r.query) onSearch(r.query); })}
                        onKeyDown={e => { if (e.key === 'Enter' && r.query) { e.preventDefault(); onSearch(r.query); } }}
                        style={rowStyle(!!r.query)}>
                        <td className="mono" style={cellEllipsis}>{r.template}</td>
                        <td className="num">
                          <span className="lp-bar" style={{ width: `${share}%` }} aria-hidden="true" />
                          <span style={{ position: 'relative' }}>{r.count.toLocaleString()}</span>
                        </td>
                        <td><span className={sevClass(r.severity)}>{r.severityText || sevName(r.severity)}</span></td>
                        <td className="mono" style={cellServices} title={r.services.join(', ')}>
                          {r.services.join(', ')}{r.serviceCount > r.services.length ? ` +${r.serviceCount - r.services.length}` : ''}
                        </td>
                        <td className="num" style={{ fontSize: 11, color: 'var(--text2)' }} title={tsLong(r.lastSeen)}>{agoLabel(r.lastSeen)}</td>
                        <td>
                          {r.query && (
                            <Button variant="secondary" size="xs" className="lp-search"
                              title={`Ara: ${r.query}`}
                              onClick={e => { e.stopPropagation(); onSearch(r.query); }}>Ara</Button>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
      {tab === 'templates' && (
        <>
          {tq.isPending && <Spinner label="Şablonlar alınıyor…" />}
          {tq.isError && <Empty icon="⚠" title="Şablonlar alınamadı" compact>{tq.error instanceof Error ? tq.error.message : ''}</Empty>}
          {tq.data && trows.length === 0 && !tq.isPending && (
            <Empty icon="⌗" title="Bu aralıkta kalıcı şablon yok" compact>
              Templater 5 dakikada bir örnekler; yeni kurulumda ilk şablonlar birkaç dakika sonra görünür.
            </Empty>
          )}
          {trows.length > 0 && (
            <div className="table-wrap is-fit">
              <table style={{ tableLayout: 'fixed', width: '100%' }}>
                <DataTableColgroup dt={tdt} />
                <DataTableHead dt={tdt} />
                <tbody>
                  {tdt.sortedRows.map(r => {
                    const share = maxTotal > 0 ? (r.totalCount / maxTotal) * 100 : 0;
                    return (
                      <tr key={r.id} className="lp-row" title={r.sample}
                        {...rowActivation(() => { if (r.query) onSearch(r.query); })}
                        onKeyDown={e => { if (e.key === 'Enter' && r.query) { e.preventDefault(); onSearch(r.query); } }}
                        style={rowStyle(!!r.query)}>
                        <td className="mono" style={cellEllipsis}>
                          {r.exceptionType && <span className="badge b-err" style={{ marginRight: 6 }}>{r.exceptionType}</span>}
                          {r.template}
                        </td>
                        <td className="num">
                          <span className="lp-bar" style={{ width: `${share}%` }} aria-hidden="true" />
                          <span style={{ position: 'relative' }}>{r.totalCount.toLocaleString()}</span>
                        </td>
                        <td className="mono" style={cellServices} title={r.services.join(', ')}>{r.services.join(', ')}</td>
                        <td className="num" style={{ fontSize: 11, color: 'var(--text2)' }} title={tsLong(r.firstSeen)}>{agoLabel(r.firstSeen)}</td>
                        <td className="num" style={{ fontSize: 11, color: 'var(--text2)' }} title={tsLong(r.lastSeen)}>{agoLabel(r.lastSeen)}</td>
                        <td>
                          {r.query && (
                            <Button variant="secondary" size="xs" className="lp-search"
                              title={`Ara: ${r.query}`}
                              onClick={e => { e.stopPropagation(); onSearch(r.query); }}>Ara</Button>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </div>
  );
}
