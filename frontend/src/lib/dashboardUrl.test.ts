// v0.9.779 — /dashboard adres kuralları.
//
// İki şey sabitleniyor:
//
//  1. REZERVE SET. Dashboard.tsx'teki değişken aynası rezerve olmayan
//     HER paramı siliyor. 'refresh' / 'kiosk' listeye girmezse effect
//     paramı her yazımda yok eder; auto-refresh açılır ve bir sonraki
//     render'da kendiliğinden kapanır — sessiz, "bazen çalışıyor"
//     görünen bir hata sınıfı.
//  2. ARALIK AYRIŞTIRMA. ?refresh= elle düzenlenebilir. Tanımadığımız
//     bir değer KAPALI'ya düşmeli, varsayılan bir aralığa değil:
//     yapıştırılan bir link kimseye istemediği bir yenileme döngüsü
//     miras bırakmamalı.
import { describe, it, expect } from 'vitest';
import {
  DASHBOARD_RESERVED_PARAMS,
  isDashboardVariableParam,
  parseRefreshParam,
  refreshLabel,
  REFRESH_CHOICES,
} from './dashboardUrl';

describe('DASHBOARD_RESERVED_PARAMS', () => {
  it('sayfanın kendi paramlarını kapsar (refresh + kiosk dahil)', () => {
    for (const k of ['id', 'edit', 'range', 'refresh', 'kiosk']) {
      expect(DASHBOARD_RESERVED_PARAMS.has(k)).toBe(true);
    }
  });

  it('rezerve param değişken değeri SAYILMAZ', () => {
    expect(isDashboardVariableParam('refresh')).toBe(false);
    expect(isDashboardVariableParam('kiosk')).toBe(false);
    expect(isDashboardVariableParam('range')).toBe(false);
  });

  it('pano değişkeni adları değişken değeri sayılır', () => {
    expect(isDashboardVariableParam('service')).toBe(true);
    expect(isDashboardVariableParam('namespace')).toBe(true);
  });
});

describe('parseRefreshParam', () => {
  it('yok / boş → kapalı', () => {
    expect(parseRefreshParam(null)).toBe(0);
    expect(parseRefreshParam(undefined)).toBe(0);
    expect(parseRefreshParam('')).toBe(0);
  });

  it('listedeki her seçim aynen döner', () => {
    for (const sec of REFRESH_CHOICES) {
      expect(parseRefreshParam(String(sec))).toBe(sec);
    }
  });

  it('listede olmayan sayı → kapalı (10s dahil — bilinçli yok)', () => {
    expect(parseRefreshParam('10')).toBe(0);
    expect(parseRefreshParam('1')).toBe(0);
    expect(parseRefreshParam('86400')).toBe(0);
  });

  it('çöp / negatif / NaN → kapalı', () => {
    expect(parseRefreshParam('abc')).toBe(0);
    expect(parseRefreshParam('-30')).toBe(0);
    expect(parseRefreshParam('30s')).toBe(0);
    expect(parseRefreshParam('Infinity')).toBe(0);
  });
});

describe('refreshLabel', () => {
  it('her seçim için okunur etiket', () => {
    expect(refreshLabel(0)).toBe('Kapalı');
    expect(refreshLabel(30)).toBe('30s');
    expect(refreshLabel(60)).toBe('1m');
    expect(refreshLabel(300)).toBe('5m');
  });
});
