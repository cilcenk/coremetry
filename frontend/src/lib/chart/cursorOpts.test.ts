import { describe, it, expect, vi } from 'vitest';
import type uPlot from 'uplot';
import { selectRangeSec, buildCursorOpts } from './cursorOpts';

// cursorOpts (Grafana-parite M1) — dört preset'in kopya cursor.drag +
// setSelect bloklarının tekilleştirilmiş çekirdeği. Bu tablo mevcut
// davranışı PİNLER: min-width kazara-drag guard'ı, from<=to sıralaması,
// isFinite guard'ı, onZoom-presence sözleşmesi ve seçim-bandı reset'i.

describe('selectRangeSec', () => {
  // Lineer px→sec eşleme: 0px = 1000s, her px +2s.
  const posToVal = (px: number) => 1000 + px * 2;

  it('normal drag → sıralı [from,to]', () => {
    expect(selectRangeSec({ left: 10, width: 40 }, posToVal)).toEqual({ from: 1020, to: 1100 });
  });

  it('default eşik: width < 4 → null (kazara drag, MLC/OVC/TSP)', () => {
    expect(selectRangeSec({ left: 10, width: 3 }, posToVal)).toBe(null);
  });

  it('width tam eşikte (4) → geçer', () => {
    expect(selectRangeSec({ left: 0, width: 4 }, posToVal)).toEqual({ from: 1000, to: 1008 });
  });

  it('TC eşiği (minWidthPx=2): width 2-3 arası da geçer', () => {
    expect(selectRangeSec({ left: 5, width: 2 }, posToVal, 2)).toEqual({ from: 1010, to: 1014 });
    expect(selectRangeSec({ left: 5, width: 1 }, posToVal, 2)).toBe(null);
  });

  it('select yok (null/undefined) → null', () => {
    expect(selectRangeSec(null, posToVal)).toBe(null);
    expect(selectRangeSec(undefined, posToVal)).toBe(null);
  });

  it('sonlu olmayan dönüşüm (henüz ölçeksiz plot) → null', () => {
    expect(selectRangeSec({ left: 0, width: 10 }, () => NaN)).toBe(null);
    expect(selectRangeSec({ left: 0, width: 10 }, () => Infinity)).toBe(null);
  });

  it('ters (azalan) eşleme bile sıralı döner', () => {
    expect(selectRangeSec({ left: 0, width: 10 }, px => 100 - px)).toEqual({ from: 90, to: 100 });
  });
});

describe('buildCursorOpts', () => {
  it('default: drag x/setScale true, sync yok, setSelect yok (onZoom yok)', () => {
    const o = buildCursorOpts({});
    expect(o.cursor.drag).toEqual({ x: true, y: false, setScale: true });
    expect('sync' in o.cursor).toBe(false);
    expect(o.setSelect).toBeUndefined();
  });

  it('syncKey → cursor.sync.key (kardeş grafik imleç senkronu)', () => {
    expect(buildCursorOpts({ syncKey: 'service:api' }).cursor.sync).toEqual({ key: 'service:api' });
  });

  it('TC şekli: dragX/setScale bayrakları aynen geçer', () => {
    const o = buildCursorOpts({ dragX: false, setScale: false });
    expect(o.cursor.drag).toEqual({ x: false, y: false, setScale: false });
  });

  it('onZoom varsa TEK setSelect hook üretilir (hasZoom presence sözleşmesi)', () => {
    const o = buildCursorOpts({ onZoom: () => {} });
    expect(o.setSelect).toHaveLength(1);
  });

  // Sahte uPlot — hook'un uçtan uca davranışı: posToVal ile sec'e çevir,
  // sıralı çağır, seçim bandını resetle.
  const fakeU = (left: number, width: number) => {
    const setSelect = vi.fn();
    const u = {
      select: { left, width, top: 0, height: 0 },
      posToVal: (px: number, scale: string) => (scale === 'x' ? 500 + px : NaN),
      setSelect,
    } as unknown as uPlot;
    return { u, setSelect };
  };

  it('setSelect hook: onZoom sıralı sec aralığıyla çağrılır + band temizlenir', () => {
    const onZoom = vi.fn();
    const o = buildCursorOpts({ onZoom });
    const { u, setSelect } = fakeU(20, 30);
    o.setSelect![0](u);
    expect(onZoom).toHaveBeenCalledWith(520, 550);
    expect(setSelect).toHaveBeenCalledWith({ left: 0, width: 0, top: 0, height: 0 }, false);
  });

  it('setSelect hook: mini seçim → onZoom da band reset de YOK', () => {
    const onZoom = vi.fn();
    const o = buildCursorOpts({ onZoom });
    const { u, setSelect } = fakeU(20, 3);
    o.setSelect![0](u);
    expect(onZoom).not.toHaveBeenCalled();
    expect(setSelect).not.toHaveBeenCalled();
  });

  it('setSelect hook: TC minWidthPx=2 eşiği hook üstünden de işler', () => {
    const onZoom = vi.fn();
    const o = buildCursorOpts({ onZoom, minWidthPx: 2 });
    const { u } = fakeU(0, 2);
    o.setSelect![0](u);
    expect(onZoom).toHaveBeenCalledWith(500, 502);
  });
});
