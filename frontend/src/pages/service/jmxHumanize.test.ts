// v0.9.383 (redesign D7) — JMX panel başlığı insanileştirme pinleri.
// Ham ad HER ZAMAN raw'da döner (alt yazı) — bilgi kaybı yasak.
import { describe, expect, it } from 'vitest';
import { jmxHumanize } from './jmxHumanize';

describe('jmxHumanize', () => {
  const cases: Array<[raw: string, title: string]> = [
    ['jvm_memory_bytes_used', 'Memory bytes used — JVM'],
    ['jvm_gc_collection_seconds', 'GC collection seconds — JVM'],
    ['jvm_threads_current', 'Threads current — JVM'],
    ['jboss_undertow_request_count_total', 'Undertow request count — JBoss'],
    ['jboss_datasource_active_count', 'Datasource active count — JBoss'],
    ['jboss_xa_pool_in_use', 'XA pool in use — JBoss'],
    // aile öneki yok → aile eki yok, ad yine akar
    ['process_cpu_seconds_total', 'Process CPU seconds'],
    // tek segment / tuhaf ad → asla boş başlık
    ['jvm_', 'jvm_'],
  ];
  it.each(cases)('%s → %s', (raw, title) => {
    const r = jmxHumanize(raw);
    expect(r.title).toBe(title);
    expect(r.raw).toBe(raw);
  });
});
