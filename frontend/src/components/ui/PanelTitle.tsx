import type { ReactNode } from 'react';

// PanelTitle — bir `<Card header={…}>` başlığı: BÜYÜK HARF ad + isteğe
// bağlı küçük harf nitelemesi + isteğe bağlı sağ yuva (v0.9.1365).
//
// NE İŞE YARAR — `sub` DÜRÜSTLÜK YUVASIDIR. Bu depoda bir panelin sayısı
// neredeyse hiç "her şey" demek değil: örneklem mi ("sample · 12k entry
// spans"), hangi kapsam mı ("all postgresql instances"), hangi eksen mi
// ("log-bin histogram"). O cümlenin başlıkta, sayının YANINDA duracağı
// yer burası; dipnota inen kapsam beyanı okunmuyor (v0.9.821/961 dersi).
//
// NEDEN ATOM — ve neden ŞİMDİ. İki nüsha vardı ve BAYT BAYT DEĞİLDİ:
// sayfa kopyası (`pages/DatabaseDetail.tsx:452`) `right` yuvasını
// KAYBETMİŞTİ. Yani bu, kopyaların nasıl bozulduğunun ders kitabı örneği:
// kopyalandığı gün aynıydılar, sonra biri gelişti. Terfi `right`lı sürüme
// yapılıyor — kaybeden nüshanın çağrı yerleri `right` GEÇMİYOR, dolayısıyla
// o dal render'da hiç çalışmıyor ve çıktı korunuyor.
//
// GEOMETRİ TOKEN'I, ÇIKTI AYNI: `components/ui/` atomları merdiveni
// token'la yazar (`styles/geometryTokens.test.ts`, v0.9.909).
// `--fs-xs` = 11px, `--sp-4` = 8px (`globals.css:136`, `:128`) — birebir
// eski ham sayılar. `sub`un 10.5px'i merdivende YOK, o yüzden ham kalıyor;
// onu 10'a çekmek kapıyı memnun etmek için görsel değişiklik yapmak olurdu
// (kapının ondalık kusuru aynı sürümde düzeltildi).
export function PanelTitle({ children, sub, right }: {
  children: ReactNode;
  /** Küçük harf niteleme — örneklem / kapsam / eksen beyanı. */
  sub?: ReactNode;
  /**
   * Sağa yaslanan yuva (bir pivot linki, bir toggle). Sayfa nüshasında
   * YOKTU; terfi bunu geri getiriyor ama hiçbir mevcut çağrı yeri
   * geçmediği için bugünkü çıktı değişmiyor.
   */
  right?: ReactNode;
}) {
  return (
    <div style={{ display: 'flex', alignItems: 'baseline', gap: 'var(--sp-4)' }}>
      <span style={{
        fontSize: 'var(--fs-xs)', fontWeight: 700, letterSpacing: 0.4,
        textTransform: 'uppercase', color: 'var(--text2)',
      }}>{children}</span>
      {sub && (
        <span style={{ fontSize: 10.5, fontWeight: 400, color: 'var(--text3)' }}>{sub}</span>
      )}
      {right && <span style={{ marginLeft: 'auto', fontWeight: 400 }}>{right}</span>}
    </div>
  );
}
