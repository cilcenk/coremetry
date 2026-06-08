import { useState } from 'react';
import { api } from '@/lib/api';
import type { SystemAnalysis } from '@/lib/types';
import { Spinner } from '@/components/Spinner';
import { Button } from '@/components/ui/Button';

// SystemAnalysis (v0.8.75) — fleet-wide AI root-cause analysis. One button runs
// a SINGLE-SHOT analysis: the server snapshots ALL services' RED + open problems
// + active log/trace anomalies + the dependency topology, hands it to the model
// with the SRE-analyst prompt, and returns a strict-JSON verdict — system
// status, root cause, the affected service CHAIN, per-service findings, and
// recommended actions. No tool calling; the model reasons over the whole
// snapshot at once, so it can spot cascades (a slow DB dragging its callers).

const RANGES: { label: string; s: number }[] = [
  { label: '30dk', s: 1800 },
  { label: '1sa', s: 3600 },
  { label: '3sa', s: 10800 },
  { label: '12sa', s: 43200 },
];

const STATUS: Record<string, { label: string; bg: string; fg: string }> = {
  saglikli: { label: 'SAĞLIKLI', bg: 'var(--ok-soft, rgba(34,197,94,.15))', fg: 'var(--ok)' },
  bozulma:  { label: 'BOZULMA',  bg: 'var(--warn-soft, rgba(217,119,6,.15))', fg: 'var(--warn)' },
  kritik:   { label: 'KRİTİK',   bg: 'var(--err-soft, rgba(220,38,38,.15))', fg: 'var(--err)' },
};
const ONEM: Record<string, string> = { yuksek: 'var(--err)', orta: 'var(--warn)', dusuk: 'var(--text3)' };

export default function SystemAnalysisPage() {
  const [rangeS, setRangeS] = useState(1800);
  const [busy, setBusy] = useState(false);
  const [res, setRes] = useState<{ analysis: SystemAnalysis | null; raw: string; parsed: boolean } | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const run = async () => {
    setBusy(true); setErr(null); setRes(null);
    try {
      setRes(await api.copilotAnalyze(rangeS));
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'Analiz başarısız');
    } finally {
      setBusy(false);
    }
  };

  const a = res?.analysis;
  const st = a ? (STATUS[a.sistem_durumu] ?? STATUS.bozulma) : null;

  return (
    <div id="content">
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 14, flexWrap: 'wrap' }}>
        <h1 style={{ fontSize: 22, margin: 0, fontWeight: 700 }}>🔬 Sistem Analizi</h1>
        <span style={{ fontSize: 12, color: 'var(--text3)' }}>
          tüm servisleri birlikte değerlendirip kök-neden + etkilenen zinciri bulur
        </span>
        <span style={{ flex: 1 }} />
        <div className="segmented">
          {RANGES.map(r => (
            <button key={r.s} className={rangeS === r.s ? 'active' : ''} onClick={() => setRangeS(r.s)}>{r.label}</button>
          ))}
        </div>
        <Button variant="primary" onClick={run} disabled={busy}>
          {busy ? 'Analiz ediliyor…' : 'Analiz et ▶'}
        </Button>
      </div>

      {busy && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, color: 'var(--text3)', fontSize: 13, padding: 24 }}>
          <Spinner /> Sistem genelinde gözlemlenebilirlik verisi değerlendiriliyor…
        </div>
      )}
      {err && !busy && (
        <div style={{ color: 'var(--err)', fontSize: 13, padding: 12, border: '1px solid var(--border)', borderRadius: 8 }}>⚠ {err}</div>
      )}

      {!busy && res && !a && (
        <div style={{ background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 8, padding: 14 }}>
          <div style={{ fontSize: 12, color: 'var(--text3)', marginBottom: 6 }}>Model yapılandırılmış JSON üretmedi — ham çıktı:</div>
          <pre style={{ whiteSpace: 'pre-wrap', fontSize: 12.5, margin: 0, color: 'var(--text)' }}>{res.raw || '(boş)'}</pre>
        </div>
      )}

      {!busy && a && st && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          {/* Verdict header */}
          <div style={{ background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 10, padding: 16 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 10 }}>
              <span style={{
                fontSize: 12, fontWeight: 800, letterSpacing: '.5px', padding: '4px 12px',
                borderRadius: 6, background: st.bg, color: st.fg,
              }}>{st.label}</span>
              <span style={{ fontSize: 11, color: 'var(--text3)' }}>güven: <b style={{ color: 'var(--text2)' }}>{a.guven}</b></span>
            </div>
            <div style={{ fontSize: 14, lineHeight: 1.55, color: 'var(--text)' }}>{a.ozet}</div>
          </div>

          {/* Root cause */}
          <div style={{ background: 'var(--bg2)', border: '1px solid var(--border-strong)', borderLeft: '3px solid var(--err)', borderRadius: 10, padding: 16 }}>
            <div style={{ fontSize: 11, fontWeight: 700, textTransform: 'uppercase', letterSpacing: '.5px', color: 'var(--text3)', marginBottom: 6 }}>Kök neden</div>
            <div style={{ fontSize: 14, lineHeight: 1.55, color: 'var(--text)' }}>{a.kok_neden}</div>
            {a.etkilenen_zincir?.length > 0 && (
              <div style={{ marginTop: 12, display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                <span style={{ fontSize: 11, color: 'var(--text3)', marginRight: 4 }}>etkilenen zincir:</span>
                {a.etkilenen_zincir.map((svc, i) => (
                  <span key={i} style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                    {i > 0 && <span style={{ color: 'var(--text3)' }}>→</span>}
                    <span style={{ fontFamily: 'var(--mono, ui-monospace)', fontSize: 12, padding: '2px 8px', borderRadius: 6, background: 'var(--bg3)', color: 'var(--text)' }}>{svc}</span>
                  </span>
                ))}
              </div>
            )}
          </div>

          {/* Findings table */}
          {a.bulgular?.length > 0 && (
            <div style={{ background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 10, padding: 16 }}>
              <div style={{ fontSize: 11, fontWeight: 700, textTransform: 'uppercase', letterSpacing: '.5px', color: 'var(--text3)', marginBottom: 10 }}>Bulgular ({a.bulgular.length})</div>
              <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 12.5 }}>
                <thead>
                  <tr style={{ textAlign: 'left', color: 'var(--text3)', fontSize: 10.5, textTransform: 'uppercase', letterSpacing: '.4px' }}>
                    <th style={{ padding: '4px 8px 8px 0' }}>Servis</th>
                    <th style={{ padding: '4px 8px 8px 0' }}>Sorun</th>
                    <th style={{ padding: '4px 8px 8px 0' }}>Kanıt</th>
                    <th style={{ padding: '4px 0 8px 0', width: 70 }}>Önem</th>
                  </tr>
                </thead>
                <tbody>
                  {a.bulgular.map((f, i) => (
                    <tr key={i} style={{ borderTop: '1px solid var(--border)' }}>
                      <td style={{ padding: '8px 8px 8px 0', fontFamily: 'var(--mono, ui-monospace)', color: 'var(--text)', whiteSpace: 'nowrap', verticalAlign: 'top' }}>{f.servis}</td>
                      <td style={{ padding: '8px 8px 8px 0', color: 'var(--text)', verticalAlign: 'top' }}>{f.sorun}</td>
                      <td style={{ padding: '8px 8px 8px 0', color: 'var(--text2)', verticalAlign: 'top' }}>{f.kanit}</td>
                      <td style={{ padding: '8px 0 8px 0', verticalAlign: 'top' }}>
                        <span style={{ fontSize: 10.5, fontWeight: 700, color: ONEM[f.onem] ?? 'var(--text3)' }}>{(f.onem ?? '').toUpperCase()}</span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {/* Recommendations */}
          {a.oneriler?.length > 0 && (
            <div style={{ background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 10, padding: 16 }}>
              <div style={{ fontSize: 11, fontWeight: 700, textTransform: 'uppercase', letterSpacing: '.5px', color: 'var(--text3)', marginBottom: 8 }}>Öneriler</div>
              <ol style={{ margin: 0, paddingLeft: 20, fontSize: 13, lineHeight: 1.7, color: 'var(--text)' }}>
                {a.oneriler.map((o, i) => <li key={i}>{o}</li>)}
              </ol>
            </div>
          )}
        </div>
      )}

      {!busy && !res && !err && (
        <div style={{ color: 'var(--text3)', fontSize: 13, padding: 24, textAlign: 'center', border: '1px dashed var(--border)', borderRadius: 8 }}>
          Pencereyi seç ve <b>Analiz et</b>'e bas — sistem genelinde kök-neden raporu üretilir.
        </div>
      )}
    </div>
  );
}
