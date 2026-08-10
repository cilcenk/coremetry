// escLayer — Esc'in KATMAN disiplini (v0.9.950, UX denetimi E2 / Ö28).
//
// SORUN: uygulamada 30'u aşkın bağımsız Esc dinleyicisi vardı ve hiçbiri
// ötekinden haberdar değildi. Sonuç iki somut kusur:
//
//   1. Drawer açıkken ⌘K'yı Esc'le kapatmak DRAWER'I DA götürüyordu — iki
//      document dinleyicisi aynı olayı görüp ikisi de kapanıyordu.
//   2. Drawer içindeki bir input'ta Esc, input'u temizlemek yerine
//      ÇEKMECEYİ komple kapatıyordu — operatör bir arama kutusunu
//      temizlemek isterken bağlamını kaybediyordu.
//
// MODEL: LIFO. "En son açılan en üsttedir" — öncelik tablosu YAZILMADI ve
// bu bilinçli: sabit bir öncelik listesi (modal > drawer > popover > …) her
// yeni yüzeyde güncellenmek zorunda kalır ve unutulduğu gün sessizce yanlış
// katmanı kapatır. Açılış SIRASI zaten operatörün zihnindeki sıra.
//
// SAF — DOM'a dokunmaz. Tek document dinleyicisi keyboard.ts'te ve o
// dinleyici burayı SORAR (ikinci bir dinleyici, çözmeye çalıştığımız
// sorunun ta kendisi olurdu).

import { useEffect, useRef } from 'react';

type EscHandler = () => void;

// Yığın. Kimlik (fonksiyon referansı) ile çıkarılıyor: bir katman
// kapanırken TEPEDE olmayabilir (arkasında bir modal açılmış olabilir) ve
// körü körüne pop yapmak yanlış katmanı düşürürdü.
const stack: EscHandler[] = [];

/** pushEscLayer — katmanı yığına koyar; dönen fonksiyon onu ÇIKARIR. */
export function pushEscLayer(h: EscHandler): () => void {
  stack.push(h);
  return () => {
    const i = stack.lastIndexOf(h);
    if (i >= 0) stack.splice(i, 1);
  };
}

/** topEscLayer — şu an Esc'i hak eden katman (yoksa null). */
export function topEscLayer(): EscHandler | null {
  return stack.length ? stack[stack.length - 1] : null;
}

/** escLayerDepth — açık katman sayısı. Yalnız test/teşhis için. */
export function escLayerDepth(): number {
  return stack.length;
}

/** Test kancası — modül durumunu sıfırlar. */
export function __resetEscLayers(): void {
  stack.length = 0;
}

// useEscLayer — React girişi. `active` true olduğu SÜRECE katman yığında.
//
// Katman kaydı MOUNT'a değil AÇIKLIĞA bağlı: her zaman mount olan ama
// çoğunlukla kapalı duran yüzeyler (Drawer, Modal, popover) kapalıyken
// yığına girmemeli, yoksa görünmez bir katman Esc'i yutar.
export function useEscLayer(active: boolean, onEsc: () => void): void {
  const ref = useRef(onEsc);
  ref.current = onEsc;
  useEffect(() => {
    if (!active) return;
    // Sarmalayıcı kayıt: handler her render'da değişse bile yığındaki
    // kimlik SABİT kalır, yani katman kapanıp yeniden açılmaz (ve sırası
    // bozulmaz).
    return pushEscLayer(() => ref.current());
  }, [active]);
}
