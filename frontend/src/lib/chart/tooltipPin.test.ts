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
