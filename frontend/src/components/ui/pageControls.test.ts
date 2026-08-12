import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve, join } from 'node:path';

// v0.9.639 — yapışkan filtre barı (denetim bulgusu #5, etki 5/5:
// "filtreler kaydırınca ekrandan çıkıyor").
//
// Bu testler görsel doğrulama YERİNE GEÇMEZ — CSS'in tarayıcıda nasıl
// göründüğünü jsdom söyleyemez. Çiviledikleri şey, düzeltmenin
// dayandığı YAPISAL varsayımlar: bunlar bozulursa sticky sessizce
// çalışmaz ve kimse fark etmez.

const css = readFileSync(resolve(__dirname, '../../styles/globals.css'), 'utf8')
  .replace(/\/\*[\s\S]*?\*\//g, ''); // yorumlar önce sıyrılır: açıklama kuralı alıntılıyor

function block(sel: string): string {
  const i = css.indexOf(sel + ' {');
  if (i < 0) return '';
  return css.slice(i, css.indexOf('}', i));
}

describe('sticky filtre barının dayandığı kabuk', () => {
  // Sticky'nin yapışacağı bir kaydırma portu OLMAK ZORUNDA. #content
  // overflow:auto olmaktan çıkarsa bar sessizce sıradan bir div olur.
  // v0.9.981 — seçici `#content` idi; D3.1 kabı `#content, .page-body`
  // ortak kuralına çevirdi (çift `#content` düzeltmesi). Kural aynı,
  // ADRESİ değişti — test o yüzden GÜNCELLENDİ, gevşetilmedi: ikisinin
  // de aynı kaydırma portu sözleşmesini taşıdığı ayrıca sınanıyor.
  it('#content kaydırma portu olmayı sürdürüyor', () => {
    const b = block('#content, .page-body');
    expect(b, '#content kuralı bulunamadı — seçici mi değişti?').not.toBe('');
    expect(b).toMatch(/overflow:\s*auto/);
    expect(b).toMatch(/flex:\s*1/);
  });

  it('.page-body aynı sözleşmeyi taşıyor (gizlenen liste kabı)', () => {
    // `/inbox?problem=` ve `/problems?problem=` açıkken liste kabı bu
    // sınıfı taşıyor ve detay kapandığında GÖRÜNÜR hâle geliyor: aynı
    // padding/scroll olmazsa sayfa iki farklı yerleşim arasında zıplar.
    expect(css).toMatch(/#content, \.page-body \{/);
  });

  // v0.9.640 — operatör-bildirimli regresyon: YATAY negatif margin blok
  // elemanın genişliğini 40px artırıyor, kolon sayısı fazla bir tabloda
  // #content'te yatay kaydırma doğuruyor ve içerik barın yanından
  // sızıyordu. Yatay taşırma zaten gereksizdi: alttan kayan içerik de
  // barla aynı content-box genişliğinde.
  it('YATAY taşırma YOK — genişliği artıran negatif margin yasak', () => {
    const s = block('.controls.is-sticky');
    expect(s).not.toMatch(/margin(-left|-right)?:\s*-/);
    expect(s).not.toMatch(/width:/);
  });

  it('scrollport üst kenarına yapışıyor', () => {
    expect(block('.controls.is-sticky')).toMatch(/top:\s*0/);
  });

  it('sticky sınıfı gerçekten sticky ve opak', () => {
    const s = block('.controls.is-sticky');
    expect(s).toMatch(/position:\s*sticky/);
    expect(s).toMatch(/z-index/);
    // Opak zemin şart: altından kayan içerik barın içinden görünürdü.
    expect(s).toMatch(/background:\s*var\(--bg0\)/);
  });

  // İÇ ÇERÇEVE YASAK (operatör kuralı: "sayfa başına tek scroll ekseni").
  // Bar kendi kaydırma konteynerini açarsa iç içe scroll doğar.
  it('bar yeni bir kaydırma konteyneri AÇMIYOR', () => {
    expect(block('.controls.is-sticky')).not.toMatch(/overflow/);
  });
});

describe('opt-in olma sözleşmesi', () => {
  const src = readFileSync(resolve(__dirname, './PageControls.tsx'), 'utf8');

  // .controls'u 30 dosya kullanıyor ve hepsi liste sayfası değil.
  // Varsayılan sticky olursa doğrulanmamış bir davranış 30 sayfaya yayılır.
  it('sticky varsayılan olarak KAPALI', () => {
    expect(src).toMatch(/sticky\s*=\s*false/);
  });

  it('kapalıyken is-sticky sınıfı basılmıyor', () => {
    expect(src).toContain("sticky ? 'is-sticky' : ''");
  });
});

// v0.9.644 — yapışkan tablo BAŞLIĞI (denetim bulgusu #10, etki 5/5).
//
// Engel: `.table-wrap`'ın overflow-x:auto'su onu kaydırma konteyneri
// yapıyor, thead'in sticky'si o konteynere yapışıyor ama konteyner
// dikey kaydırmadığı için etkisiz. `.is-fit` konteyneri kaldırıyor.
describe('yapışkan tablo başlığı', () => {
  it('is-fit kaydırma konteynerini kaldırıyor', () => {
    expect(block('.table-wrap.is-fit')).toMatch(/overflow:\s*visible/);
  });

  it('başlık yapışkan ve opak', () => {
    const b = block('.table-wrap.is-fit thead th');
    expect(b).toMatch(/position:\s*sticky/);
    expect(b).toMatch(/background:/);
  });

  // İKİ ÖZELLİK BİRLİKTE ÇALIŞMALI: ikisi de top:0 olsaydı üst üste
  // binerlerdi. Başlık barın ALTINA yapışıyor.
  it('başlık, yapışkan filtre barının ALTINA yapışıyor', () => {
    expect(block('.table-wrap.is-fit thead th')).toContain('top: var(--controls-h, 0px)');
  });

  // Varsayılan DEĞİŞMEMELİ: geniş tabloda is-fit, yatay kaydırmayı
  // #content'e taşır ve v0.9.640'ta düzeltilen bar sızıntısını geri
  // getirir. Yanlış sınıflandırmanın bedeli asimetrik.
  it('varsayılan .table-wrap hâlâ kendi kaydırma konteyneri', () => {
    const b = block('.table-wrap');
    expect(b).toMatch(/overflow-x:\s*auto/);
  });
});

describe('PageControls yüksekliği yayınlıyor', () => {
  const src = readFileSync(resolve(__dirname, './PageControls.tsx'), 'utf8');

  it('--controls-h yazılıyor', () => {
    expect(src).toContain("setProperty('--controls-h'");
  });

  // Sabit sayı kırılgan olurdu: bar sarınca yüksekliği değişiyor.
  it('yükseklik ÖLÇÜLÜYOR, sabit değil', () => {
    expect(src).toContain('ResizeObserver');
    expect(src).toContain('offsetHeight');
  });

  // Bar sticky değilse değişken yazılmamalı — yoksa başlık olmayan bir
  // barın altına yapışmaya çalışır.
  it('yalnız sticky iken yayınlıyor ve sökülürken temizliyor', () => {
    expect(src).toContain('if (!el || !sticky) return;');
    expect(src).toContain("removeProperty('--controls-h')");
  });
});

// v0.9.663 — yapışkan barın YAPISAL ön koşulu: #content ağacında olmak.
//
// Bar `#content`'in kaydırma eksenine yapışıyor ve yüksekliğini
// `--controls-h` olarak ORAYA yayınlıyor. Kendi `#content`'i olmayan bir
// yüzeyde — gömülü bileşenler (LogsExplorer, MetricsExplorer,
// DependenciesTable) ve settings sekmeleri — bar sayfanın #content'ine
// tutunup alakasız içeriğin üstünde uçar. Bu yayılımda o altı dosya tam
// bu yüzden dışarıda bırakıldı; kural yorumda kalmasın diye kapı burada.
//
// KAPSAM SINIRI (dürüstlük): tarama "#content var mı ve bar ondan SONRA
// mı" diye soruyor. Aradaki bir kaydırma konteynerini yakalayamaz — onu
// ancak gözle görmek mümkün. Yakaladığı şey, gerçekten yapılabilecek
// hata: barı hiç #content'i olmayan bir yüzeye koymak.
describe('sticky bar #content ağacında', () => {
  const SRCDIR = resolve(__dirname, '../..');
  const walk = (d: string, out: string[] = []): string[] => {
    for (const e of readdirSync(d)) {
      const p = join(d, e);
      if (statSync(p).isDirectory()) walk(p, out);
      else if (p.endsWith('.tsx')) out.push(p);
    }
    return out;
  };
  const users = walk(SRCDIR)
    .map(p => ({ p, src: readFileSync(p, 'utf8') }))
    .filter(f => f.src.includes('<PageControls sticky'));

  // Tarama boş dönerse aşağıdaki her şey hiçbir şey ölçmeden GEÇER.
  it('kullanımlar bulunabiliyor', () => {
    expect(users.length).toBeGreaterThan(5);
  });

  it('her sticky bar kendi kabuk kabının İÇİNDE', () => {
    // v0.9.981 — `.page-body` de geçerli host. `/problems` (AnomaliesPage)
    // detay açıkken liste kabını gizli-ama-MOUNT'lu tuttuğu için o kap
    // id yerine sınıf taşıyor; `PageControls` da `closest('#content,
    // .page-body')` arıyor. Yalnız `id="content"` aransaydı bu test o
    // sayfayı YANLIŞ yere işaretlerdi.
    // v0.9.992 — `<PageShell>` de geçerli host. D9 kademeli geçişinde
    // sayfalar kabı elle yazmayı bırakıp atomu çağırıyor; atom `#content`i
    // basan tek yer (`components/ui/PageShell.tsx`). Bu satır olmasaydı
    // geçen her pilot bu kapıyı YANLIŞ yere kırardı — kapı "kabuk kabı"
    // kavramını ölçüyor, o kavramın YAZILIŞINI değil.
    const HOSTS = ['id="content"', 'className="page-body"', '<PageShell>'];
    // v0.9.982 — `/system/*` sekme gövdeleri MUAF, gerekçeli: D4'ten beri
    // kabuğu `System.tsx` basıyor (Settings deseni), yani host BAŞKA
    // DOSYADA ve dosya-yerel bir tarama onu göremez. Muafiyet boşluk
    // bırakmıyor: `systemShell.test.ts` System.tsx'in `#content` bastığını
    // ve sekme gövdelerinin BASMADIĞINI ayrıca çiviliyor — ikisi birlikte
    // aynı garantiyi veriyor. Bu satır bu kapının D4'ü YAKALAMASIYLA
    // doğdu (AdminAudit tam da burada kırmızıya döndü) — kapı çalıştı.
    const SHELL_FROM_PARENT = /pages\/Admin[A-Za-z]*\.tsx$/;
    const bad = users
      .filter(f => !SHELL_FROM_PARENT.test(f.p))
      .filter(f => {
        const at = HOSTS.map(h => f.src.indexOf(h)).filter(i => i >= 0);
        if (at.length === 0) return true;
        return Math.min(...at) > f.src.indexOf('<PageControls sticky');
      })
      .map(f => f.p.slice(SRCDIR.length + 1));
    expect(bad, 'sticky bar bir kabuk kabının dışında — --controls-h yayınlanmaz').toEqual([]);
  });
});
