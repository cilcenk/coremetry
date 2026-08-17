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
  INSIGHT_PARAM, INSIGHT_KINDS, formatInsightParam, parseInsightParam,
  insightParams, useInsightRow, InsightRowChip, InsightRowSlot, InsightGridSlot,
} from './insightRow';
import { escLayerDepth, topEscLayer, __resetEscLayers } from '@/lib/escLayer';

// Kartın MOUNT sayısı ölçülüyor, içi değil: kartın kendi sözleşmesi
// InsightCard.test.tsx'te. Burada tek soru "kaç kez ve ne zaman doğdu".
const seen = vi.hoisted(() => ({ mounts: [] as string[], unmounts: [] as string[] }));

vi.mock('@/components/ai/InsightCard', async () => {
  const { useEffect } = await import('react');
  return {
    InsightCard: ({ kind, id, windowSec, onClose }: {
      kind: string; id: string; windowSec?: number; onClose?: () => void;
    }) => {
      useEffect(() => {
        const tag = windowSec ? `${kind}:${id}@${windowSec}` : `${kind}:${id}`;
        seen.mounts.push(tag);
        return () => { seen.unmounts.push(tag); };
      }, [kind, id, windowSec]);
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
function Host({ ids, kind = 'exception' }: {
  ids: string[]; kind?: 'exception' | 'problem' | 'log-pattern' | 'slow-query';
}) {
  const insight = useInsightRow(kind);
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
              <InsightRowSlot kind={kind} id={id} colSpan={1}
                onClose={insight.close} />
            )}
          </Fragment>
        ))}
      </tbody></table>
    </>
  );
}

async function mount(
  entry = '/exceptions', ids = ['fp1', 'fp2'],
  kind: 'exception' | 'problem' | 'log-pattern' | 'slow-query' = 'exception',
) {
  await act(async () => {
    root.render(
      <MemoryRouter initialEntries={[entry]}>
        <Host ids={ids} kind={kind} />
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
      new URLSearchParams('tab=open&service=payment'), '', 'exception:fp1');
    expect(out.get(INSIGHT_PARAM)).toBe('exception:fp1');
    expect(out.get('tab')).toBe('open');
    expect(out.get('service')).toBe('payment');
  });

  it('null = parametre SİLİNİR, gerisi durur', () => {
    const out = insightParams(
      new URLSearchParams('insight=exception:fp1&tab=open'), '', null);
    expect(out.has(INSIGHT_PARAM)).toBe(false);
    expect(out.get('tab')).toBe('open');
  });

  it('CANLI adres taban alınır — bayat router `prev`i param SİLMEZ', () => {
    // Trace/Dashboard ham history.replaceState ile yazıyor; router'ın
    // konumu o anahtarları hiç görmemiş olabiliyor. prev'i tek otorite
    // saymak, operatörün seçili span'ini adresten silerdi.
    const out = insightParams(
      new URLSearchParams('tab=open'), '?tab=open&span=abc123&range=6h', 'exception:fp1');
    expect(out.get('span')).toBe('abc123');
    expect(out.get('range')).toBe('6h');
    expect(out.get(INSIGHT_PARAM)).toBe('exception:fp1');
  });

  it('prev\'de olup canlıda olmayan anahtar EKLENİR (iki yönlü birleşim)', () => {
    const out = insightParams(
      new URLSearchParams('owner=sy-team'), '?range=6h', 'problem:p1');
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
    // Adres KANONİK değeri taşıyor: `<kind>:<enc(id)>` (URLSearchParams
    // ':' karakterini %3A olarak kodlar).
    expect(url()).toContain(`${INSIGHT_PARAM}=exception%3Afp1`);
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
    await mount('/exceptions?tab=open&insight=exception:fp2');
    expect(cards()).toEqual(['exception:fp2']);
    expect(seen.mounts).toEqual(['exception:fp2']);
  });

  it('kapanış yabancı parametreleri KORUR', async () => {
    await mount('/exceptions?tab=open&insight=exception:fp2');
    await click(chips()[1]);
    expect(url()).toContain('tab=open');
    expect(url()).not.toContain(INSIGHT_PARAM);
  });

  it('URL\'deki bilinmeyen kimlik hiçbir kart açmaz (satırlar sağlam)', async () => {
    await mount('/exceptions?insight=exception:silinmis-fp');
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

// ════════════════════════════════════════════════════════════════════
// v0.9.1137 (AI Faz 2.4) — tür önekli param + ızgara yuvası.
// ════════════════════════════════════════════════════════════════════

describe('tür kodeği — saf', () => {
  it('format → parse round-trip (boşluk, `|`, `:` içeren id\'ler)', () => {
    for (const [kind, id] of [
      ['exception', 'fp/1+2'],
      ['problem', 'svc:error_rate:1700000000'],
      ['log-pattern', 'Oracle errors (ORA-)'],
      ['slow-query', '12345|oracle'],
    ] as const) {
      const raw = formatInsightParam(kind, id);
      expect(parseInsightParam(raw)).toEqual({ kind, id });
    }
  });

  it('id İÇİNDEKİ ayraç belirsizlik yaratmaz (ilk `:` bölüyor)', () => {
    // Problem id'leri ':' taşıyabiliyor; naif bir split(':') id'yi keserdi.
    const raw = formatInsightParam('problem', 'a:b:c');
    expect(parseInsightParam(raw)?.id).toBe('a:b:c');
  });

  it('bilinmeyen tür, boş id ve bozuk kaçış → null', () => {
    for (const raw of [
      null, undefined, '', 'exception', 'exception:', ':fp1',
      'foo:bar', 'toString:x', 'log_pattern:x', 'Exception:fp1',
    ]) {
      expect(parseInsightParam(raw), String(raw)).toBeNull();
    }
  });

  it('tür kümesi sunucunun tanıdığı DÖRT türü kapsıyor', () => {
    // Kaynak: internal/ai/insight/contract.go Kinds(). Bir tür eklenip
    // burası güncellenmezse KIND_GATE tsc'yi kırar; bu iddia listenin
    // SIRASINI değil KAPSAMINI çiviliyor.
    expect([...INSIGHT_KINDS].sort()).toEqual(
      ['exception', 'log-pattern', 'problem', 'slow-query']);
  });
});

describe('tür çakışması — dört host tek paramı paylaşıyor', () => {
  it('YABANCI türün değeri kart AÇMAZ (kopyala-yapıştır güvenliği)', async () => {
    // /inbox'tan kopyalanmış bir adres (`problem:p-42`) exception host'una
    // yapıştırıldı: kart açılmamalı, satırlar sağlam kalmalı ve hiçbir
    // istek gitmemeli. Öncesinde (çıplak id) host bunu KENDİ kimliği
    // sanıp "p-42" için üretim tetiklerdi.
    await mount('/exceptions?insight=problem:p-42', ['fp1', 'fp2']);
    expect(cards()).toEqual([]);
    expect(seen.mounts).toEqual([]);
    expect(chips()).toHaveLength(2);
    // Esc katmanı da kirlenmemeli: yabancı bir değer için katman
    // kaydetmek Esc'i "hiçbir şey yapmıyor" hâline sokardı.
    expect(escLayerDepth()).toBe(0);
  });

  // NOT: iki mount tek `it` içinde YAPILMAZ — MemoryRouter
  // `initialEntries`i yalnız ilk mount'ta okur, ikinci render aynı
  // history'yi sürdürür ve test kendi kurgusunu ölçmüş olur.
  it('aynı id farklı TÜRde açılmaz (id uzayları kesişebilir)', async () => {
    await mount('/anomalies?insight=slow-query:fp1', ['fp1'], 'log-pattern');
    expect(cards()).toEqual([]);
  });

  it('aynı id KENDİ türünde açılır', async () => {
    await mount('/anomalies?insight=log-pattern:fp1', ['fp1'], 'log-pattern');
    expect(cards()).toEqual(['log-pattern:fp1']);
  });

  it('host kendi türünü YAZAR (adres türü taşır)', async () => {
    await mount('/databases/slow-queries', ['12345|oracle'], 'slow-query');
    await click(chips()[0]);
    expect(url()).toContain('insight=slow-query%3A12345%7Coracle');
    expect(cards()).toEqual(['slow-query:12345|oracle']);
  });

  it('yabancı değer üstüne yazılır, yabancı PARAMLAR korunur', async () => {
    await mount('/anomalies?tab=open&insight=problem:p-42', ['Disk full'], 'log-pattern');
    await click(chips()[0]);
    expect(url()).toContain('tab=open');
    expect(url()).toContain('insight=log-pattern%3ADisk+full');
    expect(cards()).toEqual(['log-pattern:Disk full']);
  });
});

describe('InsightGridSlot — tablo OLMAYAN host', () => {
  /** GridHost — /anomalies'in desen ızgarasının anatomisi. */
  function GridHost({ ids }: { ids: string[] }) {
    const insight = useInsightRow('log-pattern');
    return (
      <>
        <UrlProbe />
        <div data-grid style={{ display: 'grid' }}>
          {ids.map(id => (
            <Fragment key={id}>
              <div data-card-shell={id}>
                <InsightRowChip open={insight.openId === id}
                  onToggle={() => insight.toggle(id)} />
              </div>
              {insight.openId === id && (
                <InsightGridSlot kind="log-pattern" id={id} onClose={insight.close} />
              )}
            </Fragment>
          ))}
        </div>
      </>
    );
  }

  const mountGrid = async (entry = '/anomalies') => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={[entry]}>
          <GridHost ids={['Out of memory', 'Disk full']} />
        </MemoryRouter>,
      );
    });
  };

  it('kart TAM GENİŞLİK alıyor ve kendi kabuğunun ARDINDA duruyor', async () => {
    await mountGrid();
    await click(chips()[0]);

    const slot = host.querySelector('[data-insight-row]') as HTMLElement;
    expect(slot).not.toBeNull();
    // `<tr>` DEĞİL: ızgarada satır bağlamı yok (geçersiz HTML, yalnız
    // çalışma zamanında uyarı verirdi).
    expect(slot.tagName).toBe('DIV');
    expect(slot.style.gridColumn).toBe('1 / -1');
    // Uzun ifade/örnek satırının ızgarayı taşırmaması için şart.
    expect(slot.style.minWidth).toBe('0px');

    const grid = host.querySelector('[data-grid]')!;
    const kids = Array.from(grid.children);
    expect(kids[0].getAttribute('data-card-shell')).toBe('Out of memory');
    expect(kids[1].getAttribute('data-insight-row')).toBe('Out of memory');
  });

  it('ızgara host\'unda da TEK kart açık ve kapanınca unmount', async () => {
    await mountGrid();
    await click(chips()[0]);
    await click(chips()[1]);
    expect(cards()).toEqual(['log-pattern:Disk full']);
    expect(seen.unmounts).toEqual(['log-pattern:Out of memory']);
    await click(chips()[1]);
    expect(cards()).toEqual([]);
    expect(url()).not.toContain(INSIGHT_PARAM);
  });
});

describe('pencere yuvadan karta geçiyor', () => {
  function WindowHost({ windowSec }: { windowSec?: number }) {
    const insight = useInsightRow('slow-query');
    return (
      <>
        <UrlProbe />
        <table><tbody>
          <tr>
            <td>
              <InsightRowChip open={insight.openId === '7'}
                onToggle={() => insight.toggle('7')} />
            </td>
          </tr>
          {insight.openId === '7' && (
            <InsightRowSlot kind="slow-query" id="7" colSpan={1}
              windowSec={windowSec} onClose={insight.close} />
          )}
        </tbody></table>
      </>
    );
  }

  it('windowSec karta ULAŞIYOR — sayfa aralığı kanıt penceresi olur', async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={['/databases/slow-queries']}>
          <WindowHost windowSec={21600} />
        </MemoryRouter>,
      );
    });
    await click(chips()[0]);
    // Yuva pencereyi düşürürse kart sunucunun 1sa varsayılanına düşer ve
    // 6sa'lık bir sayfada satırdan BAŞKA sayı gösterir.
    expect(seen.mounts).toEqual(['slow-query:7@21600']);
  });
});
