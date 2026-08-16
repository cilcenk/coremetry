// escLayer — UX denetimi E2 / Ö28 regresyon testleri (v0.9.950).
//
// ORİJİNAL BELİRTİ (iki ayrı kusur, tek kök):
//   1. Drawer açıkken ⌘K'yı Esc'le kapatmak DRAWER'I DA götürüyordu.
//   2. Drawer içindeki bir input'ta Esc, alanı temizlemek yerine
//      ÇEKMECEYİ komple kapatıyordu.
//
// Kök neden: 30'u aşkın bağımsız Esc dinleyicisi, hiçbiri ötekinden
// haberdar değil, öncelik kavramı yok.

import { describe, it, expect, beforeEach } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { pushEscLayer, topEscLayer, escLayerDepth, __resetEscLayers } from './escLayer';

beforeEach(() => __resetEscLayers());

describe('escLayer — LIFO', () => {
  it('boş yığında tepe yok', () => {
    expect(topEscLayer()).toBeNull();
    expect(escLayerDepth()).toBe(0);
  });

  it('EN SON açılan tepede', () => {
    const drawer = () => {}; const palette = () => {};
    pushEscLayer(drawer);
    pushEscLayer(palette);
    expect(topEscLayer()).toBe(palette);
  });

  it('tepe kapanınca ALTTAKİNE düşer — kimse sessizce kaybolmaz', () => {
    const drawer = () => {}; const palette = () => {};
    pushEscLayer(drawer);
    const popPalette = pushEscLayer(palette);
    popPalette();
    expect(topEscLayer()).toBe(drawer);
    expect(escLayerDepth()).toBe(1);
  });

  it('TEPEDE OLMAYAN katman kimliğiyle çıkar — yanlış katman düşmez', () => {
    // Bir çekmece, üstünde bir modal açıkken kapanabilir (rota değişimi).
    // Körü körüne pop yapmak modalı düşürürdü.
    const drawer = () => {}; const modal = () => {};
    const popDrawer = pushEscLayer(drawer);
    pushEscLayer(modal);
    popDrawer();
    expect(topEscLayer()).toBe(modal);
    expect(escLayerDepth()).toBe(1);
  });

  it('aynı handler iki kez kayıtlıysa yalnız BİR kopyası çıkar', () => {
    const h = () => {};
    const pop1 = pushEscLayer(h);
    pushEscLayer(h);
    pop1();
    expect(escLayerDepth()).toBe(1);
    expect(topEscLayer()).toBe(h);
  });

  it('çift pop güvenli (StrictMode çift-cleanup)', () => {
    const h = () => {};
    const pop = pushEscLayer(h);
    pop(); pop();
    expect(escLayerDepth()).toBe(0);
  });

  it('KUSUR 1 senaryosu: palet Esc’i çekmeceyi GÖTÜRMEZ', () => {
    let drawerClosed = false, paletteClosed = false;
    pushEscLayer(() => { drawerClosed = true; });
    const popPalette = pushEscLayer(() => { paletteClosed = true; });
    // Tek Esc → yalnız TEPE.
    topEscLayer()!();
    expect(paletteClosed).toBe(true);
    expect(drawerClosed).toBe(false);
    // Palet kapandı; İKİNCİ Esc çekmeceye gider.
    popPalette();
    topEscLayer()!();
    expect(drawerClosed).toBe(true);
  });
});

describe('keyboard.ts — tek kapı sözleşmesi', () => {
  const kb = readFileSync(join(__dirname, 'keyboard.ts'), 'utf8');

  it('Esc yolu KATMANI sorar', () => {
    expect(kb).toMatch(/if \(combo === 'Escape'\)/);
    expect(kb).toMatch(/topEscLayer\(\)/);
  });

  it('KUSUR 2: eleman niyetini beyan ettiyse katman DOKUNMAZ', () => {
    // defaultPrevented — React sentetik dinleyicileri kök kapsayıcıda,
    // yani bu document dinleyicisinden ÖNCE koşar.
    expect(kb).toMatch(/if \(e\.defaultPrevented\) return;/);
  });

  it('katman varken kayıt defterindeki Escape bağı ATEŞLEMEZ', () => {
    // useTableNav'ın "satır seçimini temizle" kısayolu bir katman
    // açıkken çalışmamalı; yoksa modalı kapatan Esc arkadaki tablonun
    // seçimini de siler.
    const escBlock = kb.slice(kb.indexOf("if (combo === 'Escape')"));
    expect(escBlock.slice(0, 1400)).toMatch(/layer\(\);\s*\n\s*return;/);
  });

  it('İKİNCİ bir document dinleyicisi YOK — sıra yarışı geri gelmesin', () => {
    const esc = readFileSync(join(__dirname, 'escLayer.ts'), 'utf8');
    expect(esc, 'escLayer DOM’a dokunmamalı: ikinci bir dinleyici sıra yarışını geri getirir')
      .not.toMatch(/addEventListener/);
  });
});

// Göç kapsamı: taşınmış yüzeyler kendi document dinleyicilerini GERİ
// ALMAMALI. Bu, düzeltmenin tek tek dosyalarda sessizce çözülmesini
// engelleyen kapı.
describe('göç bütünlüğü (v0.9.950)', () => {
  const MIGRATED = [
    'components/ui/Drawer.tsx',
    'components/ui/Modal.tsx',
    'components/ui/FacetMultiSelect.tsx',
    'components/CommandPalette.tsx',
    'components/HeatmapCellExemplars.tsx',
    'components/SpanDetail.tsx',
    'components/MetricPanel.tsx',
    'components/TimeRangePicker.tsx',
    'components/chart/CorePanel.tsx',
    'features/anomalies/ProblemDetail.tsx',
    'pages/Dashboard.tsx',
    'pages/Messaging.tsx',
    'pages/Trace.tsx',
    'pages/explore/RecentQueries.tsx',
  ];
  const SRC = join(__dirname, '..');

  it('taşınan yüzeyler useEscLayer kullanır', () => {
    const missing = MIGRATED.filter(rel =>
      !readFileSync(join(SRC, rel), 'utf8').includes('useEscLayer('));
    expect(missing, 'Bu dosyalar katmana taşınmıştı; geri alınmış görünüyor.').toEqual([]);
  });

  it('taşınan yüzeylerde ELLE Esc karşılaştırması kalmadı', () => {
    const offenders: string[] = [];
    for (const rel of MIGRATED) {
      const src = readFileSync(join(SRC, rel), 'utf8').replace(/\/\/.*$/gm, '');
      if (/key === 'Escape'|key !== 'Escape'/.test(src)) offenders.push(rel);
    }
    expect(offenders,
      'Elle Esc dinleyicisi geri gelmiş — katman disiplini yalnız HERKES uyarsa çalışır.')
      .toEqual([]);
  });

  it('yeni bir document Esc dinleyicisi eklenmemiş (bütün src taraması)', () => {
    // Element seviyesindeki onKeyDown’lar SERBEST: onlar elemanın kendi
    // niyeti ve defaultPrevented ile katmanı zaten susturabiliyorlar.
    // Yasak olan, document/window seviyesinde İKİNCİ bir Esc otoritesi.
    const walk = (dir: string, rel = ''): string[] => {
      const out: string[] = [];
      for (const e of readdirSync(dir)) {
        const full = join(dir, e);
        const r = rel ? `${rel}/${e}` : e;
        if (statSync(full).isDirectory()) { out.push(...walk(full, r)); continue; }
        if (!/\.tsx?$/.test(e) || /\.test\./.test(e)) continue;
        out.push(r);
      }
      return out;
    };
    const offenders: string[] = [];
    for (const rel of walk(SRC)) {
      if (rel === 'lib/keyboard.ts') continue; // TEK yetkili kapı
      const src = readFileSync(join(SRC, rel), 'utf8').replace(/\/\/.*$/gm, '');
      // Aynı dosyada hem global keydown kaydı hem Esc karşılaştırması.
      const globalKeydown = /(document|window)\.addEventListener\(\s*'keydown'/.test(src);
      const esc = /key === 'Escape'|key !== 'Escape'/.test(src);
      if (globalKeydown && esc) offenders.push(rel);
    }
    expect(offenders,
      'Bu dosyalar document/window seviyesinde Esc dinliyor — katman yığını devre dışı kalır (Ö28 geri gelir).')
      .toEqual([]);
  });
});
