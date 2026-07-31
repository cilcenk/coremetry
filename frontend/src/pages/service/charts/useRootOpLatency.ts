import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { entryLatencyDSL } from '@/lib/entrySpans';
import type { SpanMetricResult } from '@/lib/types';

// useRootOpLatency — Service Overview "Response time" kartının operasyon
// kırılımı verisi (v0.9.484, operatör onayı: "root spanler için multichart").
//
// ES/CH maliyet disiplini: `enabled` KAPALIYKEN hiç istek yok. Kırılım
// varsayılan görünüm DEĞİL; operatör açtığında (?rtops=1) tek sorgu atılır.
// Bu bilinçli: DSL'deki `kind in [server, consumer]` operation MV fast-path'ini
// diskalifiye ediyor (operationMVGate yalnız service.name/name eşitliği kabul
// eder), yani bu sorgu SINIRLANDIRILMIŞ ham-span taraması — toplam görünümün
// giriş sorgusuyla aynı maliyet sınıfı, ama varsayılan olarak bedava olmalı.
// (Sunucu tarafı zaten LIMIT + max_execution_time + zaman sınırlı WHERE
// taşıyor; ayrıca /api/spans/metric s.serveCached ile 30sn cache'li.)
//
// DSL entryLatencyDSL(service) — toplam görünümün AYNISI, aynen. İki görünüm
// aynı popülasyonu anlatmak zorunda; ayrı yazılan iki filtre birbirinden
// kayar (v0.9.483'te başlık/tooltip'in scopeTitle.ts'e toplanma sebebi).
//
// step: çağıran toplam görünümle aynı çözünürlüğü geçer (stepForPoints) —
// görünüm değiştirince bucket boyu değişip grafik zıplamasın. Rung'a snap
// edilmiş olması sunucu cache anahtarının kardinalitesini de sınırlar
// (v0.8.270).
export function useRootOpLatency(
  service: string,
  from: number,
  to: number,
  step: number,
  enabled: boolean,
) {
  return useQuery<SpanMetricResult | null>({
    // Anahtar TÜM girdileri taşır (service + pencere + adım). Pencere üstten
    // çözülmüş from/to — timeRangeToNs render'da çağrılmıyor (v0.5.184).
    queryKey: ['service-overview-entry-ops', service, from, to, step],
    queryFn: () => api.spanMetricTopN({
      agg: 'p95',
      field: 'duration_ms',
      groupBy: 'name',
      dsl: entryLatencyDSL(service),
      from, to, step,
    }),
    enabled: enabled && !!service && from > 0,
    // Sunucu cache TTL'i 30sn (span-metric:) — staleTime altına inmek
    // aynı sıcak girdiyi tekrar tekrar istemek olurdu.
    staleTime: 30_000,
  });
}
