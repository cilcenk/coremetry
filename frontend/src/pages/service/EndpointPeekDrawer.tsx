import { useEffect } from 'react';
import { Link } from 'react-router-dom';
import { Sparkline } from '@/components/Sparkline';
import { Button } from '@/components/ui/Button';
import type { EndpointRow, TimeRange } from '@/lib/types';
import { encodeRange } from '@/lib/urlState';
import { encodeEndpointParam } from '@/pages/endpoints/endpointParam';

// EndpointPeekDrawer — v0.9.379, redesign D3 (mockup af7419e5).
// Top endpoints satırına tık = sağdan 380px peek: mini RED üçlüsü +
// exemplar trace linkleri + tam görünüm kapıları. SIFIR yeni fetch:
// bundle'ın endpoint satırı sparkline/errorsSparkline/p99Sparkline
// (30-bucket, v0.5.371/387) ve slowTraceId/errorTraceId (v0.9.310 MV
// exemplar state'leri) alanlarını ZATEN taşıyor — drawer yalnız çizer.
// ADDITIVE sözleşme: drawer'ı kapatınca hiçbir şey değişmemiş olur;
// eski doğrudan-/traces davranışı drawer İÇİNDEKİ "Traces →" linki
// olarak yaşar. Kapatma: ✕, Esc, dışarı tık (arka fon).

function MiniRed({ label, values, color }: { label: string; values?: number[]; color: string }) {
  if (!values || values.length < 2) {
    return (
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 10, color: 'var(--text3)' }}>{label}</div>
        <div style={{ fontSize: 11, color: 'var(--text3)', fontStyle: 'italic' }}>seri yok</div>
      </div>
    );
  }
  return (
    <div style={{ marginBottom: 8 }}>
      <div style={{ fontSize: 10, color: 'var(--text3)', marginBottom: 2 }}>{label}</div>
      <Sparkline values={values} width={330} height={34} color={color} />
    </div>
  );
}

export function EndpointPeekDrawer({ service, range, row, onClose }: {
  service: string; range: TimeRange; row: EndpointRow; onClose: () => void;
}) {
  const rangeParam = encodeRange(range);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const tracesHref =
    `/traces?service=${encodeURIComponent(service)}&search=${encodeURIComponent(row.path)}&range=${rangeParam}&view=list&rootOnly=false`;

  // v0.9.819 — "Tam analiz →" KÖPRÜSÜ. Bu peek, bundle'ın zaten taşıdığı
  // alanları çiziyor (sıfır fetch) ve orada BİTİYORDU: gecikme dağılımı,
  // durum kırılımı, rota üstündeki exception'lar, başarısız trace'ler ve
  // split-by — hepsi /endpoints çekmecesinde yaşıyor ve buradan
  // görünmüyordu. Operatör "peki bu neden yavaş" diye sorduğunda gidecek
  // yeri yoktu.
  //
  // Hedef, tam çekmecenin KENDİ codec'i (?endpoint=, v0.8.360): yeni bir
  // URL şeması uydurulmuyor, aynı alan sınırları (alanlar önce
  // URI-encode) ve aynı sig sözleşmesi. sig=false: /service Top
  // endpoints kartı ham rotayı listeliyor, "group by shape" onun bir
  // kipi değil — sig:true göndermek alıcıya farklı bir kümeyi açardı.
  const fullAnalysisHref =
    `/endpoints?range=${rangeParam}` +
    `&service=${encodeURIComponent(service)}` +
    `&endpoint=${encodeURIComponent(encodeEndpointParam({ service, path: row.path, sig: false }))}`;

  return (
    <>
      {/* arka fon — tık = kapat */}
      <div onClick={onClose} style={{
        position: 'fixed', inset: 0, zIndex: 60, background: 'rgba(0,0,0,.28)',
      }} />
      <div role="dialog" aria-label={`Endpoint peek: ${row.path}`} style={{
        position: 'fixed', top: 0, right: 0, bottom: 0, width: 380, zIndex: 61,
        background: 'var(--bg1)', borderLeft: '1px solid var(--border)',
        boxShadow: '-16px 0 40px rgba(0,0,0,.35)', padding: '14px 16px',
        overflowY: 'auto',
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 2 }}>
          <span className="mono" title={row.path} style={{
            fontWeight: 700, fontSize: 13, overflow: 'hidden',
            textOverflow: 'ellipsis', whiteSpace: 'nowrap',
          }}>{row.path}</span>
          <Button variant="ghost" size="sm" onClick={onClose} style={{ marginLeft: 'auto' }}>✕</Button>
        </div>
        <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 10 }}>
          giriş span&#39;leri · seçili pencere
        </div>

        <div style={{
          display: 'grid', gridTemplateColumns: 'repeat(4,1fr)', gap: 6,
          fontSize: 11, marginBottom: 12,
        }}>
          <div><div style={{ color: 'var(--text3)', fontSize: 9.5 }}>CALLS</div><span className="mono">{row.calls.toLocaleString()}</span></div>
          <div><div style={{ color: 'var(--text3)', fontSize: 9.5 }}>ERR</div><span className="mono" style={{ color: row.errorRate > 1 ? 'var(--err)' : undefined }}>{row.errorRate.toFixed(1)}%</span></div>
          <div><div style={{ color: 'var(--text3)', fontSize: 9.5 }}>P50</div><span className="mono">{row.p50Ms != null ? `${row.p50Ms.toFixed(0)}ms` : '—'}</span></div>
          <div><div style={{ color: 'var(--text3)', fontSize: 9.5 }}>P99</div><span className="mono">{row.p99Ms.toFixed(0)}ms</span></div>
        </div>

        <MiniRed label="Çağrı hızı" values={row.sparkline} color="var(--teal, #2dd4bf)" />
        <MiniRed label="Hatalar" values={row.errorsSparkline} color="var(--err)" />
        <MiniRed label="P99 gecikme" values={row.p99Sparkline} color="var(--purple)" />

        <div style={{ borderTop: '1px solid var(--border)', margin: '10px 0' }} />
        <div style={{ fontSize: 10, color: 'var(--text3)', marginBottom: 6 }}>Örnek trace&#39;ler (pencere exemplar&#39;ları)</div>
        <div style={{ display: 'grid', gap: 4, fontSize: 11.5, marginBottom: 12 }}>
          {row.errorTraceId
            ? <Link className="mono" to={`/trace?id=${row.errorTraceId}`} style={{ color: 'var(--err)', textDecoration: 'none' }}>✕ en kötü hata · {row.errorTraceId.slice(0, 12)}…</Link>
            : <span style={{ color: 'var(--text3)', fontStyle: 'italic' }}>hata exemplar&#39;ı yok — pencere sağlıklı</span>}
          {row.slowTraceId
            ? <Link className="mono" to={`/trace?id=${row.slowTraceId}`} style={{ color: 'var(--orange)', textDecoration: 'none' }}>◷ en yavaş · {row.slowTraceId.slice(0, 12)}…</Link>
            : <span style={{ color: 'var(--text3)', fontStyle: 'italic' }}>yavaş exemplar&#39;ı yok</span>}
        </div>

        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
          {/* v0.9.819 — BİRİNCİL çıkış: peek'in cevaplayamadığı her şey
              (gecikme dağılımı, durum kırılımı, exception'lar, başarısız
              trace'ler, split-by) /endpoints çekmecesinde. */}
          <Link to={fullAnalysisHref} style={{ textDecoration: 'none' }}
            title="Bu rotanın tam analizini /endpoints çekmecesinde aç — gecikme dağılımı, durum kırılımı, rota üstündeki exception'lar, başarısız trace'ler, split-by.">
            <Button variant="primary" size="sm">Tam analiz →</Button>
          </Link>
          <Link to={tracesHref} style={{ textDecoration: 'none' }}>
            <Button variant="secondary" size="sm">Traces →</Button>
          </Link>
          <Link to={`/service?name=${encodeURIComponent(service)}&range=${rangeParam}&tab=operations`} style={{ textDecoration: 'none' }}>
            <Button variant="secondary" size="sm">Operations&#39;ta aç</Button>
          </Link>
        </div>
      </div>
    </>
  );
}
