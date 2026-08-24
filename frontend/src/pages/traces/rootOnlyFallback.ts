// rootOnlyFallback — /traces'in "root seçili, ama boş dönerse sessizce
// bırak" davranışı (v0.9.1372, operatör isteği 2026-08-24).
//
// Operatörün /endpoints ve /databases pivotlarından beklediği şey iki
// parçalı ve parçalar ÇELİŞİYOR:
//
//   1. "Traces butonuna bastığımda bana sadece root seçili traces
//      getirsin" — çünkü bir endpoint'in trace'lerini okurken istenen
//      şey o endpoint'i ÇAĞIRAN akışın tamamı, alt-span'lerin ayrı
//      satırlar hâlinde listeyi doldurması değil.
//   2. "Ama bazı endpointler root span değil ve root seçili olmadan
//      aramak gerekiyor" — bu deployment'ta bir kısım endpoint bir
//      mesaj tüketicisinin ya da iç bir servisin ORTASINDA yaşıyor;
//      onlar için root filtresi HER ZAMAN sıfır satır döndürür.
//
// Yani doğru varsayılan endpoint'e göre değişiyor ve bunu link kurulurken
// bilmenin yolu yok — bilmek, pivotu kurmadan önce bir sorgu koşturmak
// demekti. Onun yerine niyeti URL'e yazıyoruz (`rootOnly=auto`) ve
// KARARI sonuca bakarak veriyoruz.
//
// Operatörün ek şartı: "Geri dönüş sessiz olsun." Yani bir uyarı şeridi,
// bir "root filtresi kaldırıldı" rozeti YOK. Boş liste görüp filtreyi
// elle kaldırmak zaten operatörün yapacağı şeydi; ürün onu kendisi
// yapıyor ve sonucu gösteriyor.

export type RootOnlyInit = {
  /** Kutunun açılış hâli. */
  rootOnly: boolean;
  /** Sıfır sonuçta sessizce bırakma KURULU mu? */
  auto: boolean;
};

/**
 * parseRootOnlyParam — `?rootOnly=` üç durumlu.
 *
 *   'true' → root açık, geri dönüş YOK (operatör bunu kendi seçti ya da
 *            paylaşılan link öyle diyor; sessizce değiştirmek linkin
 *            anlamını çalardı)
 *   'auto' → root açık, sıfır sonuçta sessizce bırak (pivot linkleri)
 *   yok/ne varsa → kapalı (v0.9.78 kararı korunuyor: root-only bir
 *            VARSAYILAN olamaz, non-root operasyon seçimlerini gizler)
 */
export function parseRootOnlyParam(raw: string | null): RootOnlyInit {
  if (raw === 'auto') return { rootOnly: true, auto: true };
  return { rootOnly: raw === 'true', auto: false };
}

/**
 * rootOnlyUrlValue — durumun URL'e geri yazılışı.
 *
 * `auto` ASLA geri yazılmaz: kurulum bir kereliktir, tetiklendikten
 * sonra URL somut sonucu taşımalı. Aksi hâlde operatör linki kopyalayıp
 * paylaştığında karşı taraf aynı otomatiği bir kez daha koşturur ve
 * KENDİ verisine göre farklı bir liste görür — "aynı linki açtık, farklı
 * şey gördük" sınıfı. '' dönüşü çağıranın parametreyi DÜŞÜRMESİ demek
 * (Traces.tsx'in buildQuery sözleşmesi boş değerleri atar).
 */
export function rootOnlyUrlValue(rootOnly: boolean): string {
  return rootOnly ? 'true' : '';
}

/**
 * shouldDropRootOnly — sessiz geri dönüş tetiklendi mi?
 *
 * Hepsi ŞART:
 *   • kurulu (yalnız `auto` ile gelen pivotlar)
 *   • kutu hâlâ açık (bir kez düştüyse tekrar düşmez)
 *   • liste YÜKLENDİ ve HATASIZ (hata sıfır satır DEĞİLDİR — ağ hatasında
 *     filtreyi bırakmak, kullanıcının sormadığı bir sorguyu sessizce
 *     genişletir ve hatayı da gizler)
 *   • sıfır satır
 *
 * Tetiklendikten sonra çağıran `auto`'yu İNDİRMEK zorunda; yoksa sonraki
 * her boş sonuç bu yolu yeniden çalıştırır.
 */
export function shouldDropRootOnly(s: {
  auto: boolean;
  rootOnly: boolean;
  loaded: boolean;
  errored: boolean;
  rowCount: number;
}): boolean {
  return s.auto && s.rootOnly && s.loaded && !s.errored && s.rowCount === 0;
}
