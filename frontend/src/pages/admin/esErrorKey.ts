import type { ESQueryError } from '@/lib/types';

// esErrorKey — v0.9.876 (tutarlılık denetimi BT14, risk R4).
//
// AdminElastic'in "Recent query errors" tablosu genişletilebilir satırlar
// taşıyor ve açık satırı DİZİ İNDEKSİ ile tutuyordu (`open === i`). Sıralama
// olmadığı sürece bu çalışıyordu: indeks ile satır birebirdi.
//
// Tablo useDataTable'a geçince o varsayım ÇÖKÜYOR. `dt.sortedRows` sırayı
// değiştirir, `i` artık başka bir hataya karşılık gelir ve operatör bir
// satırı açtığında BAŞKA BİR SORGUNUN gövdesi açılır. Sessiz: ekranda bir
// şey "bozulmuyor", sadece yanlış JSON gösteriliyor — ve bu tablo tam olarak
// "hangi sorgu patladı" sorusunu cevaplamak için var.
//
// Bu yüzden anahtar SATIRIN KENDİSİNDEN türetiliyor.
//
// Alan seçimi: `at` (unix ms) tek başına yeterli değil — ES hata örnekleri
// toplu geliyor ve aynı milisaniyede birden fazla sorgu patlayabiliyor
// (bir _msearch'ün alt sorguları aynı damgayı taşır). `op` + `index` ayrımı
// artırıyor; kalan çakışmayı `query` kapatıyor, çünkü açılan gövde ZATEN
// `query` — aynı `query`ye sahip iki satır açıldığında aynı içeriği gösterir,
// yani çakışma gözlemlenebilir bir yanlışlık üretmez.
export function esErrorKey(e: ESQueryError): string {
  return `${e.at}|${e.op}|${e.index}|${e.query}`;
}
