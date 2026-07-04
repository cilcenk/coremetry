import { useState } from 'react';
import { Link } from 'react-router-dom';
import { api } from '@/lib/api';
import { tsShort } from '@/lib/utils';
import { IconSparkles } from './icons';
import type { ServiceAnalysisResponse } from '@/lib/types';
import { Button } from '@/components/ui/Button';

// AIAnalysisPanel — embedded "AI ile analiz et" affordance for a service /
// incident / error-group (NOT logs). The operator clicks; the screen context
// (service + time range) is sent automatically — no free-text question. The
// configured model returns a strict Turkish verdict rendered as structured
// cards. All fixed UI copy is Turkish-in-code; the model produces only the
// analysis content. Colour is used ONLY for the güven (confidence) badge.

const GUVEN_BADGE: Record<string, string> = { yuksek: 'b-ok', orta: 'b-warn', dusuk: 'b-err' };
const GUVEN_LABEL: Record<string, string> = { yuksek: 'YÜKSEK GÜVEN', orta: 'ORTA GÜVEN', dusuk: 'DÜŞÜK GÜVEN' };

export function AIAnalysisPanel({ service, rangeS = 1800 }: { service: string; rangeS?: number }) {
  const [state, setState] = useState<'idle' | 'loading' | 'done' | 'error'>('idle');
  const [res, setRes] = useState<ServiceAnalysisResponse | null>(null);
  const [errMsg, setErrMsg] = useState('');
  const [showCtx, setShowCtx] = useState(false);
  const [fb, setFb] = useState<'up' | 'down' | null>(null);

  const run = async (refresh = false) => {
    setState('loading'); setErrMsg(''); setFb(null);
    try {
      const r = await api.analyzeService(service, rangeS, refresh);
      setRes(r); setState('done');
    } catch (e) {
      setErrMsg(e instanceof Error ? e.message : String(e)); setState('error');
    }
  };

  return (
    <div className="card" style={{ marginTop: 12 }}>
      <div className="ov-card-h">
        <h3 style={{ display: 'inline-flex', alignItems: 'center', gap: 7 }}>
          <IconSparkles size={14} /> AI Analizi
        </h3>
        <span className="ov-sub">{service}</span>
        {state === 'done' && (
          <span className="ov-right" style={{ display: 'inline-flex', gap: 10, alignItems: 'center' }}>
            {res?.cached && <span className="ov-sub" style={{ fontSize: 10 }}>önbellekten</span>}
            <Button variant="secondary" size="sm" onClick={() => run(true)}>Yeniden analiz et</Button>
          </span>
        )}
      </div>

      <div className="ov-card-b">
        {state === 'idle' && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
            <Button variant="primary" onClick={() => run(false)} leftIcon={<IconSparkles size={13} />}>
              AI ile analiz et
            </Button>
            <span style={{ fontSize: 12, color: 'var(--text3)' }}>
              Bu servisin RED metriklerini, baseline'ını, en sık hatalarını ve bağımlılıklarını yapay zekâ ile yorumlar.
            </span>
          </div>
        )}

        {state === 'loading' && <AnalysisSkeleton />}

        {state === 'error' && (
          <div style={{ fontSize: 13, color: 'var(--text2)' }}>
            <div style={{ color: 'var(--err)', marginBottom: 6 }}>Analiz başarısız.</div>
            <div style={{ fontSize: 12, color: 'var(--text3)', marginBottom: 10 }}>
              {/AI copilot not configured/i.test(errMsg)
                ? 'Bir AI modeli yapılandırılmamış. Settings → AI bölümünden kendi modelinizi tanımlayın.'
                : errMsg}
            </div>
            <button className="sec" onClick={() => run(false)}>Tekrar dene</button>
          </div>
        )}

        {state === 'done' && res && !res.parsed && (
          <div>
            {res.context && res.context.current.spans === 0 ? (
              <div style={{ fontSize: 13, color: 'var(--text3)' }}>Bu pencerede analiz edilecek telemetri yok.</div>
            ) : (
              <>
                <div style={{ fontSize: 12, color: 'var(--text3)', marginBottom: 8 }}>Model yapılandırılmış JSON döndürmedi — ham yanıt:</div>
                <pre style={{ margin: 0, fontSize: 11.5, color: 'var(--text2)', whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>{res.raw}</pre>
              </>
            )}
          </div>
        )}

        {state === 'done' && res?.parsed && res.analysis && (
          <Result res={res} fb={fb} setFb={setFb} showCtx={showCtx} setShowCtx={setShowCtx} />
        )}
      </div>
    </div>
  );
}

function Result({ res, fb, setFb, showCtx, setShowCtx }: {
  res: ServiceAnalysisResponse;
  fb: 'up' | 'down' | null;
  setFb: (v: 'up' | 'down' | null) => void;
  showCtx: boolean;
  setShowCtx: (v: boolean) => void;
}) {
  const a = res.analysis!;
  const ctx = res.context;
  const badge = GUVEN_BADGE[a.guven] ?? 'b-gray';
  const pc = res.postCheck;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
      {/* Güven badge + post-check chip */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
        <span className={`badge ${badge}`}>{GUVEN_LABEL[a.guven] ?? a.guven}</span>
        {pc && !pc.verified && (
          <span className="badge b-warn" title={pc.note}>⚠ doğrulanamadı: {pc.unknownServices.join(', ')}</span>
        )}
      </div>

      {/* Özet */}
      <Block title="Özet"><p style={pStyle}>{a.ozet}</p></Block>

      {/* Olası Neden */}
      <Block title="Olası Neden"><p style={pStyle}>{a.olasi_neden}</p></Block>

      {/* Kanıt — list + drill-down to related sample traces */}
      {a.kanit.length > 0 && (
        <Block title="Kanıt">
          <ul style={ulStyle}>
            {a.kanit.map((k, i) => <li key={i} style={liStyle}>{k}</li>)}
          </ul>
          {ctx && ctx.topErrors.some(e => e.sampleTraceId) && (
            <div style={{ marginTop: 8, display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'center' }}>
              <span style={{ fontSize: 11, color: 'var(--text3)' }}>İlgili izler:</span>
              {ctx.topErrors.filter(e => e.sampleTraceId).slice(0, 4).map(e => (
                <Link key={e.sampleTraceId} className="mono" style={evLink} to={`/trace?id=${e.sampleTraceId}`}
                  title={`${e.type || e.message} ×${e.count}`}>
                  {(e.type || e.message || 'trace').slice(0, 22)} →
                </Link>
              ))}
            </div>
          )}
        </Block>
      )}

      {/* Öneriler */}
      {a.oneriler.length > 0 && (
        <Block title="Öneriler">
          <ul style={ulStyle}>
            {a.oneriler.map((o, i) => <li key={i} style={liStyle}>{o}</li>)}
          </ul>
        </Block>
      )}

      {/* Footer: feedback + "Bağlamı gör" */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 14, flexWrap: 'wrap', borderTop: '1px solid var(--border)', paddingTop: 10 }}>
        <span style={{ fontSize: 11.5, color: 'var(--text3)' }}>Bu analiz yararlı mıydı?</span>
        <button className="sec" style={fbBtn(fb === 'up')} onClick={() => setFb('up')} aria-label="faydalı">👍</button>
        <button className="sec" style={fbBtn(fb === 'down')} onClick={() => setFb('down')} aria-label="faydasız">👎</button>
        {fb && <span style={{ fontSize: 11, color: 'var(--text3)' }}>Teşekkürler.</span>}
        <span style={{ flex: 1 }} />
        <Button variant="secondary" size="sm" onClick={() => setShowCtx(!showCtx)}>
          {showCtx ? 'Bağlamı gizle' : 'Bağlamı gör'}
        </Button>
      </div>

      {showCtx && ctx && <ContextView ctx={ctx} />}
    </div>
  );
}

// ContextView — the summarised data the model received ("Bağlamı gör").
function ContextView({ ctx }: { ctx: NonNullable<ServiceAnalysisResponse['context']> }) {
  const c = ctx.current, b = ctx.baseline;
  const row = (k: string, v: string) => (
    <div style={{ display: 'flex', justifyContent: 'space-between', gap: 12, padding: '2px 0' }}>
      <span style={{ color: 'var(--text3)' }}>{k}</span><span className="mono" style={{ color: 'var(--text2)' }}>{v}</span>
    </div>
  );
  return (
    <div style={{ background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 6, padding: 12, fontSize: 11.5 }}>
      <div style={{ fontWeight: 700, color: 'var(--text2)', marginBottom: 6 }}>Modele gönderilen özet</div>
      {row('rate', `${c.rate.toFixed(1)} req/s`)}
      {row('error', `${c.errorRate.toFixed(2)}% (${c.errorCount})`)}
      {row('p50 · p95 · p99', `${c.p50Ms.toFixed(0)} · ${c.p95Ms.toFixed(0)} · ${c.p99Ms.toFixed(0)} ms`)}
      {b.spans > 0 && row('baseline error · p99', `${b.errorRate.toFixed(2)}% · ${b.p99Ms.toFixed(0)} ms`)}
      {ctx.deploys.length > 0 && row('deploy(lar)', ctx.deploys.map(d => `${d.version} (${tsShort(d.timeUnixNs)})`).join(', '))}
      {ctx.topErrors.length > 0 && (
        <div style={{ marginTop: 6 }}>
          <div style={{ color: 'var(--text3)', marginBottom: 2 }}>en sık hatalar</div>
          {ctx.topErrors.map((e, i) => (
            <div key={i} className="mono" style={{ color: 'var(--text2)', display: 'flex', justifyContent: 'space-between', gap: 12 }}>
              <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{e.type || e.message}</span>
              <span>×{e.count}</span>
            </div>
          ))}
        </div>
      )}
      {(ctx.upstream.length > 0 || ctx.downstream.length > 0) &&
        row('komşular', `↘ ${ctx.upstream.join(', ') || '—'} | ↗ ${ctx.downstream.join(', ') || '—'}`)}
    </div>
  );
}

function Block({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div>
      <div style={{ fontSize: 10.5, fontWeight: 700, letterSpacing: '.5px', textTransform: 'uppercase', color: 'var(--text3)', marginBottom: 5 }}>{title}</div>
      {children}
    </div>
  );
}

function AnalysisSkeleton() {
  const bar = (w: string) => <div style={{ height: 11, width: w, background: 'var(--bg3)', borderRadius: 4, animation: 'pulse 1.2s ease-in-out infinite' }} />;
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
      <div style={{ height: 18, width: 110, background: 'var(--bg3)', borderRadius: 6, animation: 'pulse 1.2s ease-in-out infinite' }} />
      {bar('92%')}{bar('80%')}{bar('64%')}
      <div style={{ height: 8 }} />
      {bar('70%')}{bar('85%')}
      <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>Analiz ediliyor…</div>
    </div>
  );
}

const pStyle: React.CSSProperties = { margin: 0, fontSize: 13, lineHeight: 1.5, color: 'var(--text)' };
const ulStyle: React.CSSProperties = { margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 4 };
const liStyle: React.CSSProperties = { fontSize: 12.5, lineHeight: 1.45, color: 'var(--text2)' };
const evLink: React.CSSProperties = { fontSize: 11, color: 'var(--accent)', textDecoration: 'none', border: '1px solid var(--border)', borderRadius: 4, padding: '2px 7px', whiteSpace: 'nowrap' };
function fbBtn(active: boolean): React.CSSProperties {
  return { padding: '2px 8px', fontSize: 13, opacity: active ? 1 : 0.6, borderColor: active ? 'var(--accent)' : 'var(--border)' };
}
