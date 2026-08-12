// useNarrow — dar ekran (telefon) katmanının TEK JS kaynağı (v0.9.988,
// arka plan + dar ekran denetimi D6).
//
// Neden gerekti: D2 CSS tarafında "üç adlı eşik" ({640, 1024, 1280})
// disiplinini kurdu, ama bir kolonu dar ekranda GİZLEMEK CSS'le
// yapılamıyor. `table-layout: fixed` + `<colgroup>` düzeninde bir
// `<col>`u `display:none` ile kaldırmak tarayıcılarda çalışmıyor,
// `td:nth-child(N)` ile gizlemek ise `colSpan` taşıyan açılan satırların
// hizasını bozuyor. Yani karar RENDER anında verilmek zorunda → JS.
//
// CSS custom property `@media` sorgusunda kullanılamadığı için sayı iki
// yerde yazılıyor (CSS + burada); ayrışmayı `styles/mobileBreakpoints.test.ts`
// çiviliyor — Sidebar'ın JS eşikleri için kurulmuş kapının aynısı.
//
// jsdom'da `window.matchMedia` YOK (CorePanel.smoke.test.tsx bunu elle
// dolduruyor). Bu yüzden hook yokluğa karşı GENİŞ ekran varsayıyor:
// mount testlerinde bugünkü (masaüstü) davranış aynen sürer, yeni bir
// test kurulumu gerekmez.

import { useEffect, useState } from 'react';

// D2'nin telefon katmanıyla AYNI sayı — globals.css `@media (max-width: 640px)`.
export const NARROW_MAX_PX = 640;

export const NARROW_QUERY = `(max-width: ${NARROW_MAX_PX}px)`;

function readNarrow(): boolean {
  if (typeof window === 'undefined') return false;
  if (typeof window.matchMedia !== 'function') return false;
  return window.matchMedia(NARROW_QUERY).matches;
}

// useIsNarrow — telefon genişliğinde mi. Yeniden boyutlandırmada
// (ve cihaz döndürmede) güncellenir.
export function useIsNarrow(): boolean {
  const [narrow, setNarrow] = useState(readNarrow);
  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return;
    const mq = window.matchMedia(NARROW_QUERY);
    const apply = () => setNarrow(mq.matches);
    apply();
    // `addEventListener` Safari 14+ ve tüm modern tarayıcılarda var;
    // depoda başka bir MediaQueryList dinleyicisi yok, eski API için
    // geriye-uyum şimi EKLENMİYOR (CLAUDE.md).
    mq.addEventListener('change', apply);
    return () => mq.removeEventListener('change', apply);
  }, []);
  return narrow;
}
