// @vitest-environment jsdom
//
// v0.9.1133 (AI Faz 2.3) — ŞERİT BİRLEŞMESİ sözleşmesi.
//
// Onaylı mockup problem satırında TEK şerit istiyor: collapsed kök-neden
// çipi (sıfır fetch, v0.8.515'ten beri satırın kalıcı özeti) ve "▸ Ne
// oldu?" yan yana. Kritik olan hangi GÖVDENİN açık olabildiği:
//
//   • İkisi de aynı olayın kanıtını gösteriyor — şerit sıralı aday
//     servisler + deploy + exemplar, kart sinyaller + pivotlar + anlatı.
//     Üst üste açık iki sıralama operatörü "hangisi doğru" tahminine
//     zorlar: v0.9.306'nın iki-çelişen-liste sınıfı, bu kez tek satırda.
//   • Ama çipi ÖLÜ TIK yapmak da çözüm değil (v0.9.592 dersi: tıklanan
//     ama hiçbir şey yapmayan affordance). O yüzden şerit gövdesi
//     istendiğinde host kartı KAPATIR — kural "kart açıkken şerit yasak"
//     değil, "aynı anda tek kanıt yüzeyi".
//
// NEDEN GERÇEK MOUNT: `suppressed` bir render dalı ve `onExpandRequest`
// bir tık zinciri; ikisi de tip-doğru şekilde YANLIŞ bağlanabilir
// (gövdeyi `open` ile çizmeye devam etmek, ya da isteği host'un kapatma
// fonksiyonuna hiç bağlamamak) ve tsc susar.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { useState } from 'react';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import { RootCauseRibbon } from './RootCauseRibbon';
import { api } from '@/lib/api';
import type { RootCauseSummary } from '@/lib/types';

let host: HTMLDivElement;
let root: Root;

const SUMMARY: RootCauseSummary = {
  topSuspect: 'payment-db', topScore: 84, confidence: 0.72,
};

const text = () => host.textContent ?? '';
const button = (label: string) =>
  Array.from(host.querySelectorAll('button')).find(b => (b.textContent ?? '').includes(label));
/** Şeridin GÖVDESİ açık mı — açılınca ilk çizilen şey fan-out spinner'ı. */
const bodyOpen = () => text().includes('Assembling root-cause evidence');
const cardOpen = () => host.querySelector('[data-fake-card]') !== null;

/**
 * MergedStrip — ProblemsSection'ın satır şeridinin anatomisi: kart açık
 * hâli host'ta yaşıyor, şerit onu `suppressed` ile görüyor ve gövdesini
 * istediğinde `onExpandRequest` ile kapatılmasını istiyor.
 */
function MergedStrip() {
  const [insight, setInsight] = useState(false);
  return (
    <div>
      <RootCauseRibbon anchor="problem" id="p1" summary={SUMMARY}
        suppressed={insight}
        onExpandRequest={() => setInsight(false)}
        trailing={
          <button type="button" onClick={() => setInsight(v => !v)}>▸ Ne oldu?</button>
        } />
      {insight && <div data-fake-card>KART</div>}
    </div>
  );
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  // Fan-out ASLA çözülmüyor: gövdenin açık olup olmadığını spinner'dan
  // okuyoruz ve testin hiçbir iddiası ağ cevabına bağlı değil.
  vi.spyOn(api, 'problemRootCause').mockImplementation(() => new Promise(() => {}));
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
});

async function mount() {
  await act(async () => {
    root.render(<MemoryRouter><MergedStrip /></MemoryRouter>);
  });
}

const click = async (el: Element) => { await act(async () => { (el as HTMLElement).click(); }); };

describe('RootCauseRibbon — insight kartıyla birleşik şerit', () => {
  it('collapsed çip AYNEN duruyor: özet + güven, sıfır fetch', async () => {
    await mount();
    expect(text()).toContain('Root cause');
    expect(text()).toContain('payment-db');
    expect(text()).toContain('72%');
    expect(api.problemRootCause).not.toHaveBeenCalled();
    expect(bodyOpen()).toBe(false);
  });

  it('kart AÇIKKEN çip görünür kalır ama gövde ÇİZİLMEZ', async () => {
    await mount();
    await click(button('Ne oldu?')!);
    expect(cardOpen()).toBe(true);
    // Çip bilgi taşıyor; onu gizlemek satırdan veri silmek olurdu.
    expect(text()).toContain('payment-db');
    expect(bodyOpen()).toBe(false);
  });

  it('kart açıkken şerit gövdesi açılırsa KART kapanır (tek kanıt yüzeyi)', async () => {
    await mount();
    await click(button('Ne oldu?')!);
    expect(cardOpen()).toBe(true);

    // Şerit çipine basmak ÖLÜ TIK değil: host kartı kapatır, gövde açılır.
    await click(button('Root cause')!);
    expect(cardOpen()).toBe(false);
    expect(bodyOpen()).toBe(true);
    expect(api.problemRootCause).toHaveBeenCalledTimes(1);
  });

  it('kart, ZATEN AÇIK bir gövdeyi bastırır ve kapanınca gövde GERİ gelir', async () => {
    await mount();
    await click(button('Root cause')!);
    expect(bodyOpen()).toBe(true);

    await click(button('Ne oldu?')!);          // kart aç → gövde bastırılır
    expect(bodyOpen()).toBe(false);

    await click(button('Ne oldu?')!);          // kart kapat
    // Operatörün bıraktığı hâl korunuyor: kart açmak şeridi KALICI
    // olarak kapatmaz, ve ikinci bir fan-out fetch'i de yok.
    expect(bodyOpen()).toBe(true);
    expect(api.problemRootCause).toHaveBeenCalledTimes(1);
  });

  it('suppressed YOKKEN davranış eskisi gibi: aç/kapa tek çipte', async () => {
    await mount();
    await click(button('Root cause')!);
    expect(bodyOpen()).toBe(true);
    await click(button('Root cause')!);
    expect(bodyOpen()).toBe(false);
  });
});
