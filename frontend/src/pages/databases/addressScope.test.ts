import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { addressScopeNotice } from './addressScope';

// v0.10.19 — F0.8'in ölçümlü yarısı. /database'in Scope satırı
// `system / instance` yazıyor ve operatör bunu bir MAKİNE sanıyor.
// `instance` aslında peer_service: MV aynı peer.service'i paylaşan farklı
// server.address değerlerini tek satıra çöküyor.
//
// ÖLÇÜLDÜ (lokal CH): oracle/oracle/COREBANK → 2 adres, CARDS → 2,
// diğer 13 kimlik → 1.
// ⚠ Bu "2" bir DEMO SABİTİ (cmd/demo/main.go iki adresi elle yazıyor).
// Fixture olan SAYI; çökmeyi yapan MV anahtarı fixture değil.

describe('addressScopeNotice', () => {
  it('ÇOKLUK — toplam olduğunu ve nedenini söyler', () => {
    const n = addressScopeNotice(
      { probed: true, addrs: ['corebank-scan.prod:1521', 'corebank-dg.prod:1521'] },
      'oracle',
    );
    expect(n).not.toBeNull();
    expect(n!.multiple).toBe(true);
    expect(n!.label).toBe('2 fiziksel adres');
    // Kritik: TOPLAM olduğunu söylemeli, yoksa beyan bilgi vermez.
    expect(n!.detail).toContain('TOPLAM');
    // Ve nedenini: instance bir makine değil, bir etiket.
    expect(n!.detail).toContain('peer.service');
    expect(n!.detail).toContain('corebank-dg.prod:1521');
  });

  it('TEKİLLİK — ölçüldüyse o da bilgidir', () => {
    const n = addressScopeNotice({ probed: true, addrs: ['pg-1:5432'] }, 'postgres');
    expect(n!.multiple).toBe(false);
    expect(n!.label).toBe('1 fiziksel adres');
    expect(n!.detail).toContain('pg-1:5432');
  });

  it('kırpılmış liste "+" ile ilan edilir', () => {
    const n = addressScopeNotice(
      { probed: true, capped: true, addrs: ['a', 'b', 'c', 'd', 'e', 'f'] },
      'oracle',
    );
    expect(n!.label).toBe('6+ fiziksel adres');
    expect(n!.detail).toContain('kırpıldı');
  });

  // ⚠ EN ÖNEMLİ DAL — SESSİZLİK SÖZLEŞMESİ.
  //
  // Ölçüm yapılmadıysa hiçbir şey ilan edilmemeli. Buradaki tuzak,
  // ölçülmemiş/boş sonucu "1 fiziksel adres" diye okumak: o an
  // TEKİLLİĞİ YANLIŞ YERE iddia etmiş oluruz ve bu, susmaktan kötüdür —
  // operatör ölçülmemiş bir şeyi ölçülmüş sanar.
  describe('ölçüm yoksa SUSAR', () => {
    const silent: Array<[string, Parameters<typeof addressScopeNotice>[0]]> = [
      ['undefined', undefined],
      ['prob koşmadı', { probed: false }],
      ['prob koşmadı ama adres alanı dolu', { probed: false, addrs: ['a', 'b'] }],
      ['prob koştu, adres yok (eski SDK)', { probed: true, addrs: [] }],
      ['prob koştu, alan hiç yok', { probed: true }],
    ];
    for (const [name, p] of silent) {
      it(name, () => expect(addressScopeNotice(p, 'oracle')).toBeNull());
    }
  });
});

// KABLOLAMA PİNİ — saf çekirdek yeşil ama çağrılmıyorsa kusur yerinde
// kalır (v0.9.1334, v0.10.11 sınıfı).
describe('detailSections/DatabaseDetail kablolaması', () => {
  const sections = readFileSync(new URL('./detailSections.tsx', import.meta.url), 'utf8');
  const page = readFileSync(new URL('../DatabaseDetail.tsx', import.meta.url), 'utf8');

  it('Scope satırı beyanı GERÇEKTEN hesaplıyor', () => {
    expect(sections).toContain('addressScopeNotice(physicalAddrs, refObj.instance)');
    expect(sections).toContain('{addrNotice && (');
  });

  it('sayfa ölçümü header\'a GEÇİRİYOR', () => {
    // Prop tanımlı ama geçirilmiyorsa beyan hiç çıkmaz — sessiz ölüm.
    expect(page).toContain('physicalAddrs={d?.physicalAddrs}');
  });

  it('çokluk vurgulanıyor, tekillik vurgulanmıyor', () => {
    // Uyarı tonu yalnız gerçekten toplam olduğunda; her zaman sarı bir
    // rozet, hiçbir zaman okunmayan bir rozettir.
    expect(sections).toContain("addrNotice.multiple ? 'badge b-warn' : 'badge b-gray'");
  });
});
