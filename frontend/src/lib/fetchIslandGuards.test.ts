// fetchIslandGuards.test.ts — v0.9.939 (UX denetimi C1 · C2 · C3).
//
// Üç kusur AYNI kök nedene bağlıydı: elle yazılmış `useEffect` + `useState`
// fetch adaları, React Query'nin bedava verdiği iki şeyi kaybediyordu —
// keepPreviousData ve iptal.
//
//   C1/Ö20 — Service "Latency distribution": her pencere/op/cluster
//     değişiminde panel BOŞALIP spinner'a düşüyordu (sayfanın en pahalı
//     sorgusu, ≤3s bütçe), üstelik yarış koruması YOKTU: hızlı iki
//     değişiklikte eski pencerenin ızgarası yeninin üstüne yazabiliyordu.
//   C2/Ö18 — Explore heatmap / trace listesi / repeats modları aynı
//     boşaltmayı yapıyordu; ÇİZGİ modu (React Query) yapmıyordu. Aynı
//     sayfada iki farklı akıcılık sınıfı.
//   C3/Ö19 — `cancelled` bayrağı yalnız YANITI atıyordu, İSTEĞİ değil:
//     superseded ham-spans taraması CH'de max_execution_time'a kadar
//     koşmaya devam ediyor, operatörün gerçekten beklediği sorguyu
//     yavaşlatıyordu.
//
// Neden KAYNAK pini: davranışın kendisi (blank olmama) ancak tam bir DOM
// testiyle görülür ve bu depo o ağırlığı taşımıyor. Kaybolması ise tek bir
// satırın geri gelmesiyle olur — `setX(undefined)`. Pin tam olarak o satırı
// bekliyor.
import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const read = (p: string) => readFileSync(resolve(__dirname, p), 'utf8');

// stripComments — pin YORUMLARDA değil KODDA arasın. Bu dosyaların
// yorumları düzeltilen kusuru ADIYLA anlatıyor (`setX(undefined)` geri
// gelmesin diye), yani ham metinde aramak her pini kalıcı olarak yeşil
// yapardı: kusurun tarifi, kusurun kendisi sanılırdı. Bu depoda tekrarlayan
// tarama hatası.
const code = (p: string) =>
  read(p).replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');

describe('C1 — Service latency heatmap (v0.9.939)', () => {
  const src = code('../pages/service/ServiceLatencyHeatmap.tsx');

  it('yarış koruması + gerçek iptal', () => {
    expect(src).toContain('raceGuard()');
    expect(src).toContain('}, g.signal)');
    expect(src).toContain('return g.cancel;');
  });

  it('her değişiklikte paneli BOŞALTMIYOR', () => {
    // setData(undefined) = keep-previous'ın tam tersi; geri gelirse
    // operatör her tıkta grafiğini yeniden kaybeder.
    expect(src).not.toContain('setData(undefined)');
    expect(src).toContain('setBusy(true)');
  });

  it('bayat veriyi taze gibi göstermiyor', () => {
    // Sessiz keep-previous, yanlış pencereyi doğru sanmaya yol açar.
    expect(read('../pages/service/ServiceLatencyHeatmap.tsx')).toContain('yenileniyor');
  });
});

describe('C2 — Explore üç mod (v0.9.939)', () => {
  const src = code('../pages/Explore.tsx');

  it('heatmap / traces / repeats artık ekranı boşaltmıyor', () => {
    for (const call of ['setHeatmap(undefined)', 'setTraces(undefined)', 'setRepeats(undefined)']) {
      expect(src, `${call} geri gelmiş — çizgi modu akıcı, bu mod değil`).not.toContain(call);
    }
  });

  it('üç ağır okuma da iptal edilebilir', () => {
    expect(src).toContain('raceGuard()');
    // spanHeatmap + traces + spanRepeats: üçü de signal alıyor.
    expect((src.match(/}, g\.signal\)/g) ?? []).length).toBeGreaterThanOrEqual(3);
    expect(src).toContain('return g.cancel;');
  });

  it('yenileme izi var', () => {
    expect(read('../pages/Explore.tsx')).toContain('yenileniyor…');
  });
});

describe('C3 — ağır uçlar signal alıyor (v0.9.939)', () => {
  const api = code('./api.ts');

  it('spanHeatmap ve spanRepeats signal parametresi taşıyor', () => {
    // İkisi de ham `spans` taraması. Signal'siz kalırlarsa bayrak yalnız
    // yanıtı atar, sorgu koşmaya devam eder.
    expect(api).toMatch(/spanHeatmap: \([\s\S]{0,200}?\}, signal\?: AbortSignal\)/);
    expect(api).toMatch(/spanRepeats: \([\s\S]{0,400}?\}, signal\?: AbortSignal\)/);
    expect(api).toContain('`/api/spans/heatmap?${qs(params)}`, signal)');
    expect(api).toContain('`/api/spans/repeats?${q}`, signal)');
  });
});
