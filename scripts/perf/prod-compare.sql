-- prod-compare.sql — perfcheck nokta adları = self-telemetry span adları.
-- Coremetry kendi otelhttp span'larını kendi spans tablosuna yazar
-- (servis coremetry-monolithic|api, %10 örnekleme); spanmetrics_1m
-- rollup'ı uç başına dakikalık p95 taşır. Bu sorgu prod'daki gerçek
-- p50/p95'i perfcheck çıktısıyla AYNI ADLA döker; iki tablo yan yana
-- okunur. Yalnız SELECT — prod'a yazma yok. -d coremetry ile koş.
--
-- Not: prod'da spanmetrics_1m ORDER BY (service_name, name, kind,
-- status_code, http_route, time_bucket); pencere 24 saat.
SELECT
    name                                                                AS point,
    countMerge(calls_state)                                             AS calls,
    round(quantilesTDigestMerge(0.5)(duration_q_state)[1] / 1e6, 1)     AS p50_ms,
    round(quantilesTDigestMerge(0.95)(duration_q_state)[1] / 1e6, 1)    AS p95_ms,
    round(countIfMerge(error_state) / greatest(countMerge(calls_state), 1) * 100, 2) AS err_pct
FROM spanmetrics_1m
WHERE service_name LIKE 'coremetry%'
  AND time_bucket > now() - INTERVAL 24 HOUR
  AND name IN ('GET /api/traces', 'GET /api/traces/:id', 'GET /api/servicegraph',
               'GET /api/service-map', 'POST /api/dashboards/data', 'GET /api/problems',
               'GET /api/exception-groups', 'GET /api/services')
GROUP BY name
ORDER BY calls DESC
SETTINGS max_execution_time = 10;
