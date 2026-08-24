// @vitest-environment jsdom
//
// callerLogsPivot — v0.9.1367.
//
// NE ÇİVİLİYOR: /database'in log pivotunun TAŞINABİLİR kapsamı, yani
// yapısal `service` kolon yüklemi. Bu bir stil tercihi değil, ölçülmüş bir
// backend kısıtı:
//
//   · ClickHouse log backend'inde serbest metin `q`, `body` üzerinde DÜZ
//     BİR ALT DİZE aramasıdır (`internal/logstore/clickhouse.go:310`,
//     `multiSearchAnyCaseInsensitive(body, [?])`). Alan sözdizimi YOK.
//     Yani `q=service.name:"orders"` CH kurulumunda body'de o literal
//     metni arar ve BOŞ döner — link ölüdür.
//   · `service` ise her iki backend'de de yapısal: CH `service_name = ?`
//     (`clickhouse.go:289`), ES servis alanı fan-out'u.
//
// Bu depo ölü pivot bedelini İKİ KEZ ödedi (v0.9.256 messaging, v0.9.268
// db trace linki: canlı veride 2201 span'lik bir satırın linki 0 eşleşti).
// Üçüncüsü bir testle karşılansın.
//
// NEDEN GERÇEK MOUNT: link tip-doğru şekilde YANLIŞ kurulabilir —
// `service` yerine `q` geçmek, pencereyi düşürmek, env'i taşımamak. Üçü de
// tsc'de sessiz ve üçü de operatörün gördüğü listeyi sessizce yanlışlar.
import { describe, it, expect, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import { DatabaseCallersSection } from './detailSections';
import type { DBCallerBreakdown, TimeRange } from '@/lib/types';

let host: HTMLDivElement | null = null;
let root: Root | null = null;

const RANGE: TimeRange = { preset: '1h' } as TimeRange;

const CALLERS: DBCallerBreakdown[] = [
  {
    service: 'orders-api', pod: 'orders-api-7c9', spanCount: 1200,
    errorCount: 3, errorRate: 0.25, avgDurationMs: 12.5, p99DurationMs: 88,
  } as DBCallerBreakdown,
  {
    service: 'billing-worker', pod: 'billing-worker-2', spanCount: 40,
    errorCount: 0, errorRate: 0, avgDurationMs: 400, p99DurationMs: 1200,
  } as DBCallerBreakdown,
];

function render(env?: string): HTMLElement {
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => {
    root!.render(
      <MemoryRouter>
        <DatabaseCallersSection callers={CALLERS} range={RANGE} env={env} />
      </MemoryRouter>,
    );
  });
  return host;
}

/** Satırların /logs hedefleri. */
function logHrefs(el: HTMLElement): string[] {
  return Array.from(el.querySelectorAll('a'))
    .map(a => a.getAttribute('href') ?? '')
    .filter(h => h.startsWith('/logs'));
}

afterEach(() => {
  act(() => { root?.unmount(); });
  host?.remove();
  host = null; root = null;
});

describe('çağıran satırı → log pivotu', () => {
  it('her çağıran satırı bir /logs linki taşıyor', () => {
    const hrefs = logHrefs(render());
    expect(hrefs.length).toBe(CALLERS.length);
  });

  it('kapsam YAPISAL service yüklemi — CH backend\'inde de yaşar', () => {
    for (const href of logHrefs(render())) {
      const q = new URLSearchParams(href.slice('/logs?'.length));
      expect(q.get('service'), href).toBeTruthy();
    }
    expect(logHrefs(render()).map(h => new URLSearchParams(h.slice(6)).get('service')).sort())
      .toEqual(['billing-worker', 'orders-api']);
  });

  it('serbest metin `q` KULLANILMIYOR — alan sözdizimi ES\'e özgü', () => {
    // Bu iddia dar ve kasıtlı: `q=service.name:"…"` ClickHouse'ta body alt
    // dizesi olarak aranır ve boş döner. Testin kırılması "bir tercih
    // değişti" değil, "link bir backend'de öldü" demektir.
    for (const href of logHrefs(render())) {
      const q = new URLSearchParams(href.slice('/logs?'.length));
      expect(q.get('q'), `${href} — q alan sözdizimi taşıyor`).toBeNull();
    }
  });

  it('PENCERE taşınıyor — sayfanın dilimi, /logs varsayılanı değil', () => {
    // window zorunlu argüman (logsUrl.ts, v0.9.1347) ama ZORUNLU OLMAK
    // taşınmayı garanti etmiyor: `null` geçmek de derlenirdi ve link
    // /logs'un kendi varsayılan penceresine düşerdi.
    for (const href of logHrefs(render())) {
      expect(new URLSearchParams(href.slice('/logs?'.length)).get('range'), href).toBeTruthy();
    }
  });

  it('env verildiğinde TAŞINIR, verilmediğinde YAZILMAZ', () => {
    for (const href of logHrefs(render('prod'))) {
      expect(new URLSearchParams(href.slice('/logs?'.length)).get('env')).toBe('prod');
    }
    for (const href of logHrefs(render())) {
      expect(new URLSearchParams(href.slice('/logs?'.length)).get('env')).toBeNull();
    }
  });

  it('servis linki DURUYOR — log pivotu onun yerine geçmedi', () => {
    const el = render();
    const svc = Array.from(el.querySelectorAll('a'))
      .map(a => a.getAttribute('href') ?? '')
      .filter(h => h.startsWith('/service'));
    expect(svc.length).toBe(CALLERS.length);
  });

  it('çağıran yokken pivot da yok (boş durum dalı korunuyor)', () => {
    host = document.createElement('div');
    document.body.appendChild(host);
    root = createRoot(host);
    act(() => {
      root!.render(
        <MemoryRouter>
          <DatabaseCallersSection callers={[]} range={RANGE} />
        </MemoryRouter>,
      );
    });
    expect(logHrefs(host).length).toBe(0);
    expect(host.textContent).toContain('No caller in this window');
  });
});
