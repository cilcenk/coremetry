// @vitest-environment jsdom
//
// v0.9.1021 — Combobox × escLayer katman sözleşmesi (Ö28 sınıfı).
//
// Kural: bir Esc BİR katman kapatır. Liste AÇIKKEN Esc tüketilir
// (defaultPrevented=true → keyboard.ts'in escLayer'ı üst katmanı
// KAPATMAZ); liste KAPALIYKEN tüketilmez (olay katmana akar, Drawer/
// Modal normal kapanır). Düzeltme öncesi açık Combobox'lı bir
// Drawer'da tek Esc ikisini birden kapatıyordu (FilterBuilder canlı
// örnekti) — bu test o gerilemeyi çivileriyor. Neden gerçek mount:
// `open` bir çalışma zamanı dalı; kaynak taramasıyla ölçülemez.
//
// v0.9.1022 — ÇIKIŞ SÖZLEŞMESİ eklendi (autoFocus / disabled /
// onBlurCommit / onEscape). Aynı dosyada duruyorlar çünkü hepsi TEK
// soruyu cevaplıyor: "alandan nasıl çıkılır ve çıkarken ne olur".
// Esc'in iki anlamı (listeyi kapat / düzenlemeyi iptal et) ile
// blur'un commit'i birbirine BAĞLI — ayrı dosyalara bölmek, aradaki
// tuzağı (iptalden sonra gelen blur) görünmez kılardı.
//
// MUTASYONLA DOĞRULANDI (14 mutasyon). İkisi SAĞ KALDI ve ikisi de
// kapı boşluğu DEĞİL, ULAŞILAMAZ kod: (1) `onClick`teki `disabled`
// koruması — React kilitli form kontrollerinde fare olaylarını zaten
// düşürüyor (korumayı sildim, `open` yine sızmadı); (2) `autoFocus`
// efektindeki `disabled` koruması — DOM kilitli bir alana odak
// vermiyor. İkisi de NİYET olarak duruyor; onları "ölçen" bir test
// yazmak, hiçbir zaman kırmızıya dönemeyecek bir test yazmak olurdu.
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act, useState } from 'react';
import { Combobox } from './Combobox';

let host: HTMLDivElement;
let root: Root;

// jsdom `scrollIntoView` uygulamıyor; Combobox vurgulanan satırı
// görünür kılmak için onu çağırıyor (tarayıcıda var, jsdom'da yok).
// Ok tuşu vakalarının hepsi bu shim olmadan TypeError ile düşer.
beforeEach(() => {
  if (!Element.prototype.scrollIntoView) {
    Element.prototype.scrollIntoView = () => {};
  }
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

function mountBox() {
  act(() => {
    root.render(
      <Combobox value="" onChange={() => {}} options={['alpha', 'beta']}
        placeholder="pick…" />,
    );
  });
  return host.querySelector('input') as HTMLInputElement;
}

// jsdom'da gerçek klavye olayı: dispatchEvent dönüşü false ise
// preventDefault çağrılmıştır (= olay tüketildi).
function pressEsc(el: HTMLElement): { consumed: boolean } {
  let consumed = false;
  act(() => {
    const ev = new KeyboardEvent('keydown', {
      key: 'Escape', bubbles: true, cancelable: true,
    });
    consumed = !el.dispatchEvent(ev);
  });
  return { consumed };
}

// Kontrollü koşum: gerçek çağıranlar gibi `value`yu geri yazan bir
// kap. onBlurCommit'in "O ANKİ yazılı değer" sözleşmesi ancak değer
// gerçekten güncelleniyorsa ölçülebilir.
function Harness(props: Partial<React.ComponentProps<typeof Combobox>>) {
  const [v, setV] = useState(props.value ?? '');
  return (
    <Combobox options={['alpha', 'beta']} placeholder="pick…"
      {...props} value={v} onChange={x => setV(x)} />
  );
}

function mountHarness(props: Partial<React.ComponentProps<typeof Combobox>> = {}) {
  act(() => { root.render(<Harness {...props} />); });
  return host.querySelector('input') as HTMLInputElement;
}

// jsdom'da .blur() hem `blur` hem bubble eden `focusout` üretir;
// React'in onBlur'u ikincisine bağlıdır.
function blurAway(el: HTMLElement) {
  act(() => { el.dispatchEvent(new FocusEvent('focusout', { bubbles: true })); });
}

// React 17+ onFocus'u BUBBLE EDEN `focusin` üzerinden dinler. İlk
// yazılışta `focus` (bubbles:false) gönderiyordum: olay React'e hiç
// ulaşmıyordu, yani `disabled` testleri hiçbir şey ÖLÇMÜYORDU —
// mutasyon (odak korumasını sil) yeşil geçti ve bunu ortaya çıkardı.
function focusIn(el: HTMLElement) {
  act(() => { el.dispatchEvent(new FocusEvent('focusin', { bubbles: true })); });
}

function typeToOpen(input: HTMLInputElement, text: string) {
  act(() => {
    input.focus();
    const setter = Object.getOwnPropertyDescriptor(
      window.HTMLInputElement.prototype, 'value')!.set!;
    setter.call(input, text);
    input.dispatchEvent(new Event('input', { bubbles: true }));
  });
}

describe('Combobox Esc katman sözleşmesi', () => {
  it('liste AÇIKKEN Esc tüketilir ve yalnız listeyi kapatır', () => {
    const input = mountBox();
    typeToOpen(input, 'al');
    expect(host.textContent).toContain('alpha'); // liste açık
    const { consumed } = pressEsc(input);
    expect(consumed).toBe(true);                  // defaultPrevented
    expect(host.textContent).not.toContain('alpha'); // liste kapandı
  });

  it('liste KAPALIYKEN Esc tüketilmez — üst katmana akar', () => {
    const input = mountBox();
    const { consumed } = pressEsc(input);
    expect(consumed).toBe(false);
  });
});

// ─── v0.9.1022 · çıkış sözleşmesi ─────────────────────────────────
describe('Combobox — onEscape (düzenlemeden iptal-çıkış)', () => {
  it('liste AÇIKKEN onEscape ÇAĞRILMAZ — ilk Esc yalnız listeyi kapatır', () => {
    // Sözleşmenin can alıcı yarısı: iki katman (liste + düzenleyici)
    // varsa her Esc BİRİNİ kapatır. Bu assert olmasaydı "Esc'te iptal
    // et" en kısa yazılışıyla (koşulsuz onEscape) geçerdi ve operatör
    // açık listeli bir hücrede Esc'e basınca düzenlemeyi kaybederdi.
    let escapes = 0;
    const input = mountHarness({ onEscape: () => { escapes++; } });
    typeToOpen(input, 'al');
    expect(host.textContent).toContain('alpha');
    const { consumed } = pressEsc(input);
    expect(consumed).toBe(true);
    expect(escapes, 'liste açıkken onEscape çağrıldı').toBe(0);
    expect(host.textContent).not.toContain('alpha');
  });

  it('liste KAPALIYKEN onEscape çağrılır VE olay tüketilir', () => {
    // Tüketmek şart: düzenleyici de bir katman. Tüketmezsek onu saran
    // Drawer aynı Esc ile kapanır — v0.9.1021'in düzelttiği hatanın
    // bir katman aşağıdaki kopyası.
    let escapes = 0;
    const input = mountHarness({ onEscape: () => { escapes++; } });
    const { consumed } = pressEsc(input);
    expect(escapes).toBe(1);
    expect(consumed, 'düzenleyici katmanı Esc’i tüketmedi').toBe(true);
  });

  it('onEscape VERİLMEYEN çağıranda kapalı-liste davranışı birebir eski', () => {
    const input = mountHarness({});
    expect(pressEsc(input).consumed).toBe(false);
  });
});

describe('Combobox — onBlurCommit', () => {
  it('odaktan çıkışta O ANKİ yazılı değeri verir', () => {
    const seen: string[] = [];
    const input = mountHarness({ onBlurCommit: v => seen.push(v) });
    typeToOpen(input, 'beta-x');
    expect(host.querySelector('.cb-list'), 'yazınca liste açılmadı').toBeTruthy();
    blurAway(input);
    expect(seen).toEqual(['beta-x']);
    expect(host.querySelector('.cb-list'), 'blur listeyi kapatmadı').toBeNull();
  });

  it('Esc iptalinden SONRAKİ blur commit ETMEZ', () => {
    // Tuzak: iptal çoğu çağıranda alanı söker/odağı taşır → Esc'in
    // hemen ardından blur gelir. Bastırmasaydık "iptal" sessizce
    // KAYDET olurdu: operatörün Esc'i tam tersini yapardı.
    const seen: string[] = [];
    const input = mountHarness({
      onBlurCommit: v => seen.push(v),
      onEscape: () => {},
    });
    typeToOpen(input, 'beta-x');
    pressEsc(input); // liste açıktı → yalnız liste kapandı
    pressEsc(input); // liste kapalı → iptal
    blurAway(input);
    expect(seen, 'iptalden sonra gelen blur commit etti').toEqual([]);
  });

  it('iptalden sonra yazmaya devam eden oturum YENİDEN commit eder', () => {
    // Bastırma bayrağı tek seferlik olmalı. Kalıcı olsaydı, alanı
    // ayakta bırakan bir çağıranda (sökmeyen) tek bir Esc o alanı
    // ömür boyu "kaydetmeyen" bir kutuya çevirirdi.
    const seen: string[] = [];
    const input = mountHarness({
      onBlurCommit: v => seen.push(v),
      onEscape: () => {},
    });
    pressEsc(input);            // iptal (liste kapalı)
    typeToOpen(input, 'gamma'); // operatör yazmaya devam etti
    blurAway(input);
    expect(seen).toEqual(['gamma']);
  });

  it('iptali YAZMADAN da bir tuş geçersiz kılar (ok tuşu)', () => {
    // Mutasyonla bulundu: bayrağı SIFIRLAYAN iki yer var (onChange ve
    // onKeyDown). Yukarıdaki test yazma yoluyla geçtiği için keydown
    // sıfırlaması silinse bile YEŞİL kalıyordu. Bu vaka onu tek başına
    // ölçüyor: Esc'ten sonra yalnız ok tuşuna basıp alanı bırakan
    // operatör (değeri değiştirmeden) yine commit görmeli.
    const seen: string[] = [];
    const input = mountHarness({
      value: 'alpha', onBlurCommit: v => seen.push(v), onEscape: () => {},
    });
    pressEsc(input); // iptal
    act(() => {
      input.dispatchEvent(new KeyboardEvent('keydown', {
        key: 'ArrowDown', bubbles: true, cancelable: true,
      }));
    });
    blurAway(input);
    expect(seen).toEqual(['alpha']);
  });
});

describe('Combobox — disabled / autoFocus', () => {
  it('disabled: input kilitli ve odak listeyi AÇMAZ', () => {
    const input = mountHarness({ value: 'alpha', disabled: true });
    expect(input.disabled).toBe(true);
    focusIn(input);
    act(() => { input.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
    expect(host.querySelector('.cb-list'), 'kilitli alan liste açtı').toBeNull();
  });

  it('disabled iken gelen odak `open` durumunu SIZDIRMAZ', () => {
    // Mutasyonla bulundu: liste render'ı da `!disabled` ile korunuyor,
    // dolayısıyla yalnız "liste görünüyor mu" diye bakan bir assert
    // odak korumasının silinmesini göremiyordu. Sızıntı gerçek bir
    // vaka: TeamEditor kayıt sırasında alanı kilitler; kayıt hata
    // verip alan yeniden AÇILDIĞINDA, sızmış bir `open` listeyi
    // kendiliğinden patlatırdı.
    const input = mountHarness({ value: 'alpha', disabled: true });
    focusIn(input);
    act(() => { input.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
    act(() => { root.render(<Harness value="alpha" disabled={false} />); });
    expect(host.querySelector('.cb-list'), 'kilit kalkınca liste kendiliğinden açıldı').toBeNull();
  });

  it('AÇIK listeyle kilitlenen alan listeyi bırakır', () => {
    // Gerçek vaka (TeamEditor): operatör Enter'a basar, kayıt başlar,
    // alan busy=true ile kilitlenir — ama alan hâlâ montedir ve liste
    // AÇIKTIR. Render korumasi olmasaydı açılır liste kayıt boyunca
    // satırın üstünde asılı kalırdı.
    const input = mountHarness({ onBlurCommit: () => {} });
    typeToOpen(input, 'al');
    expect(host.querySelector('.cb-list'), 'liste açılmadı — öncül bayat').toBeTruthy();
    act(() => { root.render(<Harness value="al" disabled />); });
    expect(host.querySelector('.cb-list'), 'kilitlenen alan listeyi açık bıraktı').toBeNull();
  });

  it('disabled: ✕ (temizle) çalışır KALIR', () => {
    // Bayat/sticky bir değeri atıl bir alandan bırakabilmek gerekiyor
    // (EnvPicker'ın "env uygulanmıyor" hâli tam olarak bu).
    const input = mountHarness({ value: 'alpha', disabled: true });
    const clear = host.querySelector('.cb-clear') as HTMLButtonElement;
    expect(clear, 'kilitli alanda ✕ kayboldu').toBeTruthy();
    act(() => { clear.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
    expect(input.value).toBe('');
  });

  it('autoFocus: mount’ta odak input’ta ve metin SEÇİLİ', () => {
    const input = mountHarness({ value: 'alpha', autoFocus: true });
    expect(document.activeElement).toBe(input);
    expect(input.selectionStart).toBe(0);
    expect(input.selectionEnd).toBe('alpha'.length);
  });
});
