// PageShell — sayfa gövdesi kabı (v0.9.991, denetim DALGA 9, KADEMELİ).
//
// NE YAPAR: `#content` kabını TEK yerden basar. Atom yazıldığında 43
// sayfa dosyası bunu elle yazıyordu (58 çağrı noktası) ve hepsi HARFİ
// HARFİNE aynıydı: `<div id="content">`. Nitelik taşıyan tek bir örnek
// bile yoktu — ölçüldü, v0.9.991. Yani bu atom bir soyutlama DEĞİL,
// tekrarlanan bir sabitin tek kaynağa çekilmesi; çıktısı bit bit aynı.
//
// NEDEN ATOM: `id` tekilliğini kaynak seviyesinde garanti etmenin başka
// yolu yok. `singleContentId` kapısı (v0.9.981) hâlâ GEVŞEK — "aynı
// return ağacında iki tane olmasın" diyor, çünkü erken-dönüş dalları
// kabı tekrarlıyor. Kap TEK bir bileşenden basıldığında o kural TİPE
// düşer ve kapı gevşek olmak zorunda kalmaz. Allowlist sıfırlandığı gün
// `singleContentId` tam kilide çevrilebilir.
//
// KADEMELİ GEÇİŞ (operatör kararı 2026-08-12): büyük patlama YOK.
// v0.9.991 atom + kapı; 992/993/994/995 dört dalgada 41 dosya / 54 kap;
// 998 ProblemDetail (2 kap). Allowlist 43 → 1. Kalan TEK dosya "sıra
// gelmedi" değil, GEREKÇELİ erteleme (`pageShellAdoption.test.ts` FROZEN
// yorumunda): TraceCompare bir `variant="full"` adayı ve kalibre edilmiş
// `calc(100vh - 220px)`ini atmak masaüstünde ölçülebilir kayma üretir.
// O allowlist bu geçişin sayacıdır ve YALNIZ KÜÇÜLEBİLİR.
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
   * Geçen 54 çağrı noktasının TAMAMI budur; göç bu yüzden DOM'u
   * hiçbir sayfada değiştirmedi.
   *
   * `'full'` — tam-bleed yüzeyler (topoloji tuvali, heatmap, trace
   * şelalesi, dashboard ızgarası): dolgu sıfırlanır ve kaydırma
   * ÇOCUĞA bırakılır, böylece çocuk `flex: 1; min-height: 0` diyebilir.
   * Kapattığı bozukluk, `calc(100vh - Npx)` sihirli sayıları
   * (`globals.css:1729` `#td-outer` -185px, `TraceCompare.tsx:234`
   * -220px): bu aritmetik `[data-density]` dolgu değişimine (6/10/20/22px)
   * ve ≤640px bloğuna KÖR, yani üç yoğunlukta üçü de bir miktar yanlış.
   *
   * DİKKAT (v0.9.995'te hâlâ geçerli): `'full'`ün ADOPTE EDEN SAYFASI
   * YOK. Mevcut tam-bleed sayfaları geçirmek kalibre edilmiş `calc()`
   * değerlerini atmak demek ve masaüstünde ölçülebilir bir kayma üretir
   * — o yüzden operatörün gözüne girmeden yapılmıyor (mockup-first).
   *
   * SIRADAKİ ADAYLAR — v0.9.997'de yeniden ÖLÇÜLDÜ, listeden iki kalem
   * düştü:
   *   · `TraceCompare.tsx:234` — `height: calc(100vh - 220px)`, kabın
   *     hemen içinde. Gerçek `full` adayı.
   *   · `globals.css:1729` `#td-outer` -185px — yalnız `PublicTrace.tsx`
   *     render ediyor; public kabuk ayrı iş (`PublicShell` + `full`).
   *   · `AdminSql.tsx` -80px ARTIK YOK — v0.9.981/D3.2'de kaldırıldı,
   *     yerinde `height:100%; minHeight:0` var. Denetim metnindeki bu
   *     kalem BAYAT; koddan doğrulanmadan kuyruğa alınmamalı.
   *   · `AdminCatalog.tsx:182` -220px bir TABLO `maxHeight`i, sayfa kabı
   *     değil — `full` göçünün konusu değil, ayrı (ve küçük) bir kalem.
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
