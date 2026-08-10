// rebuildPreserving.test.ts — v0.9.940 (UX denetimi A7).
//
// Traces ve Explore sorgu dizesini SIFIRDAN kuruyor. Listede olmayan her
// parametre bir render sonra siliniyordu — kim yazmış olursa olsun. Sınıf
// üç kez doğdu:
//   • v0.8.383 (K4) Topbar'ın `?env=`i,
//   • v0.9.878 (K9) DataTable primitifinin `?s_traces-agg`i (paylaşılan
//     sıralama linki sessizce kayboluyor, alıcı başlıkta p99 görüp
//     sunucuda count sıralaması alıyordu),
//   • ve sıradaki aday `?ai=` (AI çekmecesi bir filtre düzenlemesinde
//     kapanırdı).
// Her seferinde çözüm "listeye bir tane daha ekle" oldu; kusur listede
// değil, listenin TEK OTORİTE sayılmasındaydı.
import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { buildQuery, rebuildPreserving } from './urlState';

describe('rebuildPreserving (v0.9.940)', () => {
  it('YABANCI parametre korunur — A7\'nin tamamı bu', () => {
    const out = rebuildPreserving('?ai=exception%3A42&range=30m', [
      ['range', '6h'], ['view', 'list'],
    ]);
    const p = new URLSearchParams(out);
    expect(p.get('ai')).toBe('exception:42');
    expect(p.get('range')).toBe('6h');   // sahip olunan güncellendi
    expect(p.get('view')).toBe('list');
  });

  it('SAHİP OLUNAN parametre boş değerle SİLİNİR', () => {
    // buildQuery semantiği aynen: sahip olduğun bir parametreyi
    // temizleyebilmek şart, yoksa "aggregate'ten listeye dön" gibi bir
    // geçiş eski parametreyi URL'de bırakırdı.
    const out = rebuildPreserving('?view=aggregate&groupBy=attr&ai=x', [
      ['view', ''], ['groupBy', ''],
    ]);
    const p = new URLSearchParams(out);
    expect(p.has('view')).toBe(false);
    expect(p.has('groupBy')).toBe(false);
    expect(p.get('ai')).toBe('x');       // yabancı el değmeden durur
  });

  it('yabancı param yokken çıktı buildQuery ile BAYT BAYT aynı', () => {
    // Mevcut linkler değişmemeli: A7 bir davranış değişikliği değil,
    // bir koruma eklemesi.
    const entries: Array<[string, string]> = [
      ['range', '30m'], ['view', 'aggregate'], ['sort', 'duration'],
    ];
    expect(rebuildPreserving('', entries)).toBe(buildQuery(entries));
    expect(rebuildPreserving('?range=1h', entries)).toBe(buildQuery(entries));
  });

  it('İDEMPOTENT — ikinci çağrı aynı dizeyi üretir', () => {
    // Zorunlu: iki çağıran da sonucu window.location.search ile
    // KARŞILAŞTIRIP öyle yazıyor. Sıra kararlı olmasaydı her render bir
    // yazım daha tetikler, iki yazıcı birbirini yeniden sıralardı.
    const entries: Array<[string, string]> = [['range', '6h'], ['view', 'list']];
    const once = rebuildPreserving('?ai=x&env=prod', entries);
    expect(rebuildPreserving('?' + once, entries)).toBe(once);
  });

  it('sahip olunanlar ÖNCE, yabancılar prev sırasıyla sonra', () => {
    const out = rebuildPreserving('?z=1&a=2', [['range', '6h']]);
    expect(out).toBe('range=6h&z=1&a=2');
  });

  it('tekrarlanan yabancı parametreler kaybolmaz', () => {
    const out = rebuildPreserving('?tag=a&tag=b', [['range', '6h']]);
    expect(new URLSearchParams(out).getAll('tag')).toEqual(['a', 'b']);
  });

  it('boş prev ve boş entries çökmez', () => {
    expect(rebuildPreserving('', [])).toBe('');
    expect(rebuildPreserving('?ai=x', [])).toBe('ai=x');
  });
});

// Kaynak pini: iki sayfa da GERÇEKTEN bu fonksiyonu kullanmalı. Saf
// fonksiyonun testli olması, çağıranın buildQuery'ye geri dönmesini
// engellemez — sınıfın üç kez doğmasının sebebi tam olarak buydu.
describe('çağıranlar (v0.9.940)', () => {
  const code = (p: string) =>
    readFileSync(resolve(__dirname, p), 'utf8')
      .replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');

  it('Traces URL yazımı yabancı paramları koruyor', () => {
    expect(code('../pages/Traces.tsx')).toContain('rebuildPreserving(window.location.search, [');
  });

  it('Explore URL yazımı yabancı paramları koruyor', () => {
    const src = code('../pages/Explore.tsx');
    expect(src).toContain('rebuildPreserving(window.location.search,');
    // `meaningful` hesabı SAHİP OLUNAN dizeden çıkmalı: yabancı bir param
    // boş bir sorguyu "anlamlı" gösterip paramsız giriş ekranını
    // (soru kartları) yok ederdi.
    expect(src).toContain('const queryQs = buildQuery(queryEntries);');
  });
});
