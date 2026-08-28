#!/usr/bin/env bash
# entity-discovery-prod.sh — K8s entity katmanı keşfi, PROD SALT-OKUNUR probe paketi.
#
# Bu makineden prod'a erişim YOK (tek kube context: minikube; Thanos yalnız
# prod'da). Operatör bu betiği prod'a erişen bir yerden koşturur; çıktı dosyası
# docs/audit/entity-layer-discovery-2026-08-28.md'deki boş tablolara işlenir.
# Hiçbir şey YAZMAZ: yalnız SELECT + Thanos GET.
#
# Kullanım:
#   THANOS_URL=https://thanos-querier.example/ THANOS_TOKEN=... \
#   CH="clickhouse-client --host ch1 --user ro --password ... -d coremetry" \
#   scripts/probe/entity-discovery-prod.sh > entity-probe.txt
#
# Ortam:
#   (E bölümü clusterAllReplicas('{cluster}') kullanır — küme adı farklıysa düzenle)
#   THANOS_URL    Thanos Querier tabanı (Settings → Remote Cluster'daki URL)
#   THANOS_TOKEN  Bearer token (aynı ayardaki token; boşsa Authorization gönderilmez)
#   CH            clickhouse-client komutu (salt-okunur kullanıcı, -d coremetry)
#   WIN           span pencere (varsayılan 1 HOUR); ağır kümede 15 MINUTE yap
set -u
WIN="${WIN:-1 HOUR}"
CH="${CH:-clickhouse-client -d coremetry}"
hdr() { printf '\n\n===== %s =====\n' "$*"; }
q() { # PromQL anlık sorgu → JSON (jq varsa tablo)
  local expr="$1"
  if [ -n "${THANOS_TOKEN:-}" ]; then
    curl -sS -m 60 -H "Authorization: Bearer $THANOS_TOKEN" --data-urlencode "query=$expr" "$THANOS_URL/api/v1/query"
  else
    curl -sS -m 60 --data-urlencode "query=$expr" "$THANOS_URL/api/v1/query"
  fi
}
ql() { # etiket değerleri
  local label="$1" match="$2"
  if [ -n "${THANOS_TOKEN:-}" ]; then
    curl -sS -m 60 -H "Authorization: Bearer $THANOS_TOKEN" --data-urlencode "match[]=$match" "$THANOS_URL/api/v1/label/$label/values"
  else
    curl -sS -m 60 --data-urlencode "match[]=$match" "$THANOS_URL/api/v1/label/$label/values"
  fi
}
timed() { local s=$(date +%s.%N); "$@"; local e=$(date +%s.%N); printf '\n[süre %.2f s]\n' "$(echo "$e - $s" | bc)"; }

# ───────────────────────── A. Cluster kimlik zinciri ─────────────────────────
hdr "A2(b) Thanos: external label adayları ve değerleri (kube_pod_info üzerinden)"
for L in cluster cluster_id cluster_name prometheus prometheus_replica receive tenant_id; do
  printf '%s: ' "$L"; ql "$L" 'kube_pod_info' | head -c 600; echo
done

hdr "A2(b) Thanos: cluster başına seri sayısı (hangi etiket ayırıyor)"
q 'count by (cluster) (kube_pod_info)'                     | head -c 1500; echo
q 'count by (cluster_id) (kube_pod_info)'                  | head -c 1500; echo
q 'count by (prometheus) (kube_pod_info)'                  | head -c 1500; echo

hdr "A2(c) Span: cluster attribute değerleri (6-yol türetme kolonu + ham anahtarlar)"
$CH -q "
SELECT cluster, count() AS spans, uniqExact(service_name) AS services
FROM spans WHERE time >= now() - INTERVAL $WIN AND time < now()
GROUP BY cluster ORDER BY spans DESC LIMIT 50
FORMAT PrettyCompactMonoBlock SETTINGS max_execution_time=60"
$CH -q "
SELECT k, uniqExact(res_values[indexOf(res_keys, k)]) AS distinct_values, count() AS spans,
       arraySlice(groupUniqArray(10)(res_values[indexOf(res_keys, k)]), 1, 10) AS sample_values
FROM spans ARRAY JOIN res_keys AS k
WHERE time >= now() - INTERVAL $WIN AND time < now()
  AND k IN ('k8s.cluster.name','openshift.cluster.name','cluster','deployment.environment','deployment.environment.name')
GROUP BY k ORDER BY spans DESC
FORMAT PrettyCompactMonoBlock SETTINGS max_execution_time=60"

# ───────────────────────── B. Span tarafı ────────────────────────────────────
hdr "B5 Span resource anahtar doluluğu — filo geneli"
$CH -q "
SELECT k, count() AS spans, round(100 * count() / (SELECT count() FROM spans WHERE time >= now() - INTERVAL $WIN AND time < now()), 1) AS pct,
       uniqExact(service_name) AS services
FROM spans ARRAY JOIN res_keys AS k
WHERE time >= now() - INTERVAL $WIN AND time < now()
  AND (k LIKE 'k8s.%' OR k LIKE 'kubernetes.%' OR k LIKE 'openshift.%' OR k IN ('host.name','container.id','container.image.tag','service.instance.id','deployment.environment'))
GROUP BY k ORDER BY spans DESC
FORMAT PrettyCompactMonoBlock SETTINGS max_execution_time=60"

hdr "B5 Servis bazında doluluk (pod adı / pod uid / namespace / node / cluster) — en az dolu 40 servis"
$CH -q "
SELECT service_name, count() AS spans,
       round(100*countIf(has(res_keys,'k8s.pod.name'))/count(),1)       AS pod_name_pct,
       round(100*countIf(has(res_keys,'k8s.pod.uid'))/count(),1)        AS pod_uid_pct,
       round(100*countIf(has(res_keys,'k8s.namespace.name') OR has(res_keys,'kubernetes.namespace.name'))/count(),1) AS ns_pct,
       round(100*countIf(has(res_keys,'k8s.node.name'))/count(),1)      AS node_pct,
       round(100*countIf(cluster != '')/count(),1)                      AS cluster_pct,
       round(100*countIf(host_name != '')/count(),1)                    AS host_name_pct,
       any(res_values[indexOf(res_keys,'telemetry.sdk.language')])      AS sdk
FROM spans WHERE time >= now() - INTERVAL $WIN AND time < now()
GROUP BY service_name ORDER BY pod_name_pct ASC, spans DESC LIMIT 40
FORMAT PrettyCompactMonoBlock SETTINGS max_execution_time=90"

hdr "B5 Doluluk özeti (servis sayısı: tam / kısmi / hiç)"
$CH -q "
SELECT
  countIf(p = 100) AS services_full, countIf(p > 0 AND p < 100) AS services_partial, countIf(p = 0) AS services_none, count() AS services
FROM (SELECT service_name, round(100*countIf(has(res_keys,'k8s.pod.name'))/count(),1) AS p
      FROM spans WHERE time >= now() - INTERVAL $WIN AND time < now() GROUP BY service_name)
FORMAT PrettyCompactMonoBlock SETTINGS max_execution_time=90"

hdr "B6/B7 host.name ≟ k8s.pod.name; pod uid biçimi"
$CH -q "
SELECT
  countIf(has(res_keys,'k8s.pod.name') AND has(res_keys,'host.name')) AS both,
  countIf(has(res_keys,'k8s.pod.name') AND has(res_keys,'host.name')
          AND res_values[indexOf(res_keys,'k8s.pod.name')] = res_values[indexOf(res_keys,'host.name')]) AS equal,
  any(res_values[indexOf(res_keys,'k8s.pod.uid')]) AS uid_sample,
  any(res_values[indexOf(res_keys,'k8s.pod.name')]) AS pod_sample,
  any(res_values[indexOf(res_keys,'k8s.node.name')]) AS node_sample
FROM spans WHERE time >= now() - INTERVAL $WIN AND time < now()
FORMAT Vertical SETTINGS max_execution_time=60"

hdr "B(metrik) metric_points resource anahtarları (k8s.* var mı)"
$CH -q "
SELECT k, count() AS points FROM metric_points ARRAY JOIN res_keys AS k
WHERE time >= now() - INTERVAL 15 MINUTE AND time < now() AND (k LIKE 'k8s.%' OR k LIKE 'kubernetes.%' OR k LIKE 'openshift.%')
GROUP BY k ORDER BY points DESC LIMIT 30
FORMAT PrettyCompactMonoBlock SETTINGS max_execution_time=60"

# ───────────────────────── C. Thanos serileri ────────────────────────────────
hdr "C8 Seri doğrulama: örnek etiket setleri (her seri için 1 örnek)"
for S in kube_pod_info kube_pod_owner kube_replicaset_owner kube_pod_container_status_restarts_total kube_node_info kube_namespace_labels kube_deployment_labels kube_statefulset_labels kube_daemonset_labels kube_job_owner kube_pod_container_info; do
  printf '\n-- %s: ' "$S"; q "topk(1, $S)" | head -c 900; echo
done

hdr "C9 Owner zinciri kapsamı: pod→RS→Deployment çözülebilen pod oranı"
q 'count(kube_pod_owner{owner_kind="ReplicaSet"})' | head -c 300; echo
q 'count(kube_pod_owner{owner_kind="ReplicaSet"} * on (namespace, owner_name, cluster) group_left(owner_kind_rs, owner_name_rs) label_replace(label_replace(kube_replicaset_owner, "owner_kind_rs", "$1", "owner_kind", "(.*)"), "owner_name_rs", "$1", "owner_name", "(.*)") )' | head -c 300; echo
q 'count by (owner_kind) (kube_pod_owner)' | head -c 800; echo
q 'count by (owner_kind) (kube_replicaset_owner)' | head -c 800; echo
q 'count(kube_pod_info) - count(kube_pod_info{node!=""})' | head -c 300; echo   # node ataması eksik pod

hdr "C11 Erişim kapsamı: kaç cluster görünüyor (kube_node_info)"
q 'count by (cluster) (kube_node_info)' | head -c 1500; echo

# ───────────────────────── D. Ölçek ──────────────────────────────────────────
hdr "D12 Cluster / namespace / pod sayıları"
q 'count(count by (cluster) (kube_pod_info))'             | head -c 300; echo
q 'count(count by (cluster, namespace) (kube_pod_info))'  | head -c 300; echo
q 'count(kube_pod_info)'                                  | head -c 300; echo
q 'count by (cluster) (kube_pod_info)'                    | head -c 3000; echo

hdr "D13 Günlük pod devri (24h'de görülen farklı pod − şu anki pod)"
q 'count(count_over_time(kube_pod_info[24h]))'            | head -c 300; echo
q 'count(kube_pod_created > (time() - 86400))'            | head -c 300; echo

hdr "D14 Fan-out: tüm cluster'ları kapsayan kube_pod_info sorgusunun süresi + seri sayısı"
timed q 'kube_pod_info' | tail -c 200; echo
timed q 'count(kube_pod_info)' | tail -c 300; echo
timed q 'kube_pod_info{cluster=~".+"}' | tail -c 200; echo

# ───────────────────────── E. Prod CH şema durumu ────────────────────────────
hdr "E Prod spans_local: chstore'un MATERIALIZED kolonları indi mi (cluster / terfi kolonları / idx)"
$CH -q "
SELECT hostName() AS host, name, type, default_kind
FROM clusterAllReplicas('{cluster}', system.columns)
WHERE database = currentDatabase() AND table = 'spans_local'
  AND name IN ('cluster','attr_channel_code','attr_function_code','host_name','deploy_env')
ORDER BY host, name FORMAT PrettyCompactMonoBlock SETTINGS max_execution_time=10" 2>&1 | head -40
$CH -q "SELECT name, type, expr FROM system.data_skipping_indices WHERE table = 'spans_local' FORMAT PrettyCompactMonoBlock" 2>&1 | head -20
$CH -q "SELECT cluster, count() AS hosts FROM system.clusters GROUP BY cluster FORMAT PrettyCompactMonoBlock" 2>&1 | head -10

echo; echo "=== BİTTİ ==="
