import { describe, it, expect } from 'vitest';
import { buildDetailsMetricPanels, otlpUnitToGrafana } from './detailsMetricPanels';
import type { MetricInfo } from '@/lib/types';

// v0.9.784 — Details "Metrikler" bölümünün kural tablosu.
//
// Bu dosyanın PİNLEDİĞİ karar: histogram süre ailesi avg üretir, p95
// ÜRETMEZ. Prod histogramlarında bucket_bounds yok; metrikten yüzdelik
// hesaplanamıyor ve panel sessizce boş kalıyor (v0.9.774). Testi
// gevşetmek o olayı geri getirir.

function m(name: string, type: string, unit = ''): MetricInfo {
  return { name, description: '', unit, type };
}

/** Panel anahtarı → metrik adları (sıra korunur). */
function shape(catalog: MetricInfo[]): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  for (const p of buildDetailsMetricPanels(catalog)) out[p.key] = p.metrics.map(x => x.name);
  return out;
}

function panel(catalog: MetricInfo[], key: string) {
  return buildDetailsMetricPanels(catalog).find(p => p.key === key);
}

// ── Gerçek kurulumlardan alınmış iki katalog (CH ground truth) ──────────
// api-gateway: Go demo üreticisi — kural tablosunun yedi ailesini de basar.
const API_GATEWAY: MetricInfo[] = [
  m('db.client.connections.max', 'gauge', '{connection}'),
  m('db.client.connections.usage', 'gauge', '{connection}'),
  m('db.client.connections.utilization', 'gauge', '1'),
  m('demo.dashboard.viewed', 'sum', '1'),
  m('http.server.active_requests', 'gauge', '1'),
  m('http.server.duration', 'histogram', 'ms'),
  m('http.server.queued_requests', 'gauge', '1'),
  m('http.server.requests', 'sum', '1'),
  m('process.runtime.cpu.utilization', 'gauge', '1'),
  m('process.runtime.gc.pause', 'gauge', 'ms'),
  m('process.runtime.goroutines', 'gauge', '1'),
  m('process.runtime.memory.rss', 'gauge', 'By'),
  m('system.cpu.utilization', 'gauge', '1'),
  m('system.memory.utilization', 'gauge', '1'),
];

// coremetry-monolithic: otel-go runtime — ÖTEKİ adlandırma stili.
const MONOLITHIC: MetricInfo[] = [
  m('http.server.duration', 'histogram', 'ms'),
  m('http.server.request.size', 'sum', 'By'),
  m('http.server.response.size', 'sum', 'By'),
  m('process.runtime.go.cgo.calls', 'sum'),
  m('process.runtime.go.gc.count', 'sum'),
  m('process.runtime.go.gc.pause_ns', 'histogram'),
  m('process.runtime.go.gc.pause_total_ns', 'sum'),
  m('process.runtime.go.goroutines', 'sum'),
  m('process.runtime.go.mem.heap_alloc', 'sum', 'By'),
  m('process.runtime.go.mem.heap_inuse', 'sum', 'By'),
  m('process.runtime.go.mem.heap_objects', 'sum'),
  m('runtime.uptime', 'sum', 'ms'),
];

describe('otlpUnitToGrafana', () => {
  const cases: { unit: string | undefined; want: string | undefined }[] = [
    { unit: 'By', want: 'bytes' },
    { unit: 'ms', want: 'ms' },
    { unit: 's', want: 's' },
    { unit: 'ns', want: 'ns' },
    { unit: '%', want: 'percent' },
    { unit: '1', want: undefined },          // boyutsuz = birim yok
    { unit: '', want: undefined },
    { unit: undefined, want: undefined },
    { unit: '{connection}', want: undefined }, // UCUM annotation → ham sayı
    { unit: 'furlong', want: undefined },      // bilinmeyen → sessizce ms DEĞİL
  ];
  for (const c of cases) {
    it(`${String(c.unit)} → ${String(c.want)}`, () => {
      expect(otlpUnitToGrafana(c.unit)).toBe(c.want);
    });
  }
});

describe('buildDetailsMetricPanels — aile kuralları', () => {
  const cases: {
    name: string;
    catalog: MetricInfo[];
    wantKeys: string[];
    wantMetrics?: Record<string, string[]>;
  }[] = [
    {
      name: 'boş katalog → panel yok',
      catalog: [],
      wantKeys: [],
    },
    {
      name: 'HTTP istek hızı — sum ailesi',
      catalog: [m('http.server.requests', 'sum', '1')],
      wantKeys: ['http-rate'],
    },
    {
      name: 'HTTP istek hızı — ÖTEKİ ad stili (request.count)',
      catalog: [m('http.server.request.count', 'sum', '1')],
      wantKeys: ['http-rate'],
    },
    {
      name: 'HTTP süre — lokal ad (http.server.duration)',
      catalog: [m('http.server.duration', 'histogram', 'ms')],
      wantKeys: ['http-duration'],
    },
    {
      name: 'HTTP süre — prod adı (http.server.request.duration, s)',
      catalog: [m('http.server.request.duration', 'histogram', 's')],
      wantKeys: ['http-duration'],
    },
    {
      name: 'aktif+kuyruk tek kartta iki seri',
      catalog: [
        m('http.server.queued_requests', 'gauge', '1'),
        m('http.server.active_requests', 'gauge', '1'),
      ],
      wantKeys: ['http-inflight'],
      // Sıra kural tablosundan gelir (aktif önce), katalog sırasından değil.
      wantMetrics: { 'http-inflight': ['http.server.active_requests', 'http.server.queued_requests'] },
    },
    {
      name: 'UpDownCounter sum olarak inse de aktif/kuyruk paneli kurulur',
      catalog: [m('http.server.active_requests', 'sum', '1')],
      wantKeys: ['http-inflight'],
    },
    {
      name: 'DB havuzu — usage önce, sonra max/idle',
      catalog: [
        m('db.client.connections.idle', 'gauge', '{connection}'),
        m('db.client.connections.max', 'gauge', '{connection}'),
        m('db.client.connections.usage', 'gauge', '{connection}'),
      ],
      wantKeys: ['db-pool'],
      wantMetrics: {
        'db-pool': [
          'db.client.connections.usage',
          'db.client.connections.max',
          'db.client.connections.idle',
        ],
      },
    },
    {
      name: 'süreç belleği — process.runtime.memory.rss',
      catalog: [m('process.runtime.memory.rss', 'gauge', 'By')],
      wantKeys: ['process-memory'],
    },
    {
      name: 'süreç belleği — process.memory.usage',
      catalog: [m('process.memory.usage', 'gauge', 'By')],
      wantKeys: ['process-memory'],
    },
    {
      name: 'CPU — iki kaynak tek kartta (process + system)',
      catalog: [
        m('system.cpu.utilization', 'gauge', '1'),
        m('process.runtime.cpu.utilization', 'gauge', '1'),
      ],
      wantKeys: ['cpu-utilization'],
    },
    {
      name: 'goroutine — demo ad stili',
      catalog: [m('process.runtime.goroutines', 'gauge', '1')],
      wantKeys: ['runtime-goroutines'],
    },
    {
      name: 'goroutine — monolithic ad stili',
      catalog: [m('process.runtime.go.goroutines', 'sum')],
      wantKeys: ['runtime-goroutines'],
    },
    {
      name: 'GC — gc.pause (ms) ad stili',
      catalog: [m('process.runtime.gc.pause', 'gauge', 'ms')],
      wantKeys: ['runtime-gc'],
    },
    {
      name: 'GC — gc.pause_ns ad stili; kümülatif pause_total_ns DIŞARIDA',
      catalog: [
        m('process.runtime.go.gc.pause_ns', 'histogram'),
        m('process.runtime.go.gc.pause_total_ns', 'sum'),
      ],
      wantKeys: ['runtime-gc'],
      wantMetrics: { 'runtime-gc': ['process.runtime.go.gc.pause_ns'] },
    },
    {
      name: 'bilinmeyen instrument/aile → panel yok',
      catalog: [
        m('demo.dashboard.viewed', 'sum', '1'),
        m('kafka.consumer.lag', 'gauge', '1'),
        m('runtime.uptime', 'sum', 'ms'),
      ],
      wantKeys: [],
    },
    {
      name: 'HTTP süresi histogram DEĞİLSE panel kurulmaz',
      catalog: [m('http.server.duration', 'gauge', 'ms')],
      wantKeys: [],
    },
    {
      name: 'HTTP istek hızı sum DEĞİLSE panel kurulmaz',
      catalog: [m('http.server.requests', 'gauge', '1')],
      wantKeys: [],
    },
  ];

  for (const c of cases) {
    it(c.name, () => {
      const got = buildDetailsMetricPanels(c.catalog);
      expect(got.map(p => p.key)).toEqual(c.wantKeys);
      if (c.wantMetrics) {
        for (const [k, names] of Object.entries(c.wantMetrics)) {
          expect(got.find(p => p.key === k)?.metrics.map(x => x.name)).toEqual(names);
        }
      }
    });
  }
});

describe('buildDetailsMetricPanels — agg kararı', () => {
  it('histogram süre ailesi AVG üretir, p95 ÜRETMEZ (v0.9.774 pini)', () => {
    const p = panel([m('http.server.duration', 'histogram', 'ms')], 'http-duration');
    expect(p?.agg).toBe('avg');
    // Pin: hiçbir panel yüzdelik agg'e kaymasın.
    for (const q of buildDetailsMetricPanels([...API_GATEWAY, ...MONOLITHIC])) {
      expect(['avg', 'rate']).toContain(q.agg);
    }
  });

  it('monoton sayaç ailesi rate üretir', () => {
    expect(panel([m('http.server.requests', 'sum', '1')], 'http-rate')?.agg).toBe('rate');
  });

  it('gauge aileleri avg üretir', () => {
    for (const [name, type, key] of [
      ['http.server.active_requests', 'gauge', 'http-inflight'],
      ['db.client.connections.usage', 'gauge', 'db-pool'],
      ['process.runtime.memory.rss', 'gauge', 'process-memory'],
      ['system.cpu.utilization', 'gauge', 'cpu-utilization'],
      ['process.runtime.goroutines', 'gauge', 'runtime-goroutines'],
    ] as const) {
      expect(panel([m(name, type, '1')], key)?.agg).toBe('avg');
    }
  });
});

describe('buildDetailsMetricPanels — birim eşlemesi', () => {
  const cases: { name: string; catalog: MetricInfo[]; key: string; want: string | undefined }[] = [
    { name: 'By → bytes', catalog: [m('process.runtime.memory.rss', 'gauge', 'By')], key: 'process-memory', want: 'bytes' },
    { name: 'ms → ms', catalog: [m('http.server.duration', 'histogram', 'ms')], key: 'http-duration', want: 'ms' },
    { name: 's → s', catalog: [m('http.server.request.duration', 'histogram', 's')], key: 'http-duration', want: 's' },
    { name: '1 → birim yok', catalog: [m('http.server.active_requests', 'gauge', '1')], key: 'http-inflight', want: undefined },
    { name: '{connection} → birim yok', catalog: [m('db.client.connections.usage', 'gauge', '{connection}')], key: 'db-pool', want: undefined },
    { name: 'rate ailesi reqps (katalog birimi ezilir)', catalog: [m('http.server.requests', 'sum', '1')], key: 'http-rate', want: 'reqps' },
    { name: 'CPU percentunit (0-1 değeri ELLE ölçeklenmez)', catalog: [m('system.cpu.utilization', 'gauge', '1')], key: 'cpu-utilization', want: 'percentunit' },
    { name: 'gc.pause_ns birimsiz gelse de ns (addan)', catalog: [m('process.runtime.go.gc.pause_ns', 'histogram')], key: 'runtime-gc', want: 'ns' },
  ];
  for (const c of cases) {
    it(c.name, () => {
      expect(panel(c.catalog, c.key)?.unit).toBe(c.want);
    });
  }

  it('birim karıştırma: ayrışan birimli aday panele ALINMAZ', () => {
    const p = panel([
      m('process.runtime.gc.pause', 'gauge', 'ms'),
      m('process.runtime.go.gc.pause_ns', 'histogram'),
    ], 'runtime-gc');
    expect(p?.unit).toBe('ms');
    expect(p?.metrics.map(x => x.name)).toEqual(['process.runtime.gc.pause']);
  });
});

describe('buildDetailsMetricPanels — groupBy', () => {
  it('yalnız DB havuzu ailesinde state kırılımı var', () => {
    const all = buildDetailsMetricPanels([...API_GATEWAY, ...MONOLITHIC]);
    const withGroup = all.filter(p => p.groupBy);
    expect(withGroup.map(p => p.key)).toEqual(['db-pool']);
    expect(withGroup[0].groupBy).toBe('state');
  });
});

describe('buildDetailsMetricPanels — gerçek kataloglar', () => {
  // Yedi kural ailesi → SEKİZ panel: GC/goroutine ailesi iki karta ayrılır
  // (adet vs süre — tek kartta birleştirmek birim karıştırma olurdu).
  it('api-gateway: yedi ailenin hepsi kurulur (GC/goroutine iki kart)', () => {
    expect(shape(API_GATEWAY)).toEqual({
      'http-rate': ['http.server.requests'],
      'http-duration': ['http.server.duration'],
      'http-inflight': ['http.server.active_requests', 'http.server.queued_requests'],
      'db-pool': ['db.client.connections.usage', 'db.client.connections.max'],
      'process-memory': ['process.runtime.memory.rss'],
      'cpu-utilization': ['process.runtime.cpu.utilization', 'system.cpu.utilization'],
      'runtime-goroutines': ['process.runtime.goroutines'],
      'runtime-gc': ['process.runtime.gc.pause'],
    });
  });

  it('coremetry-monolithic: yalnız ailesi olan üç panel', () => {
    expect(shape(MONOLITHIC)).toEqual({
      'http-duration': ['http.server.duration'],
      'runtime-goroutines': ['process.runtime.go.goroutines'],
      'runtime-gc': ['process.runtime.go.gc.pause_ns'],
    });
  });

  it('bir metrik en fazla bir panele girer', () => {
    const seen = new Set<string>();
    for (const p of buildDetailsMetricPanels([...API_GATEWAY, ...MONOLITHIC])) {
      for (const x of p.metrics) {
        expect(seen.has(x.name)).toBe(false);
        seen.add(x.name);
      }
    }
  });

  it('panel anahtarları benzersiz (storageKey çakışması yok)', () => {
    const keys = buildDetailsMetricPanels(API_GATEWAY).map(p => p.key);
    expect(new Set(keys).size).toBe(keys.length);
  });
});
