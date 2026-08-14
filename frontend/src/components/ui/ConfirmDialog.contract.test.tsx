// @vitest-environment jsdom
//
// ConfirmDialog sözleşmesi — K6 (v0.9.1008, etkileşim denetimi M6a).
//
// NE ÇİVİLİYOR: `window.confirm`in yerine geçen atomun, onun
// KAYBETTİRDİĞİ dört şeyi gerçekten geri verdiğini.
//
//   1. Karar bir Promise olarak DÖNÜYOR (senkron confirm'in yerine
//      geçebilmesinin tek şartı).
//   2. Onay/iptal/Esc üç yolu da AYNI kararı çözüyor ve promise
//      YALNIZ BİR KEZ resolve ediliyor — Esc katmanı kapanırken
//      Modal'ın `onClose`u da koşuyor, iki resolve'lu bir atom
//      sessizce yanlış cevap verirdi.
//   3. Şiddet kademesi var: `danger` onay düğmesini kırmızıya
//      çeviriyor. `confirm()`ün yapısal olarak yapamadığı şey buydu.
//   4. Odak İPTALE gidiyor: Enter'a hazır bir parmak yıkıcı yolu
//      seçmemeli.
import { describe, it, expect, afterEach } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { ConfirmProvider, useConfirm } from './ConfirmDialog';
import { __resetEscLayers, escLayerDepth, topEscLayer } from '@/lib/escLayer';

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function mount(ui: React.ReactElement) {
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => { root!.render(ui); });
}
afterEach(() => {
  act(() => { root?.unmount(); });
  host?.remove();
  root = null; host = null;
  __resetEscLayers();
});

// Modal portal'a render ediyor → düğmeleri BELGEDEN topluyoruz.
const dialog = () => document.querySelector('.modal-dialog') as HTMLElement | null;
const btn = (label: string) =>
  [...document.querySelectorAll<HTMLButtonElement>('.modal-footer button')]
    .find(b => b.textContent?.trim() === label);

function Harness({ onResult, opts }: {
  onResult: (v: boolean) => void;
  opts?: Partial<Parameters<ReturnType<typeof useConfirm>>[0]>;
}) {
  const confirm = useConfirm();
  return (
    <button onClick={() => { void confirm({
      title: 'Delete runbook?',
      body: <b>payments-restart</b>,
      confirmLabel: 'Delete runbook',
      ...opts,
    }).then(onResult); }}>trigger</button>
  );
}

function open(onResult: (v: boolean) => void, opts?: Parameters<typeof Harness>[0]['opts']) {
  mount(<ConfirmProvider><Harness onResult={onResult} opts={opts} /></ConfirmProvider>);
  act(() => { host!.querySelector('button')!.click(); });
}

describe('ConfirmDialog — karar sözleşmesi', () => {
  it('açılmadan önce DOM’da hiçbir diyalog yok', () => {
    mount(<ConfirmProvider><Harness onResult={() => {}} /></ConfirmProvider>);
    expect(dialog()).toBeNull();
  });

  it('onay → true, ve diyalog kapanıyor', async () => {
    let got: boolean | undefined;
    open(v => { got = v; });
    expect(dialog()).not.toBeNull();
    await act(async () => { btn('Delete runbook')!.click(); });
    expect(got).toBe(true);
    expect(dialog()).toBeNull();
  });

  it('iptal → false', async () => {
    let got: boolean | undefined;
    open(v => { got = v; });
    await act(async () => { btn('Vazgeç')!.click(); });
    expect(got).toBe(false);
  });

  it('Esc → false (native confirm’in escLayer’a görünmeyen hâli kapandı)', async () => {
    let got: boolean | undefined;
    open(v => { got = v; });
    // Diyalog AÇIKKEN bir Esc katmanı yığında olmalı: `confirm()`
    // döneminde yığın diyaloğu hiç görmüyordu ve açık bir çekmecenin
    // üstünde Esc davranışı uygulamanın değil TARAYICININ'dı (C5-3).
    expect(escLayerDepth()).toBeGreaterThan(0);
    // Esc'i klavye olayıyla değil KATMANI çağırarak sürüyoruz: gerçek
    // tuş yolu `lib/keyboard.ts`in global registry'sinden geçiyor ve o
    // burada mount değil. Depodaki diğer katman testleri de böyle
    // (pageControlsCollapse.test.tsx). Ölçtüğümüz şey aynı: diyalog
    // yığının TEPESİNDE mi ve tepe çağrılınca false mı dönüyor.
    await act(async () => { topEscLayer()!(); });
    expect(got).toBe(false);
    expect(dialog()).toBeNull();
  });

  it('promise YALNIZ BİR KEZ çözülüyor', async () => {
    const calls: boolean[] = [];
    open(v => { calls.push(v); });
    await act(async () => { btn('Delete runbook')!.click(); });
    // Kapandıktan sonra artık yığında katman kalmamalı — kalsaydı
    // ikinci bir Esc ikinci bir karar üretirdi.
    expect(topEscLayer()).toBeNull();
    expect(calls).toEqual([true]);
  });

  it('danger onay düğmesini KIRMIZI yapıyor, danger’sız yapmıyor', async () => {
    open(() => {}, { danger: true });
    expect(btn('Delete runbook')!.className.split(' ')).toContain('danger');
    await act(async () => { btn('Vazgeç')!.click(); });
    act(() => { root?.unmount(); });
    host?.remove(); root = null; host = null; __resetEscLayers();

    open(() => {}, { confirmLabel: 'Apply', danger: false });
    // primary sınıfsızdır (element-seviyesi `button` kuralı boyar).
    expect(btn('Apply')!.className).toBe('');
  });

  it('onay etiketi ÇAĞIRANIN yazdığı eylem — varsayılan "OK" yok', () => {
    open(() => {}, { confirmLabel: 'Token’ı iptal et' });
    expect(btn('Token’ı iptal et')).toBeTruthy();
    expect(btn('OK')).toBeUndefined();
  });

  it('body render ediliyor — nesnenin ADI diyalogda görünüyor', () => {
    open(() => {});
    expect(dialog()!.textContent).toContain('payments-restart');
  });

  it('odak İPTALDE, onayda değil', async () => {
    open(() => {});
    // Modal odağı bir sonraki tick'te taşıyor (portal mount gecikmesi).
    await act(async () => { await new Promise(r => setTimeout(r, 10)); });
    expect(document.activeElement).toBe(btn('Vazgeç'));
  });

  it('açık bir diyalog üstüne ikinci çağrı gelirse BİRİNCİ false ile kapanır', async () => {
    // Askıda kalan bir await görünmeyen bir hatadır: çağıran handler
    // sonsuza kadar bekler ve operatör "buton çalışmıyor" der.
    const seen: boolean[] = [];
    function Two() {
      const confirm = useConfirm();
      return (
        <button onClick={() => {
          void confirm({ title: 'A', body: 'a', confirmLabel: 'A!' }).then(v => seen.push(v));
          void confirm({ title: 'B', body: 'b', confirmLabel: 'B!' }).then(v => seen.push(v));
        }}>go</button>
      );
    }
    mount(<ConfirmProvider><Two /></ConfirmProvider>);
    await act(async () => { host!.querySelector('button')!.click(); });
    expect(seen).toEqual([false]);          // birinci kapandı
    expect(dialog()!.textContent).toContain('B'); // ikinci ayakta
  });
});
