// @vitest-environment jsdom
//
// v0.9.1127 (AI Faz 1.5) — tek-atış ✨ Explain'in AKAN render sözleşmesi.
//
// NEDEN GERÇEK MOUNT: dördü de çalışma zamanı dalı, kaynak taramasıyla
// ölçülemez —
//   (1) delta geldikçe metin BÜYÜR (biriktirme; tek parça basmak
//       akışın tüm değerini yok eder),
//   (2) ilk token'a kadar Spinner, token'lardan SONRA imleç — ikisi
//       birlikte "hem bekliyor hem yazıyor" diye okunur,
//   (3) 👍/👎 yalnız answer çerçevesinden sonra çıkar (kimlik oradan
//       gelir; erken çizilen düğme oy kaydedemez — v0.9.592'nin dersi),
//   (4) "Yeniden sor" uçuştaki akışı KESER, yoksa eski delta'lar yeni
//       cevabın üstüne yazar ve panelde iki cevap iç içe geçer.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { CopilotExplain } from './CopilotExplain';
import { __resetCopilotEnabledCache } from './ai/useCopilotEnabled';
import { api } from '@/lib/api';
import type { ExplainStreamOpts } from '@/lib/api';
import { getRaw, setRaw, STORAGE_KEYS } from '@/lib/storage';

let host: HTMLDivElement;
let root: Root;

async function mount(node: React.ReactNode) {
  await act(async () => { root.render(node); });
}

const panelText = () => host.textContent ?? '';
const cursor = () => host.querySelector('.cm-ai-cursor');
const feedbackButtons = () => Array.from(host.querySelectorAll('button[aria-pressed]'));
const rerunButton = () =>
  Array.from(host.querySelectorAll('button')).find(b => b.textContent?.includes('Yeniden sor'));

/**
 * fakeExplain — akışı testin sürdüğü sahte uç.
 *
 * Çağrıları AYRI AYRI tutuyor (tek bir "son çağrı" değil): "Yeniden sor"
 * ikinci bir çağrı başlatıyor ve testin sorusu tam olarak BİRİNCİSİNE ne
 * olduğu. Tek slot tutan bir sahte, iptal edilmemiş yeni signal'i ölçüp
 * yeşil dönerdi.
 */
interface FakeCall {
  emit: (t: string) => Promise<void>;
  finish: (explanation: string, exchangeId?: string) => Promise<void>;
  fail: (e: unknown) => Promise<void>;
  aborted: () => boolean;
}

function fakeExplain() {
  const seen: FakeCall[] = [];
  vi.spyOn(api, 'copilotExplainProblem').mockImplementation(
    (_id: string, opts?: ExplainStreamOpts) => {
      let resolve!: (v: { explanation: string; exchangeId?: string }) => void;
      let reject!: (e: unknown) => void;
      const p = new Promise<{ explanation: string; exchangeId?: string }>((res, rej) => {
        resolve = res; reject = rej;
      });
      seen.push({
        emit: async t => { await act(async () => { opts?.onDelta?.(t); }); },
        finish: async (explanation, exchangeId = 'x1') => {
          await act(async () => { resolve({ explanation, exchangeId }); });
        },
        fail: async e => {
          reject(e);
          await act(async () => { await p.catch(() => {}); });
        },
        aborted: () => opts?.signal?.aborted ?? false,
      });
      return p;
    });
  return {
    call: (i = 0) => seen[i],
    count: () => seen.length,
    emit: (t: string) => seen[seen.length - 1].emit(t),
    finish: (explanation: string, exchangeId = 'x1') =>
      seen[seen.length - 1].finish(explanation, exchangeId),
  };
}

beforeEach(() => {
  // v0.10.81 — kutu artık ?aicode='u GERÇEK URL'ye yazıyor; jsdom URL'si
  // testler arası sıfırlanmadığı için bir testin tıkı sonrakinin
  // tohumunu işaretli bulurdu. Her test temiz adresle başlar.
  window.history.replaceState({}, '', '/');
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  __resetCopilotEnabledCache();
  // v0.9.1238 — "Kodu da incele" tercihi artık localStorage'da yaşıyor;
  // testler arası sızması, bir sonraki testin ilk isteğini sessizce
  // kodlu yapardı. Bellek-içi taklit, çünkü Node 22'nin deneysel
  // yerleşik localStorage'ı jsdom'unkini gölgeliyor ve metotları
  // çalışmıyor (aynı tuzak CorePanel.smoke.test.tsx'te belgeli).
  const mem = new Map<string, string>();
  vi.stubGlobal('localStorage', {
    getItem: (k: string) => mem.get(k) ?? null,
    setItem: (k: string, v: string) => { mem.set(k, String(v)); },
    removeItem: (k: string) => { mem.delete(k); },
  });
  vi.spyOn(api, 'copilotConfig').mockResolvedValue({ enabled: true, model: 'gemma4' });
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('CopilotExplain — akan render', () => {
  it('delta geldikçe metin BÜYÜR', async () => {
    const f = fakeExplain();
    await mount(<CopilotExplain kind="problem" id="p1" auto />);

    // İlk token'dan önce: Spinner, metin yok.
    expect(panelText()).toContain('CoSRE düşünüyor');
    expect(cursor()).toBeNull();

    await f.emit('Kök ');
    expect(panelText()).toContain('Kök');
    expect(panelText()).not.toContain('CoSRE düşünüyor');

    await f.emit('neden: redis.');
    expect(panelText()).toContain('Kök neden: redis.');
  });

  it('akış sürerken imleç var, cevap bitince YOK', async () => {
    const f = fakeExplain();
    await mount(<CopilotExplain kind="problem" id="p1" auto />);

    await f.emit('yarım');
    expect(cursor()).not.toBeNull();

    await f.finish('Kök neden: redis.');
    expect(cursor()).toBeNull();
  });

  it('answer çerçevesi biriken metni EZER', async () => {
    const f = fakeExplain();
    await mount(<CopilotExplain kind="problem" id="p1" auto />);
    await f.emit('yarım cevap');
    await f.finish('tam ve nihai cevap');
    expect(panelText()).toContain('tam ve nihai cevap');
    expect(panelText()).not.toContain('yarım cevap');
  });

  it('👍/👎 yalnız cevap TAMAMLANINCA çıkar', async () => {
    const f = fakeExplain();
    await mount(<CopilotExplain kind="problem" id="p1" auto />);

    await f.emit('akıyor…');
    expect(feedbackButtons()).toHaveLength(0); // kimlik henüz yok

    await f.finish('bitti', 'xid-42');
    expect(feedbackButtons()).toHaveLength(2);
  });

  it('sıfır delta (buffered geri düşüşü) yine de cevabı çizer', async () => {
    const f = fakeExplain();
    await mount(<CopilotExplain kind="problem" id="p1" auto />);
    await f.finish('tek parça cevap');
    expect(panelText()).toContain('tek parça cevap');
    expect(cursor()).toBeNull();
  });
});

describe('CopilotExplain — Yeniden sor', () => {
  it('uçuştaki akışı İPTAL eder ve ekranı sıfırlar', async () => {
    const f = fakeExplain();
    await mount(<CopilotExplain kind="problem" id="p1" auto />);
    await f.emit('eski cevap');
    await f.finish('eski cevap tamam', 'xid-eski');
    expect(panelText()).toContain('eski cevap tamam');
    expect(feedbackButtons()).toHaveLength(2);

    const btn = rerunButton();
    expect(btn).toBeDefined();
    await act(async () => { btn!.click(); });

    expect(f.call(0).aborted()).toBe(true);    // BİRİNCİ akış kesildi
    expect(f.count()).toBe(2);                 // ve ikincisi başladı
    expect(panelText()).not.toContain('eski cevap tamam');
    expect(feedbackButtons()).toHaveLength(0); // eski oy yeni cevabın üstünde kalamaz
  });

  it('kesilen eski akışın delta\'ları yeni cevaba KARIŞMAZ', async () => {
    const f = fakeExplain();
    await mount(<CopilotExplain kind="problem" id="p1" auto />);
    await f.emit('eski');
    await f.finish('eski tamam');
    await act(async () => { rerunButton()!.click(); });

    // Yeni akış konuşuyor…
    await f.call(1).emit('yeni ');
    // …ve gecikmiş eski çağrı AbortError ile düşüyor. Panel bunu HATA
    // olarak göstermemeli: kullanıcının kendi eylemi kırmızı kutu değil.
    await f.call(0).fail(new DOMException('aborted', 'AbortError'));

    expect(panelText()).toContain('yeni');
    expect(panelText()).not.toContain('aborted');
    expect(panelText()).not.toContain('Explain failed');
    expect(cursor()).not.toBeNull(); // yeni akış hâlâ sürüyor
  });
});

// ── v0.9.1184 — "Kodu da incele" kutusunun GÖRÜNÜRLÜĞÜ ─────────────────
//
// Operatör: "Kodu incele checkboxı da çok küçük daha belirgin olabilir."
// 11px'lik çıplak bir etiketti; oysa bu bir KARAR — cevabın koda bakıp
// bakmayacağını belirliyor.
//
// Neden gerçek mount: iddia "sınıf adı doğru yazıldı" değil, İŞARETLİ
// DURUMUN GÖRÜNÜR OLMASI. Aktif tonu taşıyan sınıf, işaret değiştiğinde
// DOM'da da değişmeli — kutunun içine bakmadan durumu okuyabilmenin tek
// karşılığı bu. Sınıfı statik yazmak (hep `active`, ya da hiç) tsc'yi
// memnun eder ve ekranda durumu sessizce yalanlar.
describe('CopilotExplain — "Kodu da incele" kutusu (v0.9.1184)', () => {
  const chip = () => host.querySelector<HTMLButtonElement>('button.btn-chip');

  it('yalnız kod okunabilen türlerde çıkar', async () => {
    fakeExplain();
    await mount(<CopilotExplain kind="problem" id="p1" />);
    expect(chip()).toBeNull();
  });

  it('trace türünde deponun chip sözlüğüyle çizilir', async () => {
    fakeExplain();
    await mount(<CopilotExplain kind="trace" id="t1" />);
    const c = chip();
    expect(c).toBeTruthy();
    expect(c!.className).toContain('ch-sm');
    expect(c!.textContent).toContain('Kodu da incele');
    // Varsayılan KAPALI (v0.9.831 sözleşmesi) → aktif ton YOK.
    expect(c!.className).not.toContain('active');
  });

  it('işaretlenince aktif tonu alır, kaldırılınca bırakır', async () => {
    fakeExplain();
    await mount(<CopilotExplain kind="trace" id="t1" />);
    const box = chip()!;

    await act(async () => { box.click(); });
    expect(chip()!.className).toContain('active');

    await act(async () => { box.click(); });
    expect(chip()!.className).not.toContain('active');
  });

  // ⚠ v0.10.60 — SÖZLEŞME OPERATÖR KARARIYLA TERSİNE ÇEVRİLDİ.
  //
  // v0.9.1238 tercihi kalıcı yazıyordu (çekmece her özne için yeni mount
  // kurduğu için kutu sıfırlanıyor ve auto-koşu ilk turu kodsuz atıyordu).
  // Operatör: "Kodu incele default seçili olmasın."
  //
  // Gerekçe teknik değil, MALİYET: kod okumak bir depo listelemesi + dosya
  // çekmesi + ikinci bir yerel LLM turu demek. Hatırlama, o maliyeti
  // operatörün haberi olmadan HER Explain'e yayıyordu.
  //
  // Test silinmedi, TERSİNE çevrildi: kalıcılığın YOKLUĞU artık
  // sözleşmenin kendisi ve sessizce geri gelmesi bu testi kırar.
  it('tıklama tercihi KALICI YAZMAZ (v0.10.60 operatör kararı)', async () => {
    fakeExplain();
    await mount(<CopilotExplain kind="trace" id="t1" />);
    await act(async () => { chip()!.click(); });
    expect(getRaw(STORAGE_KEYS.aiIncludeCode)).toBeNull();
  });

  it('durum ÜÇ işaretle birden söylenir (ton · glif · aria-pressed)', async () => {
    // Üçü de aynı anda doğru olmalı: renk körü bir operatör glifi okur,
    // ekran okuyucu aria-pressed'ı, geri kalan herkes tonu. Biri sessizce
    // düşerse geriye kalanlar hatayı örter — bu yüzden tek testte.
    fakeExplain();
    await mount(<CopilotExplain kind="trace" id="t1" />);
    expect(chip()!.getAttribute('aria-pressed')).toBe('false');
    expect(chip()!.textContent).toContain('☐');

    await act(async () => { chip()!.click(); });
    expect(chip()!.getAttribute('aria-pressed')).toBe('true');
    expect(chip()!.textContent).toContain('☑');
    expect(chip()!.className).toContain('active');
  });
});

// ── v0.9.1238 — hatırlanan tercih + KISMİ sonucun görünürlüğü ──────────
//
// İki denetim bulgusu, tek yüzey:
//   (1) tercih her mount'ta unutuluyordu (AIDrawer özne başına `key` ile
//       yeniden mount ediyor) → auto koşusunun İLK turu her zaman
//       kodsuz; kutuyu yeniden işaretlemek İKİNCİ bir yerel LLM turu.
//   (2) kod GELDİĞİNDE de bir not olabilir (v0.9.1237: "3 pencereden
//       2'si kesildi", v0.9.1236: "depo adı sunucudan düzeltildi") ve
//       backend bunu `reason`da taşıyor.
//
// Neden gerçek mount: ikisi de ÇALIŞMA ZAMANI dalı. (1)'in iddiası
// "state tohumlandı" değil, İLK İSTEĞİN ARGÜMANI — tohumu bir useEffect
// ile düzeltmek tsc'yi memnun eder ve kodsuz istek çoktan yola çıkmış
// olurdu. (2)'nin iddiası "reason alanı var" değil, dosya listesi VARKEN
// de EKRANA çizilmesi; bu satır dal koşuluna asılı ve saf testle
// ölçülemez.
describe('CopilotExplain — hatırlanan "Kodu da incele" (v0.9.1238)', () => {
  const chip = () => host.querySelector<HTMLButtonElement>('button.btn-chip');

  /** Trace ucunun sahtesi — hangi çağrının hangi `includeCode` ile
   *  gittiğini AYRI AYRI tutar: sorunun tamamı İLK çağrının argümanı. */
  function fakeTrace(code?: import('@/lib/types').AICodeContext) {
    const flags: (boolean | undefined)[] = [];
    vi.spyOn(api, 'copilotExplainTrace').mockImplementation(
      async (_id: string, includeCode?: boolean, _opts?: ExplainStreamOpts) => {
        flags.push(includeCode);
        return { explanation: 'kök neden: bağlantı havuzu doldu', exchangeId: 'x1', code };
      });
    return flags;
  }

  const settle = async () => { await act(async () => { await Promise.resolve(); }); };

  // ⚠ v0.10.60 — TERSİNE ÇEVRİLDİ (yukarıdaki operatör kararı).
  // Kayıtlı bir tercih ARTIK OKUNMUYOR: kutu her açılışta kapalı başlar
  // ve auto-koşunun ilk turu kodsuz gider. Eski davranışın sessizce geri
  // gelmesi bu testi kırar.
  it('kayıtlı tercih OKUNMAZ — ilk tur yine kodsuz (v0.10.60)', async () => {
    setRaw(STORAGE_KEYS.aiIncludeCode, '1');
    const flags = fakeTrace();
    await mount(<CopilotExplain kind="trace" id="t1" auto />);
    await settle();

    expect(flags).toEqual([false]);       // tek tur, ve KODSUZ (açıkça false)
    expect(chip()!.textContent).toContain('☐');
  });

  it('tercih yokken ilk tur kodsuz KALIR (v0.9.831 varsayılanı)', async () => {
    const flags = fakeTrace();
    await mount(<CopilotExplain kind="trace" id="t1" auto />);
    await settle();
    expect(flags).toEqual([false]);
  });

  it('kod okunamayan tür kayıtlı tercihi görmezden gelir', async () => {
    // Tercih açıkken /problems açıklamasının "CoSRE kodu okuyor…"
    // demesi yalan olurdu: o uçta kod çekilmiyor.
    setRaw(STORAGE_KEYS.aiIncludeCode, '1');
    fakeExplain();
    await mount(<CopilotExplain kind="problem" id="p1" auto />);
    expect(chip()).toBeNull();
    expect(panelText()).toContain('CoSRE düşünüyor');
    expect(panelText()).not.toContain('kodu okuyor');
  });

  it('KISMİ sonuçta not, dosya listesiyle BİRLİKTE çizilir', async () => {
    setRaw(STORAGE_KEYS.aiIncludeCode, '1');
    fakeTrace({
      repo: 'CashManagement.CashFlow', branch: 'release', source: 'convention',
      files: [{ path: 'src/Batch.java', fromLine: 40, toLine: 100 }],
      reason: 'süre tavanı: 3 pencereden 2\'si kesildi',
    });
    await mount(<CopilotExplain kind="trace" id="t1" auto />);
    await settle();

    expect(panelText()).toContain('src/Batch.java');            // başarı dalı
    expect(panelText()).toContain('3 pencereden 2');            // …ve not YİNE DE görünür
    expect(panelText()).not.toContain('Kod okunamadı');         // sıfır-dosya dalı DEĞİL
  });

  it('hiç pencere gelmediğinde dürüst not aynen kalır', async () => {
    setRaw(STORAGE_KEYS.aiIncludeCode, '1');
    fakeTrace({ files: [], reason: 'bu kayıtta stacktrace yok' });
    await mount(<CopilotExplain kind="trace" id="t1" auto />);
    await settle();

    expect(panelText()).toContain('Kod okunamadı');
    expect(panelText()).toContain('bu kayıtta stacktrace yok');
  });
});
