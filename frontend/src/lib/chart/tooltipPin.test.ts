// tooltipPin — Grafana-parite #2. Tıkla→pin karar çekirdeğinin kontratı:
// drag kuyruğu asla pin durumunu değiştirmez, pinliyken her düz tık çözer,
// veri-noktasız (idx yok) tık boş tooltip pinlemez, idx 0 geçerli bir pindir.

import { describe, it, expect } from 'vitest';
import { decidePinClick } from './tooltipPin';

describe('decidePinClick — pin', () => {
  it('unpinned + valid cursor idx + no drag → pin at that idx', () => {
    expect(decidePinClick({ pinnedIdx: null, cursorIdx: 17, dragPx: 0 }))
      .toEqual({ action: 'pin', idx: 17 });
  });

  it('idx 0 is a valid pin target (falsy-guard regression)', () => {
    expect(decidePinClick({ pinnedIdx: null, cursorIdx: 0, dragPx: 0 }))
      .toEqual({ action: 'pin', idx: 0 });
  });

  it('dragPx omitted counts as a plain click (presets without drag tracking)', () => {
    expect(decidePinClick({ pinnedIdx: null, cursorIdx: 3 }))
      .toEqual({ action: 'pin', idx: 3 });
  });
});

describe('decidePinClick — unpin', () => {
  it('pinned + plain click → unpin (second click releases)', () => {
    expect(decidePinClick({ pinnedIdx: 5, cursorIdx: 9, dragPx: 0 }))
      .toEqual({ action: 'unpin' });
  });

  it('pinned + click with NO cursor idx still unpins (click anywhere on plot)', () => {
    expect(decidePinClick({ pinnedIdx: 5, cursorIdx: null, dragPx: 0 }))
      .toEqual({ action: 'unpin' });
  });

  it('pinnedIdx 0 counts as pinned (falsy-guard regression)', () => {
    expect(decidePinClick({ pinnedIdx: 0, cursorIdx: 4, dragPx: 0 }))
      .toEqual({ action: 'unpin' });
  });
});

describe('decidePinClick — ignore', () => {
  it('drag tail (dragPx > threshold) never pins', () => {
    expect(decidePinClick({ pinnedIdx: null, cursorIdx: 8, dragPx: 40 }))
      .toEqual({ action: 'ignore' });
  });

  it('drag tail never UNpins either (zoom gesture leaves the pin alone)', () => {
    expect(decidePinClick({ pinnedIdx: 2, cursorIdx: 8, dragPx: 40 }))
      .toEqual({ action: 'ignore' });
  });

  it('dragPx exactly at the threshold is a drag (>= — zoom fires at width>=min, pin must not)', () => {
    // selectRangeSec(width:4, minWidthPx:4) ZOOM ateşler; aynı 4px drag'in
    // click kuyruğu pin'e de dokunursa tek jest çift eylem olur (review 8/8 #1).
    expect(decidePinClick({ pinnedIdx: null, cursorIdx: 8, dragPx: 4 }))
      .toEqual({ action: 'ignore' });
  });

  it('dragPx just under the threshold is still a click', () => {
    expect(decidePinClick({ pinnedIdx: null, cursorIdx: 8, dragPx: 3 }))
      .toEqual({ action: 'pin', idx: 8 });
  });

  it('fractional dragPx at/over the threshold is a drag (browser-zoom coords)', () => {
    expect(decidePinClick({ pinnedIdx: null, cursorIdx: 8, dragPx: 4.4 }))
      .toEqual({ action: 'ignore' });
  });

  it('custom dragThresholdPx is honoured (TC brush minWidthPx:2 parity)', () => {
    expect(decidePinClick({ pinnedIdx: null, cursorIdx: 8, dragPx: 3, dragThresholdPx: 2 }))
      .toEqual({ action: 'ignore' });
  });

  it('custom dragThresholdPx boundary: dragPx == threshold ignores, below pins', () => {
    expect(decidePinClick({ pinnedIdx: null, cursorIdx: 8, dragPx: 2, dragThresholdPx: 2 }))
      .toEqual({ action: 'ignore' });
    expect(decidePinClick({ pinnedIdx: null, cursorIdx: 8, dragPx: 1, dragThresholdPx: 2 }))
      .toEqual({ action: 'pin', idx: 8 });
  });

  it('double-click clicks (detail > 1) never pin (no pin-unpin flash)', () => {
    expect(decidePinClick({ pinnedIdx: null, cursorIdx: 8, dragPx: 0, detail: 2 }))
      .toEqual({ action: 'ignore' });
  });

  it('double-click clicks (detail > 1) never UNpin either (dblclick listener owns release)', () => {
    expect(decidePinClick({ pinnedIdx: 5, cursorIdx: 8, dragPx: 0, detail: 2 }))
      .toEqual({ action: 'ignore' });
  });

  it('detail 1 / omitted counts as a plain click', () => {
    expect(decidePinClick({ pinnedIdx: null, cursorIdx: 8, dragPx: 0, detail: 1 }))
      .toEqual({ action: 'pin', idx: 8 });
  });

  it.each([null, undefined, -1] as const)(
    'unpinned + cursorIdx %s → ignore (no data point under the cursor)',
    (idx) => {
      expect(decidePinClick({ pinnedIdx: null, cursorIdx: idx, dragPx: 0 }))
        .toEqual({ action: 'ignore' });
    },
  );
});

// ---------------------------------------------------------------------------
// decidePinGesture (v0.9.792) — CorePanel'in DOLU tık zincirinde pin.
//
// Buradaki tek soru: bir tıkı pin mi sahiplendi, yoksa çağıranın ◆/bucket/
// panel zinciri mi işlemeli? Tablo, MLC v1 sözleşmesinin (:387) üç köşesini
// çiviler: (a) düz tık pin yokken zincire düşer, (b) modifiyeli tık pinler ve
// zincire ASLA düşmez, (c) PİNLİYKEN her tık unpin'e gider — düz olsun
// modifiyeli olsun — ve yine zincire düşmez.
// ---------------------------------------------------------------------------
import { decidePinGesture, type PinGesture } from './tooltipPin';

describe('decidePinGesture — jest yönlendirmesi', () => {
  type Row = {
    name: string;
    pinnedIdx: number | null;
    cursorIdx: number | null | undefined;
    shiftKey?: boolean;
    altKey?: boolean;
    dragPx?: number;
    detail?: number;
    want: PinGesture;
  };

  const rows: Row[] = [
    // (a) pin yok + modifiye yok → zincir çağıranda sürer.
    { name: 'düz tık, pin yok → passthrough (◆/bucket zinciri işler)',
      pinnedIdx: null, cursorIdx: 7, dragPx: 0, want: { action: 'passthrough' } },
    { name: 'düz tık, pin yok, imleç boşta → yine passthrough',
      pinnedIdx: null, cursorIdx: null, dragPx: 0, want: { action: 'passthrough' } },

    // (b) modifiyeli tık pinler — İKİ jest de aynı kapı.
    { name: 'Shift+tık (operatör jesti) → pin',
      pinnedIdx: null, cursorIdx: 7, shiftKey: true, dragPx: 0, want: { action: 'pin', idx: 7 } },
    { name: 'Alt+tık (MLC v1 kas hafızası) → pin',
      pinnedIdx: null, cursorIdx: 7, altKey: true, dragPx: 0, want: { action: 'pin', idx: 7 } },
    { name: 'Shift+Alt birlikte → yine tek pin',
      pinnedIdx: null, cursorIdx: 3, shiftKey: true, altKey: true, dragPx: 0,
      want: { action: 'pin', idx: 3 } },
    { name: 'idx 0 modifiyeli tıkta da geçerli hedef',
      pinnedIdx: null, cursorIdx: 0, shiftKey: true, dragPx: 0, want: { action: 'pin', idx: 0 } },

    // (b′) modifiyeli ama pinlenemez tık YUTULUR — zincire düşmez.
    { name: 'Shift+sürükleme → swallow (exemplar çekmecesi açılmaz)',
      pinnedIdx: null, cursorIdx: 7, shiftKey: true, dragPx: 40, want: { action: 'swallow' } },
    { name: 'Alt+çift-tık click\'i (detail 2) → swallow',
      pinnedIdx: null, cursorIdx: 7, altKey: true, dragPx: 0, detail: 2, want: { action: 'swallow' } },
    { name: 'Shift+tık ama imleç veri noktasında değil → swallow',
      pinnedIdx: null, cursorIdx: null, shiftKey: true, dragPx: 0, want: { action: 'swallow' } },
    { name: 'Shift+tık, cursorIdx -1 → swallow',
      pinnedIdx: null, cursorIdx: -1, shiftKey: true, dragPx: 0, want: { action: 'swallow' } },

    // (c) PİNLİYKEN her tık çözer ve zincire düşmez (MLC:387 birebir).
    { name: 'pinliyken DÜZ tık → unpin (bucket\'a DÜŞMEZ)',
      pinnedIdx: 5, cursorIdx: 9, dragPx: 0, want: { action: 'unpin' } },
    { name: 'pinliyken Shift+tık → unpin',
      pinnedIdx: 5, cursorIdx: 9, shiftKey: true, dragPx: 0, want: { action: 'unpin' } },
    { name: 'pinliyken Alt+tık → unpin',
      pinnedIdx: 5, cursorIdx: 9, altKey: true, dragPx: 0, want: { action: 'unpin' } },
    { name: 'pinliyken imleç boşta düz tık → yine unpin',
      pinnedIdx: 5, cursorIdx: null, dragPx: 0, want: { action: 'unpin' } },
    { name: 'pin 0 (falsy) da çözülür',
      pinnedIdx: 0, cursorIdx: 4, dragPx: 0, want: { action: 'unpin' } },

    // (c′) pinliyken sürükleme/çift-tık pin'e DOKUNMAZ ama tıkı da yutar:
    // zoom jestinin kuyruğu bir exemplar açamaz.
    { name: 'pinliyken sürükleme kuyruğu → swallow (pin korunur)',
      pinnedIdx: 5, cursorIdx: 9, dragPx: 40, want: { action: 'swallow' } },
    { name: 'pinliyken çift-tık click\'i → swallow (dblclick dinleyicisi çözer)',
      pinnedIdx: 5, cursorIdx: 9, dragPx: 0, detail: 2, want: { action: 'swallow' } },
  ];

  it.each(rows)('$name', ({ want, ...args }) => {
    expect(decidePinGesture(args)).toEqual(want);
  });

  it('eşik decidePinClick ile AYNI kanaldan geçer (dragThresholdPx)', () => {
    expect(decidePinGesture({
      pinnedIdx: null, cursorIdx: 8, shiftKey: true, dragPx: 3, dragThresholdPx: 2,
    })).toEqual({ action: 'swallow' });
    expect(decidePinGesture({
      pinnedIdx: null, cursorIdx: 8, shiftKey: true, dragPx: 1, dragThresholdPx: 2,
    })).toEqual({ action: 'pin', idx: 8 });
  });
});
