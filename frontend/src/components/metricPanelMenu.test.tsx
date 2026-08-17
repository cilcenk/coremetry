// @vitest-environment jsdom
//
// MetricPanel × devredilen menü — v0.9.1163 (operatör-raporlu: "servis
// Overview panellerinde çift ⋯", ekran görüntülü).
//
// Kusur bir SAYIydı: aynı köşede iki kebap tetiği. Dış sarmalayıcı
// (MetricPanel) kapı eylemlerini, sardığı CorePanel kendi panel eylemlerini
// taşıyordu. Çözüm "menü sahibi tektir": sarmalayıcı bastırır ve eylemlerini
// devreder (lib/chart/panelMenu.ts sözleşmesi).
//
// BURADA ölçülen, sarmalayıcının KENDİ yarısı:
//   (a) bastırılmayan çağrı bayt bayt eski davranışta (kebap + dört satır),
//   (b) bastırılan çağrı hiç kebap çizmez AMA eylemleri çocuğun eline verir
//       — "bastır" ile "devret" tek harekettir; ayrılırsa affordance sessizce
//       ölür (bu depoda tekrar eden sınıf),
//   (c) Explore adresi metricExploreHref'ten gelir, yani v0.9.1161'in
//       deneme-modu (?metricsrc=) kablosu devredilen satıra da taşınır.
//
// İki tetiğin AYNI AĞAÇTA sayılması CorePanel.smoke.test.tsx'te (orada
// @grafana/ui stub'ı var, gerçek panel mount edilebiliyor).

import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { MetricPanel } from './MetricPanel';
import { metricQuery, decodeMetricQuery } from '@/lib/metricQuery';
import type { PanelMenuAction } from '@/lib/chart/panelMenu';

let host: HTMLDivElement;
let root: Root;
let lastPath = '';
let lastSearch = '';
const ORIGINAL_URL = '/';

function Probe() {
  const l = useLocation();
  lastPath = l.pathname;
  lastSearch = l.search;
  return null;
}

function mount(node: React.ReactNode): HTMLDivElement {
  act(() => {
    root.render(<MemoryRouter><Probe />{node}</MemoryRouter>);
  });
  return host;
}

beforeEach(() => {
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
  lastPath = ''; lastSearch = '';
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  // metricExploreHref, deneme modunu CANLI sayfa URL'inden okuyor
  // (currentMetricSource). Bir testin bıraktığı ?metricsrc= sonraki testlere
  // sızarsa adresler sessizce farklı çıkar.
  window.history.replaceState({}, '', ORIGINAL_URL);
});

const MQ = metricQuery({
  metric: 'duration_milliseconds_bucket', agg: 'avg', unit: 'ms',
  filters: { 'service.name': 'checkout' },
});

const kebabs = (el: HTMLElement) =>
  Array.from(el.querySelectorAll('button[aria-haspopup="menu"]'));
const rowLabels = (el: HTMLElement) =>
  Array.from(el.querySelectorAll('[role="menuitem"]')).map(n => n.textContent);

describe('MetricPanel — kebap bastırma (v0.9.1163)', () => {
  it('bastırılmadan: TEK kebap + dört kapı satırı (eski davranış)', () => {
    const el = mount(
      <MetricPanel compact menuOnly title="KPI" metricQuery={MQ}>
        <div data-testid="body">karo</div>
      </MetricPanel>);
    expect(kebabs(el).length).toBe(1);
    expect(el.querySelector('[data-testid="body"]')).not.toBeNull();
    act(() => { kebabs(el)[0].dispatchEvent(new MouseEvent('click', { bubbles: true })); });
    expect(rowLabels(el)).toEqual([
      '⤢ Explore', '✎ Edit', '⟨⟩ View query', '⧉ Copy link',
    ]);
  });

  it('bastırılınca HİÇ kebap çizilmez — ne tetik ne dropdown', () => {
    const el = mount(
      <MetricPanel compact menuOnly suppressMenu title="Chart" metricQuery={MQ}>
        {() => <div data-testid="body">grafik</div>}
      </MetricPanel>);
    expect(kebabs(el).length).toBe(0);
    expect(el.querySelector('[role="menu"]')).toBeNull();
    // Gövde AYNEN çizilir: bastırma bir görünürlük kararı, çocuğu etkilemez.
    expect(el.querySelector('[data-testid="body"]')).not.toBeNull();
  });

  it('bastırma DEVİRLE bir arada gelir: çocuk dört eylemi eline alır', () => {
    // Holder ile yakalıyoruz: `let x = null` yazımında TS, callback içindeki
    // atamayı akışta göremeyip değişkeni `null`a daraltıyor.
    const cap: { a: PanelMenuAction[] | null } = { a: null };
    mount(
      <MetricPanel compact menuOnly suppressMenu title="Chart" metricQuery={MQ}>
        {(actions) => { cap.a = actions; return <div />; }}
      </MetricPanel>);
    expect(cap.a).not.toBeNull();
    expect(cap.a!.map(a => a.key)).toEqual(['explore', 'edit', 'view', 'copy']);
    expect(cap.a!.map(a => a.label)).toEqual([
      '⤢ Explore', '✎ Edit', '⟨⟩ View query', '⧉ Copy link',
    ]);
    // Kopya satırı menüyü açık bırakır: geri bildirim ('⧉ Copied') satırın
    // KENDİ etiketinde okunuyor (v0.8.550'nin dürüstlük kuralının devamı).
    expect(cap.a!.find(a => a.key === 'copy')!.keepOpen).toBe(true);
    expect(cap.a!.filter(a => a.keepOpen).length).toBe(1);
  });

  it('devredilen Explore, panelin KENDİ descriptor\'ını açar', () => {
    const cap: { a: PanelMenuAction[] | null } = { a: null };
    mount(
      <MetricPanel compact menuOnly suppressMenu title="Chart" metricQuery={MQ}>
        {(actions) => { cap.a = actions; return <div />; }}
      </MetricPanel>);
    act(() => { cap.a!.find(a => a.key === 'explore')!.onClick(); });
    expect(lastPath).toBe('/explore');
    const m = new URLSearchParams(lastSearch).get('m')!;
    const back = decodeMetricQuery(m)!;
    expect(back.metric).toBe('duration_milliseconds_bucket');
    expect(back.agg).toBe('avg');
    expect(back.filters).toEqual({ 'service.name': 'checkout' });
  });

  it('Edit aynı adrese &edit=1 ekler (kapı ailesinin tamamı devrediliyor)', () => {
    const cap: { a: PanelMenuAction[] | null } = { a: null };
    mount(
      <MetricPanel compact menuOnly suppressMenu title="Chart" metricQuery={MQ}>
        {(actions) => { cap.a = actions; return <div />; }}
      </MetricPanel>);
    act(() => { cap.a!.find(a => a.key === 'edit')!.onClick(); });
    expect(lastPath).toBe('/explore');
    expect(new URLSearchParams(lastSearch).get('edit')).toBe('1');
  });

  // v0.9.1161 KABLOSU — devir onu KIRMAMALI. Panel VM verisi gösterirken
  // tıkla-Explore'un CH'de açılması, operatörün "aynı metriğin eski hâli"ne
  // bakması demekti (o sürümün operatör-raporlu kusuru). Adres tek yerden
  // (metricExploreHref) türediği için devredilen satıra bedava geliyor —
  // burada GERÇEKTEN geldiğini ölçüyoruz, satırı okumakla yetinmiyoruz.
  it('deneme modu (?metricsrc=) devredilen Explore adresinde de taşınır', () => {
    window.history.replaceState({}, '', '/services/checkout?metricsrc=vm');
    const cap: { a: PanelMenuAction[] | null } = { a: null };
    mount(
      <MetricPanel compact menuOnly suppressMenu title="Chart" metricQuery={MQ}>
        {(actions) => { cap.a = actions; return <div />; }}
      </MetricPanel>);
    act(() => { cap.a!.find(a => a.key === 'explore')!.onClick(); });
    expect(new URLSearchParams(lastSearch).get('metricsrc')).toBe('vm');
  });

  it('adres TEK kaynaktan: satırlar kendi href\'ini kurmuyor', () => {
    const src = readFileSync(resolve(__dirname, 'MetricPanel.tsx'), 'utf8');
    expect(src).toMatch(/const href = metricExploreHref\(mq\)/);
    // İkinci bir adres kurma yolu = withMetricSource'un atlanabildiği yol.
    expect((src.match(/metricExploreHref\(/g) ?? []).length).toBe(2); // import + tek çağrı
    expect(src).not.toMatch(/'\/explore\?m=/);
  });
});

// ---------------------------------------------------------------------------
// ÇAĞRI YERİ KAPISI — bastır/devret ÇİFTİ Overview'da gerçekten kurulu mu.
//
// tsc bu çifti zaten zorluyor (ayrık birleşim: suppressMenu → fonksiyon
// çocuk), ama fonksiyon çocuk eylemleri ALMAYABİLİR — `{() => <X/>}` yazıp
// menuExtra'yı hiç geçirmemek derlenir ve kapı eylemleri sessizce kaybolur.
// Bu yüzden kapı EŞLEŞMEYİ sayıyor.
//
// Kapsam bilinçli DAR: yalnız Overview. Devir bugün yalnız orada var; başka
// bir sayfa devretmeye başlayınca bu kapı ONU ölçmez ve sayı da tutmaz —
// yani gate GÜNCELLENMEK zorunda kalır (sessizce geçmez).
// ---------------------------------------------------------------------------
describe('Overview çağrı yerleri (v0.9.1163)', () => {
  // Yorumları SOY: bu sürümün açıklama blokları `suppressMenu` kelimesini
  // geçiriyor ve sayım onları eylem sanardı. Sıra önemli: JSX yorumları
  // {/* … */} blok formunda, önce onlar.
  const body = readFileSync(
    resolve(__dirname, '../pages/service/Overview.tsx'), 'utf8',
  ).replace(/\/\*[\s\S]*?\*\//g, '')
    .split('\n').map(l => l.replace(/\/\/.*$/, '')).join('\n');

  const tags = body.split('<MetricPanel ').slice(1);
  const heads = tags.map(s => s.slice(0, s.indexOf('>')));

  it('altı MetricPanel çağrısı var: üç KPI karosu + üç grafik', () => {
    expect(heads.length).toBe(6);
  });

  it('yalnız GRAFİK panelleri devrediyor (karoların içinde menü yok)', () => {
    const delegated = heads.filter(h => h.includes('suppressMenu'));
    expect(delegated.length).toBe(3);
    for (const h of delegated) {
      expect(h).toMatch(/title="(Response time|Throughput|Failure rate)"/);
      // Grafikler kendi tık jestlerinin sahibi (drag-zoom / pin / isolate) →
      // gövde-tıklaması kapalı kalmalı (v0.9.362 gerekçesi).
      expect(h).toContain('menuOnly');
    }
    // Karolar KEBABINI KORUR: içlerinde ikinci bir menü yok, bastırmak
    // affordance'ı hiç yerine koymadan silmek olurdu.
    expect(heads.filter(h => !h.includes('suppressMenu')).length).toBe(3);
  });

  it('🔴 her bastırma bir DEVİRLE eşleşiyor (3 ↔ 3)', () => {
    expect((body.match(/suppressMenu/g) ?? []).length).toBe(3);
    expect((body.match(/menuExtra=\{doorway\}/g) ?? []).length).toBe(3);
    // Devreden her çağrının gövdesi eylemleri ADIYLA alıyor.
    for (const s of tags) {
      const head = s.slice(0, s.indexOf('>'));
      if (!head.includes('suppressMenu')) continue;
      expect(s.slice(s.indexOf('>') + 1).trimStart()).toMatch(/^\{\(doorway\) => \(/);
    }
  });
});
