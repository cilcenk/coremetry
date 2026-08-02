// v0.9.561 — /metrics görünüm kodeki testleri.
//
// Varsayılanı çevirmek tek satır gibi görünüyordu ama iki sözleşmeyi
// sessizce kırabilirdi. Bu testler o iki kırılmayı sabitliyor.
import { describe, it, expect } from 'vitest';
import {
  DEFAULT_METRICS_VIEW,
  metricsViewFromParam,
  metricsViewUrlValue,
  shouldRedirectLegacyMetric,
} from './metricsView';

describe('metricsViewFromParam', () => {
  it('varsayılan EDİTÖR — operatör talebi', () => {
    expect(DEFAULT_METRICS_VIEW).toBe('editor');
    expect(metricsViewFromParam(null)).toBe('editor');
    expect(metricsViewFromParam(undefined)).toBe('editor');
    expect(metricsViewFromParam('')).toBe('editor');
  });
  it('açık değerler', () => {
    expect(metricsViewFromParam('1')).toBe('editor');
    expect(metricsViewFromParam('0')).toBe('catalogue');
  });
  it('bozuk değer varsayılana düşer', () => {
    // URL'i elle düzenleyen operatör boş ekranla karşılaşmamalı.
    expect(metricsViewFromParam('yes')).toBe('editor');
    expect(metricsViewFromParam('2')).toBe('editor');
  });
});

describe('gidiş-dönüş', () => {
  it('yazılan her görünüm aynen geri okunur', () => {
    for (const v of ['editor', 'catalogue'] as const) {
      expect(metricsViewFromParam(metricsViewUrlValue(v))).toBe(v);
    }
  });
  it('varsayılan URL’e yazılmaz', () => {
    expect(metricsViewUrlValue('editor')).toBeNull();
  });
  it('varsayılan DIŞI görünüm YAZILIR', () => {
    // ASIL REGRESYON: varsayılan editör iken katalog seçimi URL'e
    // düşmeli, yoksa geri okuma yine editör verir ve seçim yutulur.
    expect(metricsViewUrlValue('catalogue')).toBe('0');
  });
});

describe('shouldRedirectLegacyMetric', () => {
  it('eski derin link YÖNLENDİRİLİR (varsayılan editör olsa bile)', () => {
    // EN KRİTİK VAKA. Eski koşul `!editor && legacyMetric` idi;
    // varsayılan editöre çevrilince hep false olur ve
    // /metrics?metric=foo sessizce boş bir sorgu editörüne düşerdi.
    expect(shouldRedirectLegacyMetric('http.server.duration', null)).toBe(true);
    expect(shouldRedirectLegacyMetric('http.server.duration', '0')).toBe(true);
  });
  it('kullanıcı AÇIKÇA editör istediyse yönlendirilmez', () => {
    expect(shouldRedirectLegacyMetric('http.server.duration', '1')).toBe(false);
  });
  it('eski param yoksa yönlendirme yok', () => {
    expect(shouldRedirectLegacyMetric('', null)).toBe(false);
  });
});
