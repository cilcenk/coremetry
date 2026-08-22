// @vitest-environment jsdom
//
// v0.9.1241 — CoSRE kod bağlamı kartının DÜRÜSTLÜK sözleşmesi.
//
// Kartın var olma sebebi tek bir denetim bulgusu: kod-çekme yolu
// FAIL-OPEN, yani başarısızlığı hiçbir ekranda iz bırakmıyordu.
// O yüzden burada ölçülen şey "render ediyor mu" değil, kartın ÜÇ
// dürüstlük iddiası — hepsi çalışma zamanı dalı, tsc göremez:
//
//   (1) "hiç denenmedi" ≠ "%0 isabet". Kutuyu hiç işaretlememiş bir
//       kurulumda %0 yazmak, olmayan bir arıza bildirmek olurdu.
//   (2) ENTEGRASYON çıkmazı (bağlantı/PAT/ağaç) ⚠ ile ayrılır; veri
//       kaynaklı çıkmaz (stack yok, dosya o depoda değil) ayrılmaz —
//       aksi hâlde sağlıklı bir kurulum sürekli kırmızı yanardı.
//   (3) Son hata YAPIŞKAN gösterilir ve YAŞIYLA birlikte: flap eden
//       bir arızayı tek şanslı isabet ekrandan silmemeli.
//
// Ayrıca: sayaçların SÜREÇ BAŞLANGICINDAN beri olduğu başlıkta yazar.
// Bu, kalıcı sayaç yerine süreç-içi atomik seçmenin bedeli; yazmazsak
// operatör restart sonrası düşen sayıyı veri kaybı sanardı.
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { CodeFetchPanel } from './panels';
import type { SystemStats } from '@/lib/types';

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

function render(code: SystemStats['codeFetch']) {
  act(() => { root.render(<CodeFetchPanel code={code} />); });
  return host.textContent ?? '';
}

describe('CodeFetchPanel', () => {
  it('alan hiç gelmezse "hiç denenmedi" der, %0 DEMEZ', () => {
    const text = render(undefined);
    expect(text).toContain('hiç denenmedi');
    expect(text).not.toContain('%0');
  });

  it('deneme 0 ise de "hiç denenmedi" — sıfır isabet oranı yalan olurdu', () => {
    const text = render({ attempts: 0, ok: 0, partial: 0, lastUnix: 0 });
    expect(text).toContain('hiç denenmedi');
    expect(text).not.toContain('%0');
    // Kırılım karoları hiç çizilmez: gösterecek bir şey yok.
    expect(text).not.toContain('Kısmi');
  });

  it('kısmi isabeti İSABET sayar ama ayrı gösterir', () => {
    const text = render({
      attempts: 4, ok: 2, partial: 1, lastUnix: Math.floor(Date.now() / 1000),
      lastOutcome: 'ok', misses: [{ class: 'no-stack', count: 1 }],
    });
    expect(text).toContain('%75');       // (2+1)/4
    expect(text).toContain('3 / 4');
    expect(text).toContain('Kısmi');
  });

  it('süreç başlangıcından beri olduğunu BAŞLIKTA söyler', () => {
    const text = render({ attempts: 1, ok: 1, partial: 0, lastUnix: 1 });
    expect(text).toContain('süreç başlangıcından beri');
    expect(text).toContain('restart sıfırlar');
  });

  it('veri kaynaklı çıkmaz (stack yok / dosya ağaçta yok) ALARM DEĞİL', () => {
    const text = render({
      attempts: 10, ok: 4, partial: 0, lastUnix: 1,
      misses: [{ class: 'no-stack', count: 4 }, { class: 'tree-miss', count: 2 }],
    });
    expect(text).toContain('✓ %40');
    expect(text).not.toContain('⚠');
    // Sınıflar insan diline çevrilir; ham anahtar ekrana basılmaz.
    expect(text).toContain('stack yok');
    expect(text).toContain('dosya ağaçta yok');
    expect(text).not.toContain('no-stack');
  });

  it('entegrasyon çıkmazı ⚠ ile ayrılır ve düzeltme yolunu söyler', () => {
    const text = render({
      attempts: 10, ok: 8, partial: 0, lastUnix: 1,
      misses: [{ class: 'backend-error', count: 2 }],
      lastError: 'TF400813: yetkisiz', lastErrorUnix: Math.floor(Date.now() / 1000) - 120,
    });
    expect(text).toContain('⚠ %80');
    expect(text).toContain('DevOps hatası');
    expect(text).toContain('Kod entegrasyonu');
    // Son hata YAPIŞKAN: %80 isabetin yanında hâlâ görünür, yaşıyla.
    expect(text).toContain('TF400813: yetkisiz');
    expect(text).toMatch(/\d+m önce/);
  });

  it('bilinmeyen bir çıkmaz sınıfını SAKLAMAZ (ham adıyla gösterir)', () => {
    // Backend yeni bir kova eklerse panel onu düşürmemeli: görünmeyen
    // bir kova, tam da bu kartın kapatmaya çalıştığı körlüktür.
    const text = render({
      attempts: 3, ok: 1, partial: 0, lastUnix: 1,
      misses: [{ class: 'yepyeni-sinif', count: 2 }],
    });
    expect(text).toContain('yepyeni-sinif');
  });
});
