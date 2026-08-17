// @vitest-environment jsdom
//
// v0.9.1133 (AI Faz 2.3) — satır yuvasının DAVRANIŞ sözleşmesi.
//
// NEDEN GERÇEK MOUNT: iddiaların hepsi çalışma-zamanı dalı ve hiçbiri tip
// sisteminden geçmiyor —
//   (1) çip KENDİ tık hedefi: `stopPropagation` düşerse kart açılırken
//       satır navigasyonu da ateşler, yani operatör tam sayfa detaya
//       ışınlanır ve kartı HİÇ göremez. Klavyede daha sinsi: iki host'un
//       `<tr>`si de `role="button"` + Enter/Space bağlıyor;
//   (2) TEK kart açık: URL tek değer taşıdığı için bu bedava gelir, ama
//       biri yerel bir ayna eklerse (üç kez gemiye giden URL→state
//       sınıfı) iki kart aynı anda mount olur — ikisi de LLM çağrısı;
//   (3) kart YALNIZ açıkken mount: maliyet disiplininin ta kendisi. Bir
//       refactor kartı koşulsuz çizip CSS ile gizlerse tsc susar, ES+LLM
//       faturası her satır için çıkar;
//   (4) Esc katman yığınından geçer (v0.9.950): kendi document
//       dinleyicisini kuran bir yüzey üstündeki modalı da kapatır.
//
// KAPSAM DÜRÜSTÇE: buradaki `Host` iki gerçek host'un satır anatomisinin
// AYNISI ama kendisi üretim kodu DEĞİL. Gerçek host'ların bu üç birimi
// aynı şekilde bağladığı ayrı bir yapısal kapıyla çivileniyor
// (insightHosts.test.ts) — "saf test ≠ BAĞLANMA" dersi.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { Fragment } from 'react';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, useLocation } from 'react-router-dom';
import {
  INSIGHT_PARAM, insightParams, useInsightRow, InsightRowChip, InsightRowSlot,
} from './insightRow';
import { escLayerDepth, topEscLayer, __resetEscLayers } from '@/lib/escLayer';

// Kartın MOUNT sayısı ölçülüyor, içi değil: kartın kendi sözleşmesi
// InsightCard.test.tsx'te. Burada tek soru "kaç kez ve ne zaman doğdu".
const seen = vi.hoisted(() => ({ mounts: [] as string[], unmounts: [] as string[] }));

vi.mock('@/components/ai/InsightCard', async () => {
  const { useEffect } = await import('react');
  return {
    InsightCard: ({ kind, id, onClose }: {
      kind: string; id: string; onClose?: () => void;
    }) => {
      useEffect(() => {
        seen.mounts.push(`${kind}:${id}`);
        return () => { seen.unmounts.push(`${kind}:${id}`); };
      }, [kind, id]);
      return (
        <div data-card={`${kind}:${id}`}>
          KART {kind}:{id}
          <button type="button" onClick={onClose}>kart-kapat</button>
        </div>
      );
    },
  };
});

let host: HTMLDivElement;
let root: Root;
/** Satır navigasyonu — gerçek host'ta openExcDetail / openDetail. */
let navs: string[];

/** Adres çubuğunun testten okunabilir aynası. */
function UrlProbe() {
  const loc = useLocation();
  return <i data-url={loc.search} />;
}

const url = () => host.querySelector('i')?.getAttribute('data-url') ?? '';
const cards = () => Array.from(host.querySelectorAll('[data-card]'))
  .map(e => e.getAttribute('data-card'));
const chips = () => Array.from(host.querySelectorAll('button'))
  .filter(b => (b.textContent ?? '').includes('Ne oldu?'));

/**
 * Host — iki gerçek host'un satır anatomisi: satır tıkı navigasyon,
 * çip kendi hedefi, kart satırın ALTINDA ve KOŞULLU.
 */
function Host({ ids }: { ids: string[] }) {
  const insight = useInsightRow();
  return (
    <>
      <UrlProbe />
      <table><tbody>
        {ids.map(id => (
          <Fragment key={id}>
            <tr data-row={id} tabIndex={0} role="button"
              onClick={() => navs.push(id)}
              onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') navs.push(id); }}>
              <td>
                <InsightRowChip open={insight.openId === id}
                  onToggle={() => insight.toggle(id)} />
              </td>
            </tr>
            {insight.openId === id && (
              <InsightRowSlot kind="exception" id={id} colSpan={1}
                onClose={insight.close} />
            )}
          </Fragment>
        ))}
      </tbody></table>
    </>
  );
}

async function mount(entry = '/exceptions', ids = ['fp1', 'fp2']) {
  await act(async () => {
    root.render(
      <MemoryRouter initialEntries={[entry]}>
        <Host ids={ids} />
      </MemoryRouter>,
    );
  });
}

const click = async (el: Element) => { await act(async () => { (el as HTMLElement).click(); }); };

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  __resetEscLayers();
  seen.mounts.length = 0;
  seen.unmounts.length = 0;
  navs = [];
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.clearAllMocks();
});

describe('insightParams — saf kodek', () => {
  it('yabancı parametreleri KORUR', () => {
    const out = insightParams(
      new URLSearchParams('tab=open&service=payment'), '', 'fp1');
    expect(out.get(INSIGHT_PARAM)).toBe('fp1');
    expect(out.get('tab')).toBe('open');
    expect(out.get('service')).toBe('payment');
  });

  it('null = parametre SİLİNİR, gerisi durur', () => {
    const out = insightParams(
      new URLSearchParams('insight=fp1&tab=open'), '', null);
    expect(out.has(INSIGHT_PARAM)).toBe(false);
    expect(out.get('tab')).toBe('open');
  });

  it('CANLI adres taban alınır — bayat router `prev`i param SİLMEZ', () => {
    // Trace/Dashboard ham history.replaceState ile yazıyor; router'ın
    // konumu o anahtarları hiç görmemiş olabiliyor. prev'i tek otorite
    // saymak, operatörün seçili span'ini adresten silerdi.
    const out = insightParams(
      new URLSearchParams('tab=open'), '?tab=open&span=abc123&range=6h', 'fp1');
    expect(out.get('span')).toBe('abc123');
    expect(out.get('range')).toBe('6h');
    expect(out.get(INSIGHT_PARAM)).toBe('fp1');
  });

  it('prev\'de olup canlıda olmayan anahtar EKLENİR (iki yönlü birleşim)', () => {
    const out = insightParams(
      new URLSearchParams('owner=sy-team'), '?range=6h', 'p1');
    expect(out.get('owner')).toBe('sy-team');
    expect(out.get('range')).toBe('6h');
  });

  it('aynı anahtar iki kez YAZILMAZ (canlı kazanır)', () => {
    const out = insightParams(
      new URLSearchParams('tab=stale'), '?tab=live', null);
    expect(out.getAll('tab')).toEqual(['live']);
  });
});

describe('çip — kendi tık hedefi', () => {
  it('kapalıyken çip var, kart YOK (kapalı satır sıfır istek)', async () => {
    await mount();
    expect(chips()).toHaveLength(2);
    expect(chips()[0].getAttribute('aria-expanded')).toBe('false');
    expect(cards()).toEqual([]);
    expect(seen.mounts).toEqual([]);
  });

  it('tık kartı satırın ALTINDA açar ve adrese yazar', async () => {
    await mount();
    await click(chips()[0]);

    expect(cards()).toEqual(['exception:fp1']);
    expect(seen.mounts).toEqual(['exception:fp1']);
    expect(url()).toContain(`${INSIGHT_PARAM}=fp1`);
    expect(chips()[0].getAttribute('aria-expanded')).toBe('true');
    // Kart, kendi satırının HEMEN ardından geliyor.
    const rows = Array.from(host.querySelectorAll('tr'));
    expect(rows[1].querySelector('[data-card]')).not.toBeNull();
  });

  it('çip tıkı satır navigasyonunu ATEŞLEMEZ, satır tıkı ateşler', async () => {
    await mount();
    await click(chips()[0]);
    expect(navs).toEqual([]);                       // çip ≠ gezinme

    await click(host.querySelector('[data-row="fp1"]')!);
    expect(navs).toEqual(['fp1']);                  // satır hâlâ gezinir
  });

  it('çipte Enter satırın Enter\'ını ATEŞLEMEZ', async () => {
    await mount();
    await act(async () => {
      chips()[0].dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
    });
    expect(navs).toEqual([]);
  });

  it('ikinci tık kapatır — kart UNMOUNT olur (akış kesilir)', async () => {
    await mount();
    await click(chips()[0]);
    await click(chips()[0]);

    expect(cards()).toEqual([]);
    expect(seen.unmounts).toEqual(['exception:fp1']);
    expect(url()).not.toContain(INSIGHT_PARAM);
  });

  it('BAŞKA satır açılınca öncekinin kartı kapanır (tek kart açık)', async () => {
    await mount();
    await click(chips()[0]);
    await click(chips()[1]);

    expect(cards()).toEqual(['exception:fp2']);
    expect(seen.mounts).toEqual(['exception:fp1', 'exception:fp2']);
    expect(seen.unmounts).toEqual(['exception:fp1']);
  });

  it('kartın kendi kapat düğmesi de yuvayı kapatır', async () => {
    await mount();
    await click(chips()[0]);
    await click(Array.from(host.querySelectorAll('button'))
      .find(b => b.textContent === 'kart-kapat')!);
    expect(cards()).toEqual([]);
    expect(url()).not.toContain(INSIGHT_PARAM);
  });
});

describe('adres = tek kaynak', () => {
  it('paylaşılan `?insight=` linki kartı AÇIK mount eder', async () => {
    await mount('/exceptions?tab=open&insight=fp2');
    expect(cards()).toEqual(['exception:fp2']);
    expect(seen.mounts).toEqual(['exception:fp2']);
  });

  it('kapanış yabancı parametreleri KORUR', async () => {
    await mount('/exceptions?tab=open&insight=fp2');
    await click(chips()[1]);
    expect(url()).toContain('tab=open');
    expect(url()).not.toContain(INSIGHT_PARAM);
  });

  it('URL\'deki bilinmeyen kimlik hiçbir kart açmaz (satırlar sağlam)', async () => {
    await mount('/exceptions?insight=silinmis-fp');
    expect(cards()).toEqual([]);
    expect(chips()).toHaveLength(2);
  });
});

describe('Esc — katman yığını', () => {
  it('kart açıkken TEK katman kayıtlı, kapalıyken sıfır', async () => {
    await mount();
    expect(escLayerDepth()).toBe(0);
    await click(chips()[0]);
    expect(escLayerDepth()).toBe(1);
    await click(chips()[0]);
    expect(escLayerDepth()).toBe(0);
  });

  it('tepe katman çağrılınca kart kapanır', async () => {
    await mount();
    await click(chips()[0]);
    await act(async () => { topEscLayer()!(); });
    expect(cards()).toEqual([]);
    expect(url()).not.toContain(INSIGHT_PARAM);
  });
});
