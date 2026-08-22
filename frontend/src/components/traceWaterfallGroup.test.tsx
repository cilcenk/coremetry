// @vitest-environment jsdom
//
// traceWaterfallGroup.test.tsx — v0.9.1277.
//
// `groupSimilar` v0.9.226'da yazıldı ve HİÇBİR çağıran onu `true`
// geçmedi — 1000+ sürüm boyunca ölü kod. Bu dilim ona ilk gerçek
// tüketiciyi verdiği için, uyanan kodun DAVRANIŞINI çiviliyoruz.
// Kaynak taramasıyla ölçülemez: gruplama bir useMemo dalı, kimlik
// çözümü bir render kararı.
//
// Uyandırırken çıkan İKİ KIRIK burada nokta iddia olarak duruyor:
//
//  1. SENTETİK ID SEÇİMİ. Grup satırının `spanId`i "group:<parent>:…"
//     — gerçek bir span değil. Satıra tıklamak `onSelect`e o dizeyi
//     veriyordu; /trace onu `spans.find(...)` ile arayıp bulamıyor,
//     yani SpanDetail paneli AÇILMIYOR ve `?span=group:…` URL'e
//     yazılıyordu (paylaşılan link hiçbir şey seçmez).
//  2. FİLTRE SÖNÜKLEŞMESİ. `matchIds` gerçek span id'leri taşır;
//     sentetik id hiçbirinde yok, dolayısıyla filtre AÇIKKEN gruplama
//     açmak filtrenin BULDUĞU satırları `.wf-dim` yapıyordu. Tam da
//     yeni çipin akışı (filtreyi kur + grupla) bu ikisini arka arkaya
//     tetikliyor, yani kırık ilk tıklamada görünürdü.
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { TraceWaterfall } from './TraceWaterfall';
import type { SpanRow } from '@/lib/types';

let host: HTMLDivElement;
let root: Root;

// jsdom ResizeObserver uygulamıyor; şelale isim kolonunu ölçmek için
// onu mount'ta kuruyor. Shim olmadan HER vaka ReferenceError ile düşer.
// Ölçüm 0 kalır — bu testler genişlikle değil, satır KİMLİĞİYLE ilgili.
class NoopResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

beforeEach(() => {
  if (!('ResizeObserver' in globalThis)) {
    (globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver =
      NoopResizeObserver;
  }
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

function span(p: Partial<SpanRow> & { spanId: string }): SpanRow {
  return {
    traceId: 't1', parentSpanId: '', serviceName: 'orders', name: 'op',
    startTime: 1_000_000_000, endTime: 1_010_000_000,
    statusCode: 'ok', kind: 'client', attributes: {},
    ...p,
  } as unknown as SpanRow;
}

// Kök + aynı (servis, ad) 6 kardeş DB span'i + farklı adlı 1 kardeş.
// Klasik N+1 şekli.
const SPANS: SpanRow[] = [
  span({ spanId: 'root', name: 'GET /orders', serviceName: 'api',
         startTime: 0, endTime: 100_000_000 }),
  ...Array.from({ length: 6 }, (_, i) => span({
    spanId: `db${i}`, parentSpanId: 'root',
    serviceName: 'orders', name: 'SELECT items',
    startTime: 10_000_000 + i * 5_000_000,
    // db3 en uzun → temsilci (rep) o olmalı.
    endTime: 10_000_000 + i * 5_000_000 + (i === 3 ? 20_000_000 : 4_000_000),
  })),
  span({ spanId: 'other', parentSpanId: 'root',
         serviceName: 'orders', name: 'COMMIT',
         startTime: 60_000_000, endTime: 62_000_000 }),
];

function render(props: Partial<React.ComponentProps<typeof TraceWaterfall>> = {}) {
  act(() => {
    root.render(
      <TraceWaterfall spans={SPANS} selectedId={null} onSelect={() => {}} {...props} />,
    );
  });
}

const rows = () => [...host.querySelectorAll('.wf-row')];
const groupBadges = () => [...host.querySelectorAll('.wf-group')];

describe('TraceWaterfall × groupSimilar', () => {
  it('kapalıyken her span kendi satırında, ×N rozeti yok', () => {
    render({ groupSimilar: false });
    expect(rows()).toHaveLength(SPANS.length);
    expect(groupBadges()).toHaveLength(0);
  });

  it('açıkken 6 kardeş TEK ×6 satırına katlanıyor', () => {
    render({ groupSimilar: true });
    const badges = groupBadges();
    expect(badges).toHaveLength(1);
    expect(badges[0].textContent).toBe('×6');
    // root + grup satırı + grup temsilcisinin (çocuğu yok) + COMMIT.
    expect(rows()).toHaveLength(3);
    // Katlanmayan kardeş adıyla duruyor.
    expect(host.textContent).toContain('COMMIT');
  });

  it('grup satırına tıklamak GERÇEK temsilci span id\'sini seçiyor', () => {
    // Kırık 1: sentetik "group:root:0:…" dizesi geliyordu.
    const picked: string[] = [];
    render({ groupSimilar: true, onSelect: id => picked.push(id) });
    const groupRow = groupBadges()[0].closest('.wf-row') as HTMLElement;
    act(() => { groupRow.click(); });
    expect(picked).toHaveLength(1);
    expect(picked[0]).toBe('db3');            // en uzun üye = temsilci
    expect(picked[0].startsWith('group:')).toBe(false);
    // data-span-id de gerçek id taşımalı: AI çekmecesi kanıt satırını
    // bununla bulup kaydırıyor.
    expect(groupRow.getAttribute('data-span-id')).toBe('db3');
  });

  it('seçili üye grup satırını SEÇİLİ gösteriyor', () => {
    render({ groupSimilar: true, selectedId: 'db3' });
    const groupRow = groupBadges()[0].closest('.wf-row')!;
    expect(groupRow.className).toContain('wf-sel');
  });

  it('filtre eşleşmesi grup satırını SÖNÜKLEŞTİRMİYOR', () => {
    // Kırık 2: yeni çipin akışı (filtre + gruplama) tam bu bileşim.
    render({
      groupSimilar: true,
      matchIds: new Set(['db0', 'db1', 'db2', 'db3', 'db4', 'db5']),
    });
    const groupRow = groupBadges()[0].closest('.wf-row')!;
    expect(groupRow.className).toContain('wf-match');
    expect(groupRow.className).not.toContain('wf-dim');
    // Eşleşmeyen kardeş sönük kalmalı — dimming'i topyekûn kapatmadık.
    const commitRow = rows().find(r => r.textContent?.includes('COMMIT'))!;
    expect(commitRow.className).toContain('wf-dim');
  });

  it('kritik yol üyesi grup satırına şeridi taşıyor', () => {
    render({ groupSimilar: true, criticalPathIds: new Set(['root', 'db3']) });
    const groupRow = groupBadges()[0].closest('.wf-row')!;
    expect(groupRow.className).toContain('wf-critical');
  });
});

describe('TraceWaterfall × ×N grupla anahtarı', () => {
  it('onGroupSimilarChange verilmezse başlıkta anahtar YOK', () => {
    render({ groupSimilar: false });
    expect(host.querySelector('.wf-grp-toggle')).toBeNull();
  });

  it('anahtar tıklanınca çağırana TERS değeri bildiriyor', () => {
    const seen: boolean[] = [];
    render({ groupSimilar: false, onGroupSimilarChange: v => seen.push(v) });
    const t = host.querySelector('.wf-grp-toggle') as HTMLElement;
    expect(t).not.toBeNull();
    expect(t.getAttribute('aria-pressed')).toBe('false');
    act(() => { t.click(); });
    expect(seen).toEqual([true]);

    render({ groupSimilar: true, onGroupSimilarChange: v => seen.push(v) });
    const t2 = host.querySelector('.wf-grp-toggle') as HTMLElement;
    expect(t2.className).toContain('on');
    expect(t2.getAttribute('aria-pressed')).toBe('true');
    act(() => { t2.click(); });
    expect(seen).toEqual([true, false]);
  });
});
