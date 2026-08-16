import { useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { Button } from '@/components/ui/Button';
import { IconSparkles } from '@/components/icons';
import { useDataTable, DataTableColgroup, DataTableHead } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { api } from '@/lib/api';
import { tsLong, fmtFixed } from '@/lib/utils';
import { serviceHref } from '@/lib/serviceHref';
import type { Problem, ChangedService, ExceptionGroup } from '@/lib/types';
import { PageShell } from '@/components/ui/PageShell';
import { AIFeedbackButtons } from '@/components/ai/AIFeedbackButtons';
import { RenderedMarkdown } from '@/components/Markdown';

// Shift.tsx — /shift vardiya özeti (v0.9.1072, Faz 3.2; mockup
// operatör-onaylı). Vardiya devri bugüne dek 4 sayfa gezilerek
// yapılıyordu; burada üç tablo + tek ✨: pencerede açılan/çözülen
// problemler, en çok kötüleşen servisler, pencerede doğan exception
// imzaları. Liste-üstü KPI/grafik bloğu BİLİNÇLİ yok (v0.9.834-835
// operatör kararı — tablo+yoğunluk dili).
//
// Pencere ?w= URL'de (rung'lu — sunucu tek otorite; SavedViewsBar yok,
// paylaşılabilir link yeter). AI anlatımı inline panelde (SlowQueries
// emsali) — tek-atış sonuç, çekmece sohbeti değil.

const WINDOWS = ['8h', '12h', '24h'] as const;

const PROBLEM_COLS: DataTableColumn<Problem>[] = [
  { id: 'prio', label: 'Öncelik', sortValue: p => p.priority ?? 'P9', width: 80 },
  { id: 'service', label: 'Servis', sortValue: p => p.service, width: 180 },
  { id: 'rule', label: 'Kural', sortValue: p => p.ruleName, width: 240 },
  { id: 'opened', label: 'Açıldı', sortValue: p => p.startedAt, numeric: true, width: 150 },
  { id: 'state', label: 'Durum', sortValue: p => (p.resolvedAt ? 1 : 0), width: 140 },
  { id: 'rc', label: 'Kök neden', sortValue: p => p.rootCause?.topSuspect ?? '', width: 220 },
];

const WORSE_COLS: DataTableColumn<ChangedService>[] = [
  { id: 'service', label: 'Servis', sortValue: c => c.service, width: 200 },
  { id: 'err', label: 'Hata oranı', sortValue: c => c.errDeltaPct, numeric: true, width: 190 },
  { id: 'p99', label: 'P99', sortValue: c => c.p99DeltaPct, numeric: true, width: 220 },
  { id: 'rate', label: 'Rate', sortValue: c => c.rateDeltaPct, numeric: true, width: 170 },
  { id: 'score', label: 'Skor', sortValue: c => c.score, numeric: true, width: 80 },
];

const EXC_COLS: DataTableColumn<ExceptionGroup>[] = [
  { id: 'type', label: 'Tip', sortValue: g => g.type, width: 320 },
  { id: 'service', label: 'Servis', sortValue: g => g.service, width: 180 },
  { id: 'first', label: 'İlk görülme', sortValue: g => g.firstSeen, numeric: true, width: 150 },
  { id: 'occ', label: 'Adet', sortValue: g => g.occurrences, numeric: true, width: 90 },
];

export default function ShiftPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const rawW = searchParams.get('w') ?? '';
  const w = (WINDOWS as readonly string[]).includes(rawW) ? rawW : '12h';
  const setW = (next: string) => {
    setSearchParams(prev => {
      const p = new URLSearchParams(prev);
      if (next === '12h') p.delete('w'); else p.set('w', next);
      return p;
    }, { replace: true });
  };

  const q = useQuery({
    queryKey: ['shift', w],
    queryFn: () => api.shiftSummary(w),
    staleTime: 55_000, // sunucu cache TTL'i 60s — çift fetch yok
  });

  // ✨ tek-atış anlatım — inline panel (SlowQueries emsali). Fetch yalnız
  // tıkla (ES/LLM maliyet disiplini: hiçbir şey önceden istenmez).
  // v0.9.1121 (Faz 0.3b) — xid: cevabın ai_calls kimliği; 👍/👎 buna asılı.
  const [ai, setAi] = useState<{ busy: boolean; text: string | null; err: string | null; xid?: string }>({ busy: false, text: null, err: null });
  const explain = async () => {
    setAi({ busy: true, text: null, err: null });
    try {
      const r = await api.explainShift(w);
      setAi({ busy: false, text: r.explanation, err: null, xid: r.exchangeId });
    } catch (e) {
      setAi({ busy: false, text: null, err: e instanceof Error ? e.message : 'Anlatım alınamadı' });
    }
  };

  const problems = useMemo(() => q.data?.problems ?? [], [q.data]);
  const worsened = useMemo(() => q.data?.worsened ?? [], [q.data]);
  const excs = useMemo(() => q.data?.newExceptions ?? [], [q.data]);

  const probT = useDataTable({ rows: problems, columns: PROBLEM_COLS, storageKey: 'shift-problems', initialSort: { id: 'opened', dir: 'desc' } });
  const worseT = useDataTable({ rows: worsened, columns: WORSE_COLS, storageKey: 'shift-worsened', initialSort: { id: 'score', dir: 'desc' } });
  const excT = useDataTable({ rows: excs, columns: EXC_COLS, storageKey: 'shift-exceptions', initialSort: { id: 'occ', dir: 'desc' } });

  return (
    <PageShell>
      <Topbar title="Vardiya özeti" />
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, margin: '10px 0 16px' }}>
        <span className="segmented sg-sm">
          {WINDOWS.map(win => (
            <button key={win} type="button" className={w === win ? 'active' : ''}
              onClick={() => setW(win)}>Son {win}</button>
          ))}
        </span>
        <Button variant="secondary" size="sm" onClick={explain} disabled={ai.busy}
          title="Bu pencerenin hazır kanıt paketini AI anlatır">
          <IconSparkles /> Vardiyayı anlat
        </Button>
      </div>

      {/* Faz 0.5 — model markdown üretiyor; burası ham basıyordu, yani
          "kalın" işaretleri ekranda yıldızlarıyla görünüyordu: v0.9.641'in
          CopilotExplain'de düzelttiği kusurun aynısı, üç yüzey daha.
          pre-wrap KALKTI — RenderedMarkdown kendi p/ul bloklarını kuruyor,
          ikisi birlikte satır aralarını ikiye katlıyor. */}
      {(ai.busy || ai.text || ai.err) && (
        <div style={{
          padding: '12px 14px', marginBottom: 16, borderRadius: 6,
          background: 'var(--bg2)', border: '1px solid var(--border)',
          fontSize: 12.5, lineHeight: 1.55,
        }}>
          <div style={{ fontSize: 10.5, fontWeight: 700, letterSpacing: 0.4, color: 'var(--text2)', marginBottom: 6 }}>
            AI VARDİYA ANLATIMI
          </div>
          {ai.busy && <Spinner />}
          {ai.err && <span style={{ color: 'var(--err)' }}>{ai.err}</span>}
          {ai.text && <RenderedMarkdown text={ai.text} />}
          {/* v0.9.1121 (Faz 0.3b) — 👍/👎; kimlik yoksa çizilmez. */}
          <div><AIFeedbackButtons exchangeId={ai.xid} /></div>
        </div>
      )}

      {q.isPending && <Spinner />}
      {q.isError && (
        <Empty icon="✗" title="Vardiya özeti yüklenemedi">
          /api/shift isteği hata verdi — sunucu logunu kontrol edin.
        </Empty>
      )}
      {q.data && (
        <>
          <Section title={`Pencerede açılan problemler (${q.data.problemsTotal})`}
            sub={q.data.problemsTotal > problems.length
              ? `en yeni ${problems.length} gösteriliyor — kapananlar soluk`
              : 'kapananlar soluk — gece ne oldu, ne kendi kendine düzeldi'}>
            {problems.length === 0
              ? <Empty icon="✓" title="Pencerede problem açılmadı">Sakin bir vardiya.</Empty>
              : (
                <table style={{ tableLayout: 'fixed', width: '100%' }}>
                  <DataTableColgroup dt={probT} />
                  <DataTableHead dt={probT} />
                  <tbody>
                    {probT.sortedRows.map((p: Problem) => (
                      <tr key={p.id} style={p.resolvedAt ? { opacity: 0.45 } : undefined}>
                        <td>{p.priority && <span className={`badge ${p.priority === 'P1' ? 'b-err' : p.priority === 'P2' ? 'b-warn' : 'b-info'}`}>{p.priority}</span>}</td>
                        <td><Link to={serviceHref(p.service)}>{p.service}</Link></td>
                        <td><Link to={`/problems?problem=${encodeURIComponent(p.id)}`}>{p.ruleName}</Link></td>
                        <td className="mono">{tsLong(p.startedAt)}</td>
                        <td>{p.resolvedAt ? `kapandı ${tsLong(p.resolvedAt)}` : p.status}</td>
                        <td style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                          title={p.rootCause?.topSuspect ?? ''}>
                          {p.rootCause?.topSuspect
                            ? `${p.rootCause.topSuspect} (%${Math.round((p.rootCause.confidence ?? 0) * 100)})`
                            : p.recentDeploy ? `deploy ${p.recentDeploy.version}` : '—'}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
          </Section>

          <Section title="En çok kötüleşen servisler"
            sub="pencere vs önceki eş-boy pencere (MV kıyası)">
            {worsened.length === 0
              ? <Empty icon="✓" title="Kayda değer kötüleşme yok">Önceki pencereye göre sapan servis bulunmadı.</Empty>
              : (
                <table style={{ tableLayout: 'fixed', width: '100%' }}>
                  <DataTableColgroup dt={worseT} />
                  <DataTableHead dt={worseT} />
                  <tbody>
                    {worseT.sortedRows.map((c: ChangedService) => (
                      <tr key={c.service}>
                        <td><Link to={serviceHref(c.service)}>{c.service}</Link></td>
                        <td>{deltaCell(c.baselineErrorRate * 100, c.currentErrorRate * 100, '%', c.errDeltaPct)}</td>
                        <td>{deltaCell(c.baselineP99Ms, c.currentP99Ms, 'ms', c.p99DeltaPct)}</td>
                        <td>{deltaCell(c.baselineRate, c.currentRate, '/s', c.rateDeltaPct)}</td>
                        <td className="mono">{fmtFixed(c.score, 1)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
          </Section>

          <Section title={`Yeni exception imzaları (${q.data.newExceptionsTotal})`}
            sub={q.data.newExceptionsTotal > excs.length
              ? `ilk görülme bu pencerede — en yoğun ${excs.length} gösteriliyor`
              : 'ilk görülme bu pencerede'}>
            {excs.length === 0
              ? <Empty icon="✓" title="Yeni imza yok">Bu pencerede ilk kez görülen exception grubu bulunmadı.</Empty>
              : (
                <table style={{ tableLayout: 'fixed', width: '100%' }}>
                  <DataTableColgroup dt={excT} />
                  <DataTableHead dt={excT} />
                  <tbody>
                    {excT.sortedRows.map((g: ExceptionGroup) => (
                      <tr key={g.fingerprint}>
                        <td className="mono" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={g.type}>
                          <Link to={`/problems?exc=${encodeURIComponent(g.fingerprint)}`}>{g.type}</Link>
                        </td>
                        <td><Link to={serviceHref(g.service)}>{g.service}</Link></td>
                        <td className="mono">{tsLong(g.firstSeen)}</td>
                        <td className="mono">{g.occurrences}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
          </Section>
        </>
      )}
    </PageShell>
  );
}

function Section({ title, sub, children }: { title: string; sub?: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 22 }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 8 }}>
        <span style={{ fontSize: 11, fontWeight: 700, letterSpacing: 0.4, textTransform: 'uppercase', color: 'var(--text2)' }}>{title}</span>
        {sub && <span style={{ fontSize: 10.5, color: 'var(--text3)' }}>{sub}</span>}
      </div>
      {children}
    </div>
  );
}

function deltaCell(base: number, cur: number, unit: string, deltaPct: number) {
  if (base === 0 && cur === 0) return <span style={{ color: 'var(--text3)' }}>—</span>;
  const worse = cur > base;
  return (
    <span style={{ color: worse ? 'var(--err)' : 'var(--ok)' }} className="mono">
      {fmtFixed(base, 1)}{unit} → {fmtFixed(cur, 1)}{unit} ({deltaPct > 0 ? '+' : ''}{fmtFixed(deltaPct, 0)}%) {worse ? '↑' : '↓'}
    </span>
  );
}
