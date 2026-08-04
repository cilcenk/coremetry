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
  { file: 'endpoints.ts', hooks: ['api.endpoints(', 'api.endpointDetail(', 'api.endpointSplit(', 'api.endpointDownstream('] },
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

describe('pahalı okumalar iptal edilebilir', () => {
  for (const { file, hooks } of HEAVY) {
    const src = stripComments(readFileSync(new URL(`./${file}`, import.meta.url), 'utf8'));
    for (const hook of hooks) {
      it(`${file}: ${hook}…) signal alıyor`, () => {
        const i = src.indexOf(hook);
        expect(i, `${hook} bulunamadı — test bayatladı`).toBeGreaterThan(-1);
        // Çağrının kendi satırında signal geçilmeli.
        const line = src.slice(i, src.indexOf('\n', i));
        expect(line, `${hook} signal iletmiyor — operatör aralığı değiştirince ` +
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
