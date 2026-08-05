import type { ServiceMetricThroughput } from '@/lib/types';

// v0.9.665 — metrik türevli throughput ÇİZİLEMEDİĞİNDE nedenini söyler.
//
// Operatör: "metricten okusun bakalım bir. Oluştursun görelim."
//
// Grafana o metriği PROMETHEUS'tan okuyor. Coremetry'ye de girip
// girmediği buradan görülemez — collector onu OTLP ile iletiyor mu, adı
// çeviride korunuyor mu, bilinmiyor. Bu yüzden özellik sessizce boş bir
// grafik göstermiyor.
//
// Ayırt edilen üç durum, çünkü ÜÇÜ DE FARKLI EYLEM istiyor:
//   1. Metrik kurulumda YOK        → collector'ı düzelt (ya da adı ara).
//   2. Metrik var, `job` eşleşmedi → deseni/etiketi düzelt. Gerçek job
//      değerleri gösteriliyor ki operatör kıyaslasın.
//   3. Metrik var, job etiketi yok → başka bir etiket taşıyor olabilir.
//
// Boş bir grafik üçünü de aynı gösterirdi ve teşhis operatöre kalırdı.

export function MetricThroughputNote({ d }: { d: ServiceMetricThroughput }) {
  const box: React.CSSProperties = {
    marginTop: 8, padding: '8px 10px', borderRadius: 6,
    background: 'var(--bg2)', border: '1px solid var(--border)',
    fontSize: 11.5, lineHeight: 1.5, color: 'var(--text2)',
  };

  if (d.unsupportedInstrument) {
    return (
      <div style={box}>
        <b>Bu metrikten throughput türetilemiyor.</b>
        <div style={{ marginTop: 4 }}>
          <code>{d.metric}</code> · instrument <code>{d.instrument || '?'}</code>
        </div>
        <div style={{ color: 'var(--text3)', marginTop: 4 }}>
          Rate yalnız sayaç (sum) ve histogram için anlamlı; gauge anlık
          bir değer, oranı istek sayısı vermez.
        </div>
      </div>
    );
  }

  if (!d.metricExists) {
    return (
      <div style={box}>
        <b>Metrik bu kurulumda yok.</b>
        {(d.tried?.length ?? 0) > 0 && (
          <div style={{ marginTop: 4 }}>
            Denenen adlar:{' '}
            {d.tried!.map((t, i) => (
              <span key={t}>{i > 0 && ', '}<code>{t}</code></span>
            ))}
          </div>
        )}
        <div style={{ color: 'var(--text3)', marginTop: 4 }}>
          Grafana bunu Prometheus'tan okuyor; Coremetry'ye OTLP ile
          iletilmiyor ya da adı çeviride değişmiş olabilir. Farklı bir ad
          denemek için uca <code>?metric=</code> parametresi geçilebilir.
        </div>
        {(d.suggestions?.length ?? 0) > 0 && (
          <div style={{ marginTop: 6 }}>
            <div style={{ color: 'var(--text3)' }}>
              Katalogda benzer adlar — ölçüm başka bir adla içeride olabilir:
            </div>
            <ul style={{ margin: '4px 0 0 16px', padding: 0 }}>
              {d.suggestions!.map(n => (
                <li key={n} className="mono" style={{ fontSize: 11 }}>{n}</li>
              ))}
            </ul>
          </div>
        )}
      </div>
    );
  }

  const hasJobs = (d.sampleJobs?.length ?? 0) > 0;
  return (
    <div style={box}>
      <b>Metrik var ama bu servise eşleşen seri yok.</b>
      <div style={{ marginTop: 4 }}>
        <code>{d.metric}</code>
        {d.instrument && <> · instrument <code>{d.instrument}</code></>}
        {' '}· desen <code>{d.pattern}</code>
      </div>
      {(d.triedLabels?.length ?? 0) > 0 && (
        <div style={{ marginTop: 4, color: 'var(--text3)' }}>
          Denenen kimlik etiketleri:{' '}
          {d.triedLabels!.map((l, i) => (
            <span key={l}>{i > 0 && ', '}<code>{l}</code></span>
          ))}
        </div>
      )}
      {/* v0.9.682 — VARLIK, denemeden ayrı. "Anahtar yok" ise
          collector'ı düzeltmek gerekiyor; "anahtar var ama değer
          tutmadı" ise deseni. Boş grafik ikisini de aynı gösterirdi. */}
      <div style={{ marginTop: 4 }}>
        {(d.presentKeys?.length ?? 0) > 0 ? (
          <>
            <span style={{ color: 'var(--text3)' }}>Bu metrikte VAR OLAN anahtarlar:</span>{' '}
            {d.presentKeys!.map((k, i) => (
              <span key={k}>{i > 0 && ', '}<code>{k}</code></span>
            ))}
            <div style={{ color: 'var(--text3)', marginTop: 2 }}>
              Anahtar var ama eşleşme yok → değer beklenenden farklı.
            </div>
          </>
        ) : (
          <span style={{ color: 'var(--text3)' }}>
            Denenen kimlik anahtarlarının <b>hiçbiri</b> bu metrikte yok —
            collector servis kimliğini başka bir alanda gönderiyor.
          </span>
        )}
      </div>
      {hasJobs ? (
        <div style={{ marginTop: 6 }}>
          <div style={{ color: 'var(--text3)' }}>
            Kurulumda bulunan <code>{d.jobLabel}</code> değerleri ve
            bunlardan çözülen servis adları:
          </div>
          <ul style={{ margin: '4px 0 0 16px', padding: 0 }}>
            {d.sampleJobs!.map((j, i) => (
              <li key={j} className="mono" style={{ fontSize: 11 }}>
                {j}
                {d.sampleServices?.[i] && (
                  <span style={{ color: 'var(--text3)' }}> → {d.sampleServices[i]}</span>
                )}
              </li>
            ))}
          </ul>
        </div>
      ) : (
        <div style={{ color: 'var(--text3)', marginTop: 6 }}>
          Metrik hiç <code>{d.jobLabel}</code> etiketi taşımıyor — servis
          kimliği başka bir etikette olabilir.
        </div>
      )}
    </div>
  );
}
