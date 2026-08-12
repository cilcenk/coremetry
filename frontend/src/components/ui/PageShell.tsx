// PageShell — sayfa gövdesi kabı (v0.9.991, denetim DALGA 9, KADEMELİ).
//
// NE YAPAR: `#content` kabını TEK yerden basar. Bugün 42 sayfa dosyası
// bunu elle yazıyor (56 çağrı noktası) ve hepsi HARFİ HARFİNE aynı:
// `<div id="content">`. Nitelik taşıyan tek bir örnek bile yok —
// ölçüldü, v0.9.991. Yani bu atom bir soyutlama DEĞİL, tekrarlanan bir
// sabitin tek kaynağa çekilmesi; masaüstünde çıktısı bit bit aynı.
//
// NEDEN ATOM: `id` tekilliğini kaynak seviyesinde garanti etmenin başka
// yolu yok. `singleContentId` kapısı (v0.9.981) bugün GEVŞEK — "aynı
// return ağacında iki tane olmasın" diyor, çünkü 18 dosya erken-dönüş
// dallarında kabı tekrarlıyor. Kap tek bir bileşenden basıldığında o
// kural TİPE düşer ve kapı gevşek olmak zorunda kalmaz.
//
// KADEMELİ GEÇİŞ (operatör kararı 2026-08-12): büyük patlama YOK.
// Bu sürüm atom + kapı; sonraki sürüm birkaç pilot sayfa. Kalan sayfalar
// DOKUNULMAZ ve ileride başka bir iş için o dosya açıldığında geçer.
// `components/pageShellAdoption.test.ts` allowlist'i bu geçişin
// sayacıdır ve YALNIZ KÜÇÜLEBİLİR.
//
// NE YAPMAZ — bilinçli:
//   · Zemin / dolgu / yoğunluk / dar-ekran kurallarını TEKRARLAMAZ.
//     Hepsi D1 ve D2'de `globals.css`e girdi (`#content, .page-body`
//     `:409`, `[data-density=…]` `:2341/2353/2367`, `@media ≤640px`
//     `:3104`). Atom yalnız doğru id/sınıfı basar — çifte kural, iki
//     ayrı yerden çelişebilen tek bir davranış demektir.
//   · Topbar'ı SARMAZ. Sayfalar `<Topbar/>` + `#content` kardeşliğini
//     `<>…</>` içinde kuruyor; Topbar'ın prop yüzeyi (range, env, live
//     ticker) sayfaya göre değişiyor ve onu kabuğa taşımak API'yi
//     kanıtlanmamış biçimde büyütürdü.
//   · `title` / `aside` / `width` PROP'U YOK. Denetimin §5 DALGA 9
//     taslağında geçiyorlardı; kodda karşılığı olan tek varyasyon
//     `variant`. İhtiyaç kanıtlanmadan API büyütülmüyor.
import type { ReactNode } from 'react';

export function PageShell({ children, variant = 'default' }: {
  children: ReactNode;
  /**
   * `'default'` — kabuk kaydırır, dolgu `globals.css`ten gelir
   * (`#content` `padding: 20px`, yoğunluk ve ≤640px kuralları dahil).
   * Bugünkü 56 çağrı noktasının TAMAMI budur.
   *
   * `'full'` — tam-bleed yüzeyler (topoloji tuvali, heatmap, trace
   * şelalesi, dashboard ızgarası): dolgu sıfırlanır ve kaydırma
   * ÇOCUĞA bırakılır, böylece çocuk `flex: 1; min-height: 0` diyebilir.
   * Kapattığı bozukluk, `calc(100vh - Npx)` sihirli sayıları
   * (`globals.css:1729` `#td-outer` -185px, `TraceCompare.tsx:234`
   * -220px): bu aritmetik `[data-density]` dolgu değişimine (6/10/20/22px)
   * ve ≤640px bloğuna KÖR, yani üç yoğunlukta üçü de bir miktar yanlış.
   *
   * DİKKAT (v0.9.991): `'full'`ün henüz ADOPTE EDEN SAYFASI YOK. Mevcut
   * tam-bleed sayfaları geçirmek kalibre edilmiş `calc()` değerlerini
   * atmak demek ve masaüstünde ölçülebilir bir kayma üretir — o yüzden
   * operatörün gözüne girmeden yapılmıyor (mockup-first).
   */
  variant?: 'default' | 'full';
}) {
  // `className={undefined}` React'te ÖZNİTELİK BASMAZ; default varyantın
  // DOM çıktısı bugünkü elle yazılmış `<div id="content">` ile birebir.
  return (
    <div id="content" className={variant === 'full' ? 'is-full' : undefined}>
      {children}
    </div>
  );
}
