import type { CSSProperties } from 'react';
import { fmtNum, fmtDurShort } from '@/lib/utils';
import type { ExternalPathRow } from '@/lib/types';

// ExternalPaths — v0.9.1255. Bir dış bağımlılığın "en çok çağrılan
// yollar" kırılımı. İKİ yerde çiziliyor: /external çekmecesi (tam RED
// kolonları) ve topolojinin pin'li düğüm kartı (dense, ilk 5 — kart
// 240px).
//
// Neden ortak bileşen: boş / hata / kırpılmış-pencere üçlüsünün
// SÖYLEDİĞİ şey iki yüzeyde de aynı olmalı. "Yol yok" ile "okuma
// başarısız oldu" ayrımı burada BİR kez yazılıyor; ikinci bir kopya
// yazılsaydı biri er ya da geç timeout'u boşluk gibi gösterirdi
// (v0.9.363'ün sınıfı).
//
// Yollar NORMALIZE: /orders/12345 ve /orders/67890 tek satır
// (/orders/{id}). Alttaki not bunu söylüyor — operatör ham url.full
// beklerse sayıların neden birleştiğini bilmeli.

/**
 * ellipsizePathMiddle — yolu ORTADAN kırpar, KUYRUĞU korur.
 *
 * Bu fonksiyon özelliğin bütün noktası. Operatörün şikâyeti tam olarak
 * ortak ön ekti: `/tibcoESB/ExternalServices/NVI/KPS/…` altındaki beş
 * uç, sondan kırpan bir hücrede BEŞİ DE aynı görünür
 * ("/tibcoESB/ExternalServic…") — yani kırılım eklenmiş ama hiçbir şey
 * ayırt edilemiyor olurdu. Ayırt edici parça SONDA, o yüzden bütçenin
 * 2/3'ü kuyruğa gider.
 *
 * bidi hilesi (direction:rtl) bilerek KULLANILMIYOR: yolun içindeki
 * '/' ve rakamlar görsel sırayı kaydırıyor ve sonuç tarayıcıdan
 * tarayıcıya değişiyor. Saf, test edilebilir bir kırpma tercih edildi;
 * tam değer her hâlükârda `title`da duruyor.
 */
export function ellipsizePathMiddle(p: string, max: number): string {
  if (max <= 1) return p.length <= max ? p : '…';
  if (p.length <= max) return p;
  const tail = Math.max(1, Math.ceil(((max - 1) * 2) / 3));
  const head = Math.max(0, max - 1 - tail);
  return p.slice(0, head) + '…' + p.slice(p.length - tail);
}

export function ExternalPaths({ paths, error, windowS, limit, dense }: {
  paths: ExternalPathRow[] | undefined;
  error?: string;
  windowS?: number;
  limit?: number;
  dense?: boolean;
}) {
  const muted: CSSProperties = { fontSize: dense ? 10 : 12, color: 'var(--text3)' };

  if (error) {
    return (
      <div style={{ ...muted, color: 'var(--warn)' }}>
        Yol kırılımı okunamadı — çağıran listesi ve trend geçerli.
        {!dense && <span className="mono" style={{ display: 'block', marginTop: 3 }}>{error}</span>}
      </div>
    );
  }
  const rows = (paths ?? []).slice(0, limit ?? 10);
  if (rows.length === 0) {
    return (
      <div style={muted}>
        Bu pencerede URL taşıyan istemci span'i yok — yol kırılımı
        {' '}<span className="mono">url.full</span> / <span className="mono">http.url</span>
        {' '}/ <span className="mono">url.path</span> attr'ından türer.
      </div>
    );
  }

  const total = rows.reduce((a, r) => a + r.calls, 0);
  const maxChars = dense ? 26 : 46;
  return (
    <>
      <table style={{ width: '100%', fontSize: dense ? 10.5 : 12 }}>
        <thead>
          <tr style={{ color: 'var(--text3)', fontSize: dense ? 9.5 : 11, textAlign: 'left' }}>
            <th>Yol</th>
            <th className="num">Çağrı</th>
            {!dense && <th className="num">Hata %</th>}
            <th className="num">P99</th>
          </tr>
        </thead>
        <tbody>
          {rows.map(r => (
            <tr key={r.path}>
              <td>
                <span
                  title={`${r.path}\n${fmtNum(r.calls)} çağrı · ${r.errorRate.toFixed(2)}% hata · p99 ${r.p99Ms.toFixed(0)}ms`}
                  style={{ fontFamily: 'ui-monospace, monospace' }}>
                  {ellipsizePathMiddle(r.path, maxChars)}
                </span>
              </td>
              <td className="num mono">{fmtNum(r.calls)}</td>
              {!dense && (
                <td className="num mono" style={{
                  color: r.errorRate > 5 ? 'var(--err)'
                    : r.errorRate > 1 ? 'var(--warn)' : 'var(--text3)',
                }}>{r.errorRate.toFixed(2)}</td>
              )}
              <td className="num mono" style={{
                color: dense && r.errorRate > 5 ? 'var(--err)' : undefined,
              }}>{r.p99Ms.toFixed(0)}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {/* Pencere kırpması BEYAN edilir: sunucu ham spans okumasını
          kısıtlıyor, yani bu sayılar çekmecenin üst yarısıyla AYNI
          aralığı kapsamayabilir. Gizlenirse operatör seçtiği aralığın
          tamamına ait bir toplam sanar. */}
      <div style={{ ...muted, marginTop: 5 }}>
        {fmtNum(total)} çağrı{windowS ? ` · son ${fmtDurShort(windowS)}` : ''}
        {' · '}id'ler <span className="mono">{'{id}'}</span> olarak gruplandı
      </div>
    </>
  );
}
