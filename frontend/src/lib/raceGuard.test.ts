import { describe, it, expect } from 'vitest';
import { raceGuard } from './raceGuard';

// v0.9.857 — UX denetimi K7: Trace detayının fetch'inde (`useEffect([id])` →
// `api.trace(id).then(setSpans)`) ne cancelled bayrağı, ne cleanup, ne
// AbortController vardı. Büyük/yavaş trace A açılıp beklemeden küçük trace B
// açıldığında B önce dönüp render oluyor, sonra A'nın geç yanıtı ÜSTÜNE
// yazıyordu: operatör B'nin URL'i ve B'nin id'si başlıktayken A'nın
// waterfall'unu görüyordu. Veri-doğruluğu ihlali — ekranda spanların başka
// bir trace'e ait olduğunu söyleyen hiçbir şey yok.
//
// Bu testler mekanizmayı pinler: (1) yalnız EN YENİ koşu yazabilir,
// (2) süperseded İSTEK gerçekten iptal edilir (yalnız yanıtı atmak, CH'de
// max_execution_time'a kadar koşan sorguyu bırakırdı — v0.9.603).

describe('raceGuard — K7 bayat yanıt yarışı', () => {
  it('yeni guard yazmaya izinli, iptal edilen değil', () => {
    const g = raceGuard();
    expect(g.ok()).toBe(true);
    g.cancel();
    expect(g.ok()).toBe(false);
  });

  it('cancel() İSTEĞİ de iptal eder — yalnız yanıtı atmak yetmez', () => {
    const g = raceGuard();
    expect(g.signal.aborted).toBe(false);
    g.cancel();
    expect(g.signal.aborted).toBe(true);
  });

  it('cancel() idempotent — React cleanup iki kez çağırabilir', () => {
    const g = raceGuard();
    g.cancel();
    expect(() => g.cancel()).not.toThrow();
    expect(g.ok()).toBe(false);
  });

  it('guard\'lar bağımsız — B\'nin iptali A\'yı susturmaz', () => {
    const a = raceGuard();
    const b = raceGuard();
    b.cancel();
    expect(a.ok()).toBe(true);
    expect(a.signal.aborted).toBe(false);
  });

  it('BİLDİRİLEN SENARYO: geç dönen A, render edilmiş B\'nin üstüne YAZMAZ', async () => {
    // Sayfanın gerçek şekli: her effect koşusu kendi guard'ını kurar, cleanup
    // önceki koşuyu bayatlatır. Bug'lı sürümde bu satırların sonunda
    // rendered === 'A-spans' oluyordu.
    let rendered: string | null = null;
    const runs: Array<{ g: ReturnType<typeof raceGuard>; result: string; delay: number }> = [];

    const start = (result: string, delay: number) => {
      const g = raceGuard();
      runs.push({ g, result, delay });
      return new Promise<void>(resolve => {
        setTimeout(() => {
          if (g.ok()) rendered = result;   // ← guard
          resolve();
        }, delay);
      });
    };

    // A: yavaş trace açıldı. B: operatör beklemeden ikinciyi açtı →
    // effect cleanup A'yı bayatlatır.
    const aDone = start('A-spans', 30);
    runs[0].g.cancel();
    const bDone = start('B-spans', 5);

    await Promise.all([aDone, bDone]);
    expect(rendered).toBe('B-spans');
    expect(runs[0].g.signal.aborted).toBe(true);  // A'nın SORGUSU da durdu
    expect(runs[1].g.signal.aborted).toBe(false);
  });

  it('iptal edilen koşunun HATA dalı da yutulur', async () => {
    // İptal reject eder. Guard'sız bir catch, operatörün kendi gezinmesini
    // "trace yüklenemedi" hatasına çevirirdi (v0.9.603 iptal≠zaman aşımı).
    let errorShown = false;
    const g = raceGuard();
    const p = Promise.reject(new Error('aborted'))
      .catch(() => { if (g.ok()) errorShown = true; });
    g.cancel();
    await p;
    expect(errorShown).toBe(false);
  });
});
