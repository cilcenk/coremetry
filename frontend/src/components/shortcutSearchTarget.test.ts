// `/` kısayolunun hedefi — UX denetimi E5 / Ö31 (v0.9.951).
//
// ORİJİNAL BELİRTİ: /traces'te `/` basınca imleç servis picker'ına değil
// "Trace ID…" kutusuna düşüyordu. Operatör servis aramak isterken
// trace-id kutusunda buluyordu kendini — belgelenmiş davranışın
// regresyonu.
//
// KÖK NEDEN ÖZELLİKLE SİNSİ: mekanizma v0.5.454'te TAM BU VAKA için
// yazılmıştı ve GlobalShortcuts'ın yorumu bunu kelimesi kelimesine
// söylüyor ("the Service picker on /traces, not the trace-id lookup
// which is the first DOM input"). Ama `data-shortcut-search` işareti
// /traces'e HİÇ konmamıştı: kod doğru, kablo yok. Yorum bir sözleşme
// gibi okunuyordu, oysa yalnız bir NİYETTİ.

import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { stripTsComments } from '../styles/zLayers.test';

const SRC = join(__dirname, '..');
const read = (rel: string) => readFileSync(join(SRC, rel), 'utf8');

describe('GlobalShortcuts — çözümleme sırası', () => {
  const gs = read('components/GlobalShortcuts.tsx');

  it('AÇIK işaret fallback’ten ÖNCE denenir', () => {
    const opted = gs.indexOf("querySelectorAll<HTMLElement>('[data-shortcut-search]')");
    const fallback = gs.indexOf('input[type="text"]');
    expect(opted).toBeGreaterThan(-1);
    expect(fallback).toBeGreaterThan(-1);
    expect(opted, 'işaret fallback’ten sonra aranırsa opt-in hiçbir işe yaramaz').toBeLessThan(fallback);
  });

  it('fallback checkbox/radio’ya DÜŞMEZ', () => {
    // `input:not([type])` + text/search — bir onay kutusu odaklanırsa
    // `/` görünürde hiçbir şey yapmamış gibi olurdu.
    expect(gs).toMatch(/input\[type="text"\], input\[type="search"\], input:not\(\[type\]\), textarea/);
  });
});

describe('/traces — işaret KONULDU (Ö31 kapandı)', () => {
  const traces = read('pages/Traces.tsx');

  it('servis picker `/` hedefi olarak işaretli', () => {
    expect(traces).toMatch(/<ServicePicker[\s\S]{0,400}?shortcutSearch\s*\/>/);
  });

  it('Trace ID kutusu DOM’da hâlâ ÖNCE — yani işaret gerçekten gerekli', () => {
    // Bu satır düzeltmenin gerekçesini canlı tutuyor: biri kutuları
    // yeniden sıralarsa ve işaret düşerse, test artık koruma sağlamaz
    // sanılmasın diye premise burada ölçülüyor.
    const traceIdAt = traces.indexOf('placeholder="Trace ID…"');
    const pickerAt = traces.indexOf('<ServicePicker');
    expect(traceIdAt).toBeGreaterThan(-1);
    expect(pickerAt).toBeGreaterThan(traceIdAt);
  });
});

describe('ServicePicker — opt-in kablosu', () => {
  const sp = read('components/ServicePicker.tsx');

  it('bayrak input’a data-shortcut-search basar', () => {
    expect(sp).toMatch(/shortcutSearch \? \{ 'data-shortcut-search': '' \}/);
  });

  it('bayraksız picker işaret TAŞIMAZ — sayfa başına tek hedef', () => {
    // Koşulsuz basmak, bir sayfadaki HER picker'ı hedef yapardı ve
    // querySelectorAll ilkini seçerdi: yine DOM sırası kumarı.
    expect(sp).not.toMatch(/data-shortcut-search=""/);
    expect(sp).toMatch(/\.\.\.\(shortcutSearch \?/);
  });
});

// ─── v0.9.1003 · C3 — `/` sahipliği tek olmalı ────────────────────────
//
// ORİJİNAL BELİRTİ (etkileşim denetimi C3): yukarıdaki düzeltme
// gemideyken bile /traces'te `/` YANLIŞ kutuya iniyordu — bu kez
// "Min ms" SAYI kutusuna. Yukarıdaki üç describe hepsi YEŞİLDİ:
// işaret konmuştu, ServicePicker onu basıyordu, çözümleme sırası
// doğruydu. Kapı, kusuru yapısal olarak göremiyordu çünkü yalnız
// KAYNAK METNİ kontrol ediyordu, ÇAKIŞMAYI değil.
//
// KÖK NEDEN: `useDataTable`, `onOpen` + `searchRef` birlikte
// verildiğinde İKİNCİ bir `/` bindingi kaydediyor
// (DataTable.tsx:213). İki kaydın da `scope`u yok, dolayısıyla
// `navScope.pickOwner` "yığının tepesi = en son kaydolan" diyor:
// GlobalShortcuts AppShell'de sayfadan ÖNCE mount olduğu için sayfa
// mount'unda gelen DataTable kaydı DAİMA kazanıyor. Yani bir sayfa
// hem açık işaret hem `searchRef` taşıyorsa, açık işaret ÖLÜ demektir.
//
// Bu yüzden kural bir SAYFA içi çakışma kuralı: aynı dosyada hem
// `/` işareti (`data-shortcut-search` / `shortcutSearch`) hem de
// `useDataTable`a giden bir `searchRef` bulunamaz.
describe('C3 — bir dosyada `/` için TEK sahip', () => {
  // İki geçerli yazılış da sayılır: `searchRef: ref` (Traces'in eski
  // hâli) VE kısayol `searchRef,` (Endpoints/Inbox/Dashboards/
  // OperationsTable bugün böyle yazıyor). Yalnız ilkini arasaydım
  // kapı, ikinci yazılışa geçen her dosyayı sessizce ölçmeyi bırakırdı.
  const MARK = /data-shortcut-search|(?<![\w.])shortcutSearch(?![\w])/;
  const REF = /(?<![\w.])searchRef\s*[,:]/;

  function walk(dir: string, out: string[] = []): string[] {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      if (e.name === 'node_modules' || e.name === 'dist') continue;
      const p = join(dir, e.name);
      if (e.isDirectory()) walk(p, out);
      else if (/\.tsx?$/.test(e.name) && !/\.test\.tsx?$/.test(e.name)) out.push(p);
    }
    return out;
  }

  // Yorumları SOYARAK tara: bu dosyanın kendi düzyazısı, PageControls'un
  // açıklaması ve Traces'in "bu satır geri gelirse" uyarısı hepsi iki
  // dizeyi de anıyor. Soymadan yazılmış bir çakışma taraması, kuralı
  // AÇIKLAYAN her yorumu ihlal sanardı.
  const files = walk(SRC).map(p => ({ p, src: stripTsComments(readFileSync(p, 'utf8')) }));
  const marked = files.filter(f => MARK.test(f.src)).map(f => f.p);
  const refs = files.filter(f => REF.test(f.src)).map(f => f.p);

  it('tarama gerçekten bir şey buluyor', () => {
    // Sağlık assert'i: iki sinyalden biri sıfıra düşerse kapı kör
    // koşuyor demektir. Eşikler bugünkü sayıların (8 / 5) altında
    // tutuldu ki normal dalgalanma testi kırmızıya çevirmesin.
    expect(marked.length).toBeGreaterThanOrEqual(6);
    expect(refs.length).toBeGreaterThanOrEqual(4);
  });

  it('hiçbir dosya hem açık işaret hem searchRef taşımıyor', () => {
    const both = files.filter(f => MARK.test(f.src) && REF.test(f.src)).map(f => f.p);
    expect(both, 'searchRef ikinci bir `/` kaydı doğurur ve açık işareti ölü bırakır').toEqual([]);
  });
});
