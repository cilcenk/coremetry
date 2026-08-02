// v0.9.558 regresyon testi — hop derinliğinin URL gidiş-dönüşü.
//
// Korunan sözleşme: yazılan her geçerli derinlik, geri okunduğunda
// AYNI değeri vermeli. Bu tek cümle, bu repoda üç kez tekrarlamış
// tek-yön-okuma hata sınıfının panzehiri (v0.8.256 problems,
// v0.8.265 service-map, v0.8.267 anomalies).
//
// Somut tuzak: eski kod `if (h > 1) set else delete` yazıyordu, yani
// "1 varsayılandır" bilgisi koda gömülüydü. Varsayılan 2'ye çıkınca o
// satır kullanıcının 1 hop seçimini URL'den siler ve okuma yine 2
// döndürürdü — seçim hiçbir hata vermeden yutulurdu.
import { describe, it, expect } from 'vitest';
import {
  DEFAULT_TOPOLOGY_HOPS,
  MIN_TOPOLOGY_HOPS,
  MAX_TOPOLOGY_HOPS,
  parseTopologyHops,
  topologyHopsUrlValue,
} from './topologyHops';

describe('parseTopologyHops', () => {
  it('parametre yoksa varsayılan', () => {
    expect(parseTopologyHops(null)).toBe(DEFAULT_TOPOLOGY_HOPS);
    expect(parseTopologyHops(undefined)).toBe(DEFAULT_TOPOLOGY_HOPS);
    expect(parseTopologyHops('')).toBe(DEFAULT_TOPOLOGY_HOPS);
  });

  it('varsayılan 2 — operatör talebi', () => {
    expect(DEFAULT_TOPOLOGY_HOPS).toBe(2);
  });

  it('geçerli değerler aynen geçer', () => {
    expect(parseTopologyHops('1')).toBe(1);
    expect(parseTopologyHops('2')).toBe(2);
    expect(parseTopologyHops('3')).toBe(3);
  });

  it('aralık dışı en yakın sınıra kırpılır', () => {
    expect(parseTopologyHops('9')).toBe(MAX_TOPOLOGY_HOPS);
    expect(parseTopologyHops('-4')).toBe(MIN_TOPOLOGY_HOPS);
  });

  it('bozuk girdi varsayılana düşer, patlamaz', () => {
    // URL'i elle düzenleyen operatör boş grafikle karşılaşmamalı.
    expect(parseTopologyHops('abc')).toBe(DEFAULT_TOPOLOGY_HOPS);
    expect(parseTopologyHops('0')).toBe(DEFAULT_TOPOLOGY_HOPS);
    expect(parseTopologyHops('NaN')).toBe(DEFAULT_TOPOLOGY_HOPS);
  });
});

describe('topologyHopsUrlValue', () => {
  it('varsayılan URL’e yazılmaz', () => {
    expect(topologyHopsUrlValue(DEFAULT_TOPOLOGY_HOPS)).toBeNull();
  });

  it('varsayılan DIŞI değerler yazılır', () => {
    // ASIL REGRESYON: varsayılan 2 iken 1 seçimi YAZILMALI. Eski
    // `h > 1` mantığı burada null döndürür, seçim kaybolurdu.
    expect(topologyHopsUrlValue(1)).toBe('1');
    expect(topologyHopsUrlValue(3)).toBe('3');
  });
});

describe('gidiş-dönüş', () => {
  it('yazılan her geçerli derinlik aynen geri okunur', () => {
    for (let h = MIN_TOPOLOGY_HOPS; h <= MAX_TOPOLOGY_HOPS; h++) {
      const written = topologyHopsUrlValue(h);
      expect(parseTopologyHops(written)).toBe(h);
    }
  });
});
