import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';

// useStackFrameLinks — v0.10.581 (Aşama 1.5).
//
// Exception stack trace'ini TIKLANABİLİR yapan künye. Ayrıştırma bu
// dosyada YOK ve olmamalı: kanonik parser Go'da (internal/stackparse),
// frontend HAM metni gönderip `lineIndex`li frame listesi alır. İkinci
// bir TS ayrıştırıcı, iki dilde iki ayrı "doğru frame" demek olurdu.
//
// FETCH-ON-OPEN (ES-cost disiplini, CLAUDE.md): `enabled` YALNIZ
// exception bölümü ÇİZİLİYORKEN true. Trace listesinde, span
// tablosunda, çekmece kapalıyken HİÇBİR ön-getirme yok — bu uç DevOps
// sunucusuna gidiyor ve bir liste üzerinde ön-getirilirse tek bir
// trace açılışı onlarca dış istek üretirdi.
//
// SORGU ANAHTARI STACK'İN TAMAMINI TAŞIR, uzunluğunu değil. Uzunluk
// (ya da yalnız exception tipi) aynı serviste iki FARKLI exception'ı
// aynı anahtara düşürür ve operatör bir istisnanın frame linklerini
// bir başkasının satırlarında görürdü — `internal/api/cache.go`'daki
// "cache key hashes ALL inputs" kuralının frontend aynası. Metnin
// kendisi anahtarda: kısaltma yapan bir özet, çarpışma riskini
// doğruluk uğruna geri getirmenin bedelini haklı çıkarmıyor.
//
// staleTime 5 dk = sunucu TTL'i (kural: staleTime >= sunucu TTL).
// Polling YOK — bir stack trace geçmişte donmuş bir olgu, tazelenmez.
//
// retry: false — bu yüzey SESSİZCE bozulur. Uç düşerse ya da
// `configured:false` dönerse çağıran bugünkü düz metin görünümünde
// kalır; operatöre hata/uyarı GÖSTERİLMEZ. Yeniden denemek, kimsenin
// görmeyeceği bir cevap için DevOps sunucusuna üç kat yük demekti.
export function useStackFrameLinks(p: {
  service: string;
  stack: string;
  enabled?: boolean;
}) {
  return useQuery({
    queryKey: ['devops', 'stack-frames', p.service, p.stack],
    queryFn: ({ signal }) => api.stackFrameLinks(p.service, p.stack, signal),
    enabled: (p.enabled ?? true) && !!p.service && !!p.stack,
    staleTime: 5 * 60_000,
    retry: false,
  });
}
