import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { Chip } from '@/components/ui/Chip';
import { Spinner, Empty } from '@/components/Spinner';
import { DrawerSection } from '@/components/ui/Drawer';
import { RenderedMarkdown } from '@/components/Markdown';
import { useAuth } from '@/components/AuthProvider';
import { api } from '@/lib/api';
import { chartScopeLabel, type ChartScope } from '@/lib/aiSubject';
import { operationTracesHref, tracesPivotHref } from '@/lib/pivotHref';
import { fmtClock } from '@/lib/utils';
import { logsHref } from '@/lib/logsUrl';
import type { ServiceChartsExplain, OpDelta } from '@/lib/types';

// ServiceChartsExplainBody — AI çekmecesinin `charts` öznesi için gövde
// (v0.9.1033, onaylı mockup). Üç bölüm: Ne oldu · İlişkili sinyaller ·
// Sonraki adım.
//
// Neden CopilotExplain DEĞİL: CopilotExplain yalnız `{explanation}`
// düzyazısını çizer. Bu yüzeyin sözleşmesi anlatım ile KANITI ayırmak —
// sinyal tablosu CH'den deterministik gelen `signals` alanından çizilir,
// yani model kotayı doldursa ya da saçmalasa bile tablo DOĞRU kalır.
// Aynı sebeple "Sonraki adım" linkleri de model metninden değil,
// sinyallerden üretilir (link uydurulamaz).
//
// Maliyet: fetch YALNIZ açılışta (bileşen çekmece açıkken mount olur),
// poll YOK, pencere odağında yeniden çekme YOK. "Yeniden üret" bilinçli
// olarak yeni bir üretim = yeni bir ai_calls satırı.
export function ServiceChartsExplainBody({ service, fromNs, toNs, scope, onAnswer }: {
  service: string;
  fromNs: number;
  toNs: number;
  scope: ChartScope;
  /** Çekmece-içi sohbetin bağlamı (v0.9.479 sözleşmesi). */
  onAnswer?: (text: string) => void;
}) {
  const { user } = useAuth();
  const q = useQuery<ServiceChartsExplain>({
    // Anahtar TÜM girdileri taşır: kapsam değişince YENİ üretim gerekir,
    // aynı servisin başka kartının cevabı gösterilemez.
    queryKey: ['explain-charts', service, fromNs, toNs, scope],
    queryFn: async () => {
      const r = await api.copilotExplainCharts(service, fromNs, toNs, scope);
      onAnswer?.(r.explanation);
      return r;
    },
    refetchOnWindowFocus: false,
    refetchOnMount: false,
    staleTime: Infinity,
    retry: false,
  });

  const windowLabel = `${fmtClock(fromNs / 1e6)} – ${fmtClock(toNs / 1e6)}`;

  return (
    <div style={{ paddingTop: 8 }}>
      {/* Meta şeridi — hangi servis, hangi pencere, hangi kapsam.
          Paylaşılan bir linkte cevabın NEYİ anlattığı buradan okunur. */}
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, marginBottom: 12 }}>
        <Chip pill>{service}</Chip>
        <Chip pill>{windowLabel}</Chip>
        <Chip pill>{chartScopeLabel(scope)}</Chip>
        {/* Viewer çekmeceyi AÇAR ve OKUR — rol kısıtı bir engel değil,
            bir bilgi. Yalnız viewer'a gösterilir: admin/editor için
            "salt-okur" yazmak yanlış olurdu (yazacak bir şey yok, ama
            rolleri de bu değil). */}
        {user?.role === 'viewer' && <Chip pill>viewer · salt-okur</Chip>}
      </div>

      {q.isPending ? (
        <div style={{ display: 'grid', placeItems: 'center', padding: '28px 0', gap: 8 }}>
          <Spinner />
          <div style={{ fontSize: 11, color: 'var(--text3)' }}>sinyaller toplandı, model yazıyor…</div>
        </div>
      ) : q.isError ? (
        <Empty icon="✨" title="AI özeti üretilemedi" compact
          action={<Button variant="secondary" size="sm" onClick={() => void q.refetch()}>↺ Yeniden dene</Button>}>
          {q.error instanceof Error ? q.error.message : 'Copilot yanıt vermedi.'}
        </Empty>
      ) : (
        <ChartsAnswer data={q.data} service={service} fromNs={fromNs} toNs={toNs}
          busy={q.isFetching} onRegenerate={() => void q.refetch()} />
      )}
    </div>
  );
}

function ChartsAnswer({ data, service, fromNs, toNs, busy, onRegenerate }: {
  data: ServiceChartsExplain;
  service: string;
  fromNs: number;
  toNs: number;
  busy: boolean;
  onRegenerate: () => void;
}) {
  const sg = data.signals;
  const hasSignals = !!sg.deploy || !!sg.problems?.length
    || !!sg.anomalies?.length || !!sg.opDeltas?.length || sg.otherOps > 0;
  // İlk (en kötü) operasyon "Sonraki adım"ın hedefi: hatalı izleri
  // operasyona daraltmak, servis geneline bakmaktan çok daha hızlı.
  const topOp = sg.opDeltas?.[0]?.name;

  return (
    <>
      <DrawerSection title="Ne oldu">
        {data.explanation.trim()
          ? <RenderedMarkdown text={data.explanation} />
          : (
            // v0.9.1034 — anlatım düşse bile istek 200 döner ve KANIT gelir
            // (backend buildChartsResult). Boş bir panel yerine neden
            // yazılmadığını söyleyip aşağıdaki ölçülmüş sinyallere yolluyoruz.
            <div style={{ fontSize: 12, color: 'var(--text3)' }}>
              {data.error
                ? `Anlatım üretilemedi (${data.error}).`
                : 'Model boş yanıt döndürdü.'}
              {' '}Aşağıdaki sinyaller yine de ölçülmüş veridir.
            </div>
          )}
      </DrawerSection>

      {hasSignals && (
        <DrawerSection title="İlişkili sinyaller">
          <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 12 }}>
            <tbody>
              {sg.deploy && (
                <SigRow label={sg.deploy.kind === 'deploy' ? 'deploy' : 'rollout'}>
                  {fmtClock(sg.deploy.timeUnixNs / 1e6)}
                  {sg.deploy.versionAfter
                    ? ` · ${sg.deploy.versionBefore || '?'} → ${sg.deploy.versionAfter}`
                    : ''}
                  {` · ${sg.deploy.podsReplaced} pod`}
                </SigRow>
              )}
              {sg.problems?.map(p => (
                <SigRow key={p.id} label="problem">
                  <Link style={linkStyle} to={`/inbox?problem=${encodeURIComponent(p.id)}`}>
                    {p.priority || p.severity.toUpperCase()} · {p.title}
                  </Link>
                  <span style={{ color: 'var(--text3)' }}>
                    {' '}— {p.metric} {p.value.toFixed(2)} / eşik {p.threshold.toFixed(2)}
                  </span>
                </SigRow>
              ))}
              {sg.anomalies?.map(a => (
                <SigRow key={a.id} label="anomali">
                  {a.pattern}{' '}
                  <span className="mono" style={{ color: 'var(--err)' }}>
                    {a.peakRatio.toFixed(1)}×
                  </span>
                  <span style={{ color: 'var(--text3)' }}> · {a.kind} · {a.status}</span>
                </SigRow>
              ))}
              {(sg.opDeltas?.length || sg.otherOps > 0) && (
                <SigRow label="operasyon">
                  {sg.opDeltas?.map(d => <OpDeltaRow key={d.name} d={d} />)}
                  {sg.otherOps > 0 && (
                    <div style={{ color: 'var(--text3)' }}>
                      diğer {sg.otherOps} operasyon: kayda değer değişim yok
                    </div>
                  )}
                </SigRow>
              )}
            </tbody>
          </table>
        </DrawerSection>
      )}

      <DrawerSection title="Sonraki adım">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          {/* Pivot linkleri pencereyi HER ZAMAN taşır (pivotHref'in var
              oluş sebebi): penceresiz bir pivot boş liste döndürür ve bu
              "böyle bir trace yok" gibi okunur. */}
          <CtaLink
            to={topOp
              ? operationTracesHref({
                  window: { fromNs, toNs }, operation: topOp,
                  service, hasError: true,
                })
              : tracesPivotHref({
                  window: { fromNs, toNs }, service, hasError: true,
                  view: 'list', rootOnly: false,
                })}
            title="Hatalı izleri aç"
            note={`/traces · ${topOp ?? 'servis geneli'} · hasError`} />
          <CtaLink
            to={logsHref({ window: { fromNs, toNs }, service, severity: 13 })}
            title="Logları aç"
            note="/logs · ≥warn · aynı pencere" />
          {sg.problems?.[0] && (
            <CtaLink
              to={`/inbox?problem=${encodeURIComponent(sg.problems[0].id)}`}
              title="Problemi aç"
              note={`/inbox · ${sg.problems[0].title}`} />
          )}
        </div>
      </DrawerSection>

      {/* Altbilgi — dürüstlük satırı her yanıtta. /ai bu üretimin
          kaydını (surface=explain-charts) gösterir. */}
      <div style={{
        borderTop: '1px solid var(--border)', paddingTop: 8, marginTop: 4,
        display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap',
        fontSize: 10.5, color: 'var(--text3)',
      }}>
        <span>AI çıkarımı — veriyle doğrula</span>
        <Link style={linkStyle} to="/ai">/ai kaydı</Link>
        <span style={{ flex: 1 }} />
        <Button variant="secondary" size="xs" onClick={onRegenerate} loading={busy}
          title="Yeni bir üretim çalıştır (yeni ai_calls kaydı)">
          ↺ Yeniden üret
        </Button>
      </div>
    </>
  );
}

// Link görsel dili: globals.css'te bir `.lnk` sınıfı YOK (mockup'ın kendi
// adıydı). Ev konvansiyonu inline accent rengi — DBQueriesPanel /
// ServiceCatalogPill emsali. Uydurma bir sınıf adı testte yakalanmasa
// sessizce STİLSİZ link bırakırdı.
const linkStyle: React.CSSProperties = {
  color: 'var(--accent2)', textDecoration: 'none',
};

function SigRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <tr>
      <td style={{
        padding: '4px 6px', borderBottom: '1px solid var(--border)',
        verticalAlign: 'top', color: 'var(--text2)', whiteSpace: 'nowrap', width: 86,
      }}>{label}</td>
      <td style={{
        padding: '4px 6px', borderBottom: '1px solid var(--border)',
        verticalAlign: 'top', minWidth: 0,
      }}>{children}</td>
    </tr>
  );
}

// OpDeltaRow — p95Ratio === 0 "ölçülemedi" demektir, "0×" DEĞİL
// (backend ratioText'i ile aynı kural; sessiz sıfır "gecikme sıfıra
// düştü" diye okunur).
function OpDeltaRow({ d }: { d: OpDelta }) {
  return (
    <div style={{ display: 'flex', gap: 6, alignItems: 'baseline', flexWrap: 'wrap' }}>
      <span style={{
        overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
        maxWidth: '100%',
      }} title={d.name}>{d.name}</span>
      {d.isNew ? (
        <span className="badge b-info">yeni</span>
      ) : (
        <span className="mono" style={{ fontSize: 11, color: 'var(--err)' }}>
          {d.p95Ratio > 0 ? `p95 ${d.p95Ratio.toFixed(2)}×` : 'p95 ölçülemedi'}
          {d.errDeltaPp > 0 ? `  err +${d.errDeltaPp.toFixed(2)}pp` : ''}
        </span>
      )}
      <span style={{ fontSize: 11, color: 'var(--text3)' }}>{d.calls} çağrı</span>
    </div>
  );
}

function CtaLink({ to, title, note }: { to: string; title: string; note: string }) {
  return (
    <Link to={to} style={{
      display: 'flex', justifyContent: 'space-between', gap: 8, alignItems: 'baseline',
      padding: '6px 9px', border: '1px solid var(--border)',
      borderRadius: 'var(--radius-sm)', background: 'var(--bg)',
      color: 'var(--text)', textDecoration: 'none', fontSize: 12,
    }}>
      <span>{title}</span>
      <span style={{
        color: 'var(--text3)', fontSize: 11, overflow: 'hidden',
        textOverflow: 'ellipsis', whiteSpace: 'nowrap',
      }} title={note}>{note}</span>
    </Link>
  );
}
