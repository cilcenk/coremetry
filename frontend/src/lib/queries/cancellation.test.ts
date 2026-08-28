// v0.9.617 — pahalı okumalar GERÇEKTEN iptal edilebilir olmalı.
//
// v0.9.603 ham fetch yolunu (Traces) düzeltti ama kapsamını olduğundan
// geniş sandım: sayfaların çoğu React Query kullanıyor ve orada
// queryFn'in aldığı AbortSignal HİÇBİR YERE iletilmiyordu. FAZ 1
// denetimi ölçtü: 174 queryFn, sıfırı signal iletiyor.
//
// REACT QUERY SEMANTİĞİ (query-core kaynağından doğrulandı, v5):
// signal YALNIZ son observer kalktığında abort edilir
// (Query.removeObserver → retryer.cancel({revert:true})) — yani kimse
// o sonuca bakmıyorken. Ayrıca signal'e DOKUNMAK #abortSignalConsumed
// bayrağını açar ve davranışı cancelRetry()'den GERÇEK iptale
// yükseltir. Bu yüzden signal'i tüketmek ekrana hata DÜŞÜRMEZ:
// rejection'ı RQ kendi CancelledError'ı ile zaten karşılamıştır.
//
// Kapsam BİLİNÇLİ olarak dar: pahalı okumalar. Ucuz sorguların
// (isim listeleri, config, sayaçlar) iptali ölçülebilir bir kazanç
// değil ve 174 çağrı noktasını dolaşmanın regresyon riski kazancından
// büyük.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

const HEAVY: { file: string; hooks: string[] }[] = [
  { file: 'services.ts', hooks: ['api.services(', 'api.serviceMap('] },
  { file: 'logs.ts', hooks: ['api.logs('] },
  // v0.10.131 — entity pivotları: entity_seen_5m taramaları + Thanos delegasyonu.
  { file: 'entities.ts', hooks: ['api.entityServices(', 'api.servicePods(', 'api.entityMetrics(', 'api.entityContainers('] },
  { file: 'endpoints.ts', hooks: ['api.endpoints(', 'api.endpointDetail(', 'api.endpointSplit(', 'api.endpointDownstream('] },
  // v0.9.810 — Explore'un fan-out'u. Bu dosya lib/queries'te DEĞİL
  // (pages/explore altında) ama kusur sınıfı birebir aynı ve ölçeği daha
  // büyük: operatör her aralık/filtre/viz dokunuşunda DÖRT sorguyu birden
  // yeniliyor, üçü de ham spans / metric_points taramasına düşebiliyor ve
  // eskiler ClickHouse'ta sonuna kadar koşuyordu.
  {
    file: '../../pages/explore/useExploreQueries.ts',
    hooks: ['api.resolveMetric(', 'api.spanMetricTopN(', 'api.metricQueryFull('],
  },
];

// stripComments — kaynağı YORUMSUZ okur.
//
// Bu oturumda ÜÇÜNCÜ kez: kaynak taraması, düzeltmeyi anlatan bir
// yorumun içindeki eski çağrı metnine takıldı ("api.logs()" bir
// açıklama cümlesinde geçiyordu) ve yanlış kırmızı verdi. Yorumları
// atmak sınıfı kapatıyor.
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');
}

// callSpan — çağrının AÇILIŞ parantezinden EŞLEŞEN kapanışına kadar olan
// metin.
//
// v0.9.810'a dek tarama "çağrının kendi SATIRI"na bakıyordu. O kestirme,
// argümanları tek satıra sığan çağrılar için yeterliydi; Explore'un
// fan-out'unda ise `api.spanMetricTopN({ … }, signal)` on satıra yayılıyor
// ve signal KAPANIŞ satırında duruyor — kestirme onu göremez ve düzeltme
// yerindeyken kırmızı verirdi. Dengeli parantez, çağrının gerçek sınırı.
function callSpan(src: string, hook: string): string | null {
  const start = src.indexOf(hook);
  if (start < 0) return null;
  let depth = 0;
  for (let i = start + hook.length - 1; i < src.length; i++) {
    const c = src[i];
    if (c === '(') depth++;
    else if (c === ')') {
      depth--;
      if (depth === 0) return src.slice(start, i + 1);
    }
  }
  return src.slice(start);
}

describe('pahalı okumalar iptal edilebilir', () => {
  for (const { file, hooks } of HEAVY) {
    const src = stripComments(readFileSync(new URL(`./${file}`, import.meta.url), 'utf8'));
    for (const hook of hooks) {
      it(`${file}: ${hook}…) signal alıyor`, () => {
        const span = callSpan(src, hook);
        expect(span, `${hook} bulunamadı — test bayatladı`).not.toBeNull();
        expect(span!, `${hook} signal iletmiyor — operatör aralığı değiştirince ` +
          `bu sorgu ClickHouse'ta sonuna kadar koşmaya devam eder`).toContain('signal');
      });
    }
    it(`${file}: queryFn signal'i DESTRUCTURE ediyor`, () => {
      // React Query signal'i queryFn'in context'inde verir; almadan
      // iletmek mümkün değil.
      expect(src).toContain('({ signal })');
    });
  }
});
