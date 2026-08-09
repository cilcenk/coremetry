import { describe, it, expect } from 'vitest';
import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { toCoreViz } from './panelViz';
import type { PanelVizType } from '@/lib/types';

// v0.9.796 → v0.9.808 — mark → motor kararının saf kapısı.
//
// v0.9.796'da bu dosya İKİ riski koruyordu ve ikisi de sessizdi:
//   1. Yanlış YÖNLENDİRME — stacked-bar v2'ye giderse panel çubuk yerine
//      ALAN çizer (o gün CorePanel'in tek yığını yığılmış alandı).
//      Operatör "bar" seçmiş, ekranda alan görür; hata mesajı yok.
//   2. Yanlış AD — CorePanel markı 'bars', dashboard'ınki 'bar'. Tek
//      harflik sapma CorePanel'in viz union'ında eşleşmez ve panel
//      varsayılan 'line'a düşer: yine sessiz, yine yanlış grafik.
//
// v0.9.808 birinci riski KÖKÜNDEN kaldırdı: CorePanel artık yığılmış
// ÇUBUK markını da taşıyor ('stacked-bars', ters çizim sırasıyla), yani
// beş markın beşi de v2'ye gidiyor ve ayıklayacak bir şey kalmadı.
// isV2Viz bu yüzden silindi — tautoloji bir guard'ı ve onun ölü
// else-dallarını taşımak, okuyucuya "bir seçim var" diye yalan söylerdi.
// Geriye kalan tek risk İKİNCİSİ ve bu dosyanın işi artık o: EŞLEME.
describe('panelViz — mark eşlemesi (beş markın beşi v2 motorunda)', () => {
  const ALL: PanelVizType[] = ['line', 'bar', 'stacked-bar', 'area', 'stacked-area'];

  it('ad ayrışan üç mark doğru CorePanel adına düşer', () => {
    expect(toCoreViz('bar')).toBe('bars');
    expect(toCoreViz('stacked-area')).toBe('stacked');
    expect(toCoreViz('stacked-bar')).toBe('stacked-bars');
  });

  it('adı zaten aynı olan ikisi aynen geçer', () => {
    expect(toCoreViz('area')).toBe('area');
    expect(toCoreViz('line')).toBe('line');
  });

  // YIĞIN AİLESİ BÜTÜN: ikisi de v2'de ve ikisi de kendi markını korur.
  // Bu testin kızarması "yığılmışlardan biri geri düştü" ya da "ikisi aynı
  // marka eşlendi" demek — ikincisi v0.9.796'nın açıkça reddettiği hata
  // (stacked-bar'ı 'stacked'a bağlamak paneli sessizce alana çevirirdi).
  it('yığılmış İKİ mark AYRI CorePanel markına gider — çubuk alana dönmez', () => {
    expect(toCoreViz('stacked-bar')).not.toBe(toCoreViz('stacked-area'));
    expect(toCoreViz('stacked-bar')).toContain('bars');
  });

  // Eşleme TOPLU: her mark bir CorePanel markına düşer, hiçbiri sessizce
  // undefined dönmez (union genişlerse exhaustive switch derlemede,
  // burası çalışma zamanında kızarır).
  it('BEŞ markın hepsi eşlenir — boş dönen yok, çakışan yok', () => {
    const mapped = ALL.map(toCoreViz);
    expect(mapped).toEqual(['line', 'bars', 'stacked-bars', 'area', 'stacked']);
    expect(mapped.some(m => !m)).toBe(false);
    expect(new Set(mapped).size).toBe(ALL.length);
  });
});

// ---------------------------------------------------------------------------
// v0.9.844 — SÖZLEŞME TERSİNE DÖNDÜ (operatör onayı 2026-08-09: "eski
// motoru tamamen sök").
//
// v0.9.808'de bu blok "DashboardViz silinmedi ve silinmemeli" diyordu:
// motor kaçış kapısının bar/alan/yığın yarısı ondan geçiyordu. Kapı
// kalkınca gerekçe de kalktı. Yeni kapı bunun ZITTINI koruyor: eski SVG
// motoru DOSYA OLARAK yok ve panel dağıtımına geri sızamaz. Bir gün "yeni
// mark eklerken oraya da bir dal atayım" refleksi gelirse burada kızarır.
// ---------------------------------------------------------------------------
describe('eski SVG motoru SÖKÜLDÜ (v0.9.844)', () => {
  const src = readFileSync(
    resolve(__dirname, './PanelRenderer.tsx'), 'utf8',
  ).replace(/\/\/.*$/gm, '');

  it('DashboardViz.tsx dosyası YOK', () => {
    expect(existsSync(resolve(__dirname, '../DashboardViz.tsx'))).toBe(false);
  });

  it('PanelRenderer eski motoru ne import eder ne çağırır', () => {
    expect(src).not.toMatch(/DashboardViz/);
  });

  it('PanelRenderer\'da motor bayrağı YOK — dallanmasız tek yol', () => {
    expect(src).not.toMatch(new RegExp('charts' + 'V2'));
  });

  it('mark ayıklaması kalktı — isV2Viz izi YOK', () => {
    expect(src).not.toMatch(/isV2Viz/);
  });
});
