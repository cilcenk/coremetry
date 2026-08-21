import { describe, expect, it } from 'vitest';
import { classifyMetric } from './metricTemplates';
import type { MetricInfo } from './types';

const m = (name: string, type: string, unit = ''): MetricInfo =>
  ({ name, type, unit, description: '' } as MetricInfo);

// v0.9.271 — first test file for this module.
//
// The type dictionary branched on 'counter' and 'updowncounter', which the
// ingest never writes. internal/otlp/convert.go:267 is the only writer of
// Instrument and its entire vocabulary is: gauge, sum, histogram,
// exp_histogram, summary. So the two branches were dead and 'sum' fell through
// to `default: avg`.
//
// Measured against the live catalogue at the time of the fix: 79 of 114
// metrics (69.3%) carry instrument='sum', and every one of them opened in
// Explore aggregated as avg.
describe('classifyMetric — OTel type fallback', () => {
  // The vocabulary the backend ACTUALLY writes. A name here that stops
  // resolving means the ingest and the frontend have drifted apart again.
  const cases: Array<[string, string]> = [
    // v0.9.1201 — sum→rate: FE agg seti artık rate sunuyor ve kümülatif
    // counter'ın varsayılanı hız (v0.9.271 sum'ı "en iyi mevcut" diye
    // seçmişti; VM'de bare sum(sel) tırmanan kümülatifi çiziyordu).
    ['sum', 'rate'],
    ['gauge', 'last'],
    ['histogram', 'p99'],
    ['exp_histogram', 'p99'],
    ['summary', 'p99'],
  ];
  it.each(cases)('instrument %s → agg %s', (type, want) => {
    // A deliberately template-proof name, so the TYPE path is what is tested.
    expect(classifyMetric(m('zz.unmatched.metric', type))?.agg).toBe(want);
  });

  it('sum avg\'a DA sum\'a DA düşmez — hız çizer (v0.9.271 → v0.9.1201)', () => {
    expect(classifyMetric(m('demo.auth.logged_in', 'sum'))?.agg).toBe('rate');
  });

  it('counter ŞABLONLARI da rate — Error/Request counter (v0.9.1201)', () => {
    expect(classifyMetric(m('demo.payment.errors', 'sum'))?.agg).toBe('rate');
    expect(classifyMetric(m('http_server_requests_total', 'sum'))?.agg).toBe('rate');
  });

  it('the ingest vocabulary is covered; unknown words still degrade to avg', () => {
    // Not a regression guard so much as a statement of intent: an instrument
    // this dictionary has never heard of must not throw or return null.
    expect(classifyMetric(m('zz.unmatched.metric', 'counter'))?.agg).toBe('avg');
    expect(classifyMetric(m('zz.unmatched.metric', ''))?.agg).toBe('avg');
  });
});

// The nine non-monotonic sums in the live catalogue are all process.runtime.*.
// They are LEVELS reported as OTel updowncounters, and monotonicity never
// reaches the frontend (metric_catalog carries instrument only). Without a name
// rule the fallback above would aggregate them as sum, which is worse than the
// avg it replaced: avg(heap_alloc) is a meaningful number, sum(heap_alloc) over
// a window is noise.
describe('classifyMetric — runtime levels must not be summed', () => {
  const live = [
    'process.runtime.go.mem.heap_alloc',
    'process.runtime.go.mem.heap_idle',
    'process.runtime.go.mem.heap_inuse',
    'process.runtime.go.mem.heap_objects',
    'process.runtime.go.mem.heap_released',
    'process.runtime.go.mem.heap_sys',
    'process.runtime.go.mem.live_objects',
    'process.runtime.go.goroutines',
    'process.runtime.go.cgo.calls',
  ];
  it.each(live)('%s → last, not sum', (name) => {
    // Every one of these arrives as instrument='sum' — that is exactly why the
    // name rule has to win over the type fallback.
    expect(classifyMetric(m(name, 'sum'))?.agg).toBe('last');
  });

  it('the name rule runs BEFORE the type fallback', () => {
    const t = classifyMetric(m('process.runtime.go.mem.heap_alloc', 'sum'));
    expect(t?.id).toBe('Runtime level');
  });

  it('other runtimes are covered too, not just Go', () => {
    expect(classifyMetric(m('process.runtime.jvm.mem.heap_used', 'sum'))?.agg).toBe('last');
  });

  it('a genuine counter that merely starts with process. is NOT caught', () => {
    // The rule is deliberately anchored on the runtime-level segments rather
    // than the whole process.* namespace, so it cannot swallow real counters.
    // (v0.9.1201: counter fallback sum→rate; testin özü "last DEĞİL".)
    expect(classifyMetric(m('process.requests.total', 'sum'))?.agg).toBe('rate');
  });
});

// ── v0.9.1180 — VictoriaMetrics / Prometheus yazımları ────────────────────
//
// Kayıt defteri OTel semconv adlarına (noktalı) göre yazıldı; VM backend'inde
// katalog Prometheus-adlı geliyor. Prod'un canlı ailesi (operatör ekranı,
// 2026-08-17) `http_server_request_duration_seconds_*` ve hiçbir regex bunu
// tutmuyordu: operatör ana HTTP gecikme metriğini seçtiğinde p99 varsayılanı,
// route/status kırılımı ve 1s eşiği HİÇ gelmiyor, sessizce type-fallback'e
// düşüyordu.
//
// İkinci yarısı ayrılamaz: eşleşmeyi düzeltip birimi taşımamak ZARARLI olurdu
// — şablonun `unit: 'ms'` ipucu saniyelik p99'a damgalanır ve 0.25 s "0.25ms"
// diye okunurdu. Backend artık birimi addan türetiyor (v0.9.1180
// describeMetricName); burada onun şablonu YENDİĞİ pinleniyor.
describe('classifyMetric — Prometheus yazımları (VM backend)', () => {
  it('prod ailesi HTTP gecikme şablonunu bulur', () => {
    const t = classifyMetric(m('http_server_request_duration_seconds_bucket', 'histogram', 's'));
    expect(t?.id).toBe('HTTP server latency');
    expect(t?.agg).toBe('p99');
    expect(t?.groupBy).toContain('http.route');
  });

  it('histogramın üç parçası da aynı şablona düşer', () => {
    for (const suffix of ['_bucket', '_sum', '_count']) {
      const t = classifyMetric(m(`http_server_request_duration_seconds${suffix}`, 'histogram', 's'));
      expect(t?.id).toBe('HTTP server latency');
    }
  });

  it('BEYAN EDİLEN birim şablonun ipucunu yener', () => {
    // Şablon 'ms' öneriyor; seri saniye. Bu satır olmadan 0.25 s "0.25ms".
    const t = classifyMetric(m('http_server_request_duration_seconds_bucket', 'histogram', 's'));
    expect(t?.unit).toBe('s');
  });

  it('birim beyan edilmezse şablonun ipucu korunur', () => {
    const t = classifyMetric(m('http.server.request.duration', 'histogram'));
    expect(t?.unit).toBe('ms');
  });

  it('HTTP dışındaki aileler de kurtulur — tek normalleştirici', () => {
    expect(classifyMetric(m('db_client_operation_duration_seconds_bucket', 'histogram', 's'))?.id)
      .toBe('DB query latency');
    expect(classifyMetric(m('rpc_server_duration_seconds_bucket', 'histogram', 's'))?.id)
      .toBe('RPC latency');
  });

  it('ham ad ÖNCE denenir — kayıt defterindeki Prometheus yazımları ezilmez', () => {
    // `cpu_percent` kayıt defterinde ham hâliyle var; normalleştirilmiş aday
    // (`cpu.percent`) onu ezmemeli.
    expect(classifyMetric(m('cpu_percent', 'gauge'))?.id).toBe('CPU utilisation');
  });

  it('noktalı OTel adları aynen çalışmaya devam eder', () => {
    expect(classifyMetric(m('http.server.request.duration', 'histogram'))?.id)
      .toBe('HTTP server latency');
  });

  it('_total soyulur ama uydurma eşleşme üretmez', () => {
    // `http_requests_total` zaten ham hâliyle Request counter'a düşüyordu;
    // normalleştirme onu bozmamalı.
    expect(classifyMetric(m('http_requests_total', 'sum'))?.id).toBe('Request counter');
  });
});
