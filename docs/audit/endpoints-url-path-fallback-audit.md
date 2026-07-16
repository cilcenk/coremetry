# Audit — Endpoints `url.path` fallback'i pratikte hiç çalışmıyor

2026-07-16 · HEAD `85dac6b4` (v0.8.555) · Salt-okunur inceleme + canlı CH
ölçümü (lokal minikube, 7g pencere, 11.8M span). Kod:
`internal/chstore/endpoints.go`, `internal/otlp/convert.go`.

## Kısa hüküm

1. **Kök neden teşhisi DOĞRU.** `pathExpr`'in 3-4. katmanları (url.path /
   http.target attr) dokümante edilmiş ama ölü: `kind NOT IN
   ('client','producer')` filtresi coalesce'ten ÖNCE uygulanıyor ve
   url.path pratikte client span'lerde yaşıyor — o satırlar coalesce'e
   hiç ulaşmıyor.
2. **Brief'in bahsetmediği ÜÇÜNCÜ filtre sitesi var:**
   `endpointStatusSidecar` (`endpoints.go:484`, filtre `:531`). Yalnız
   `:672` gevşetilirse yeni satırlar status kırılımı alamaz — dar
   düzeltmenin kapsamına sidecar da girmeli.
3. **Lokalde `url.path` HİÇ yok (0 / 11.797.926 span)** → ölçüm (a) ve
   (b) lokalde koşulamadı; prod'a hazır sorgular §2'de. Cluster-badge
   vakasının aynısı: özellik lokalde doğrulanamaz, yalnız
   regresyonsuzluk kanıtlanır.
4. **Regresyonsuzluk KANITLI:** dar düzeltme simülasyonu lokalde
   baseline'ı değiştirmiyor (313 = 313) — gevşetilen satırların
   pathExpr'i lokalde boş kaldığından `path != ''` onları yine eliyor.
5. **peer_service zemini sağlam:** client span'lerde %99.4 dolu
   (6.506.868 / 6.545.161). peer_service-öncelikli tasarımın eklediği
   grup sayısı ölçüldü: **+191** (baseline 313'e karşı, LowCardinality,
   sınırlı). Ham url.path senaryosu lokalde ölçülemedi (attr yok) —
   kardinalite riski ancak prod'da nicelenir.
6. **`producer` gevşetilmesi ÖNERİLMİYOR** — §4: messaging span'lerinde
   http_route kavramı yok ve o trafik zaten kendi sayfasında (M1,
   v0.8.364); Endpoints'e sızdırmak çift listeleme olur.
7. **Brief'in referans verdiği `endpoints-client-calls-audit.md` MEVCUT
   DEĞİL** — "Faz 2 zaten planlı" öncülü doğrulanamadı. MV genişletme
   kararı (url.path/peer_service boyutu) bu dokümanın Açık Soru 2'sinde.
8. İnce ingest nüansı: `convert.go:108` `http.target`'ı zaten ingest'te
   `http_route` kolonuna katlıyor — coalesce'in 4. katmanı yalnız o
   davranıştan önceki satırlar için yaşıyor.

---

## 1. Kod doğrulaması — brief vs HEAD

| Brief iddiası | Sonuç |
|---|---|
| Fallback dokümantasyonu `:574-581`, pathExpr `:601-607` | ✓ (4 katman: http_route → http.route attr → url.path → http.target) |
| `getEndpointsRaw` `:672` kind filtresi coalesce'ten önce | ✓ birebir |
| `GetEndpointsMV` `:371` aynı filtre | ✓ + MV `http_route != ''` de istiyor — MV yolu url.path'i YAPISAL olarak hiç göremez (group-by'ında yok) |
| "double-counting" gerekçesi `:583-585` | ✓ — `outbound client / producer spans land under the callee's row` |
| `peer_service` ingest'te `peer.service`'ten doluyor (`convert.go:112`) | ✓ birebir, yeni ingest değişikliği gerekmiyor |
| — | ✗ eksik: **3. filtre sitesi** `endpointStatusSidecar` `:531` |
| "endpoints-client-calls-audit kapsamında Faz 2 planlı" | ✗ **doküman yok** — bu audit o kararın ilk kaydı olacak |

Tarihsel bağlam (`:587-595`): kind filtresi v0.5.386'da bir kez zaten
gevşetildi (`kind IN ('server','consumer')` → `NOT IN ('client','producer')`)
ve gerekçesi bugünkü şikâyetin birebir aynısıydı: "rows looked sparse vs
reality". Bu düzeltme o hattın devamı.

## 2. Canlı ölçümler (lokal minikube · 7g · 11.797.926 span)

### (a) Doğrulama sorgusu — url.path'li ama route'suz satırlar

**LOKALDE ÖLÇÜLEMEDİ: 0 satır.** `url.path` attr'ı lokal veride hiç yok
(0 / 11.8M). Demo jeneratörleri semconv'un bu şeklini üretmiyor. Prod'da
koşulacak sorgu (brief'tekiyle aynı):

```sql
SELECT kind, count() AS n FROM spans
WHERE time >= now() - INTERVAL 7 DAY
  AND nullIf(http_route, '') IS NULL
  AND nullIf(attr_values[indexOf(attr_keys, 'http.route')], '') IS NULL
  AND nullIf(attr_values[indexOf(attr_keys, 'url.path')], '') IS NOT NULL
GROUP BY kind ORDER BY n DESC
SETTINGS max_execution_time = 60
```

### (b) O satırlarda peer_service doluluğu

**LOKALDE ÖLÇÜLEMEDİ** (popülasyon boş). Prod sorgusu:

```sql
SELECT kind, countIf(peer_service != '') AS ps_dolu,
       countIf(peer_service = '') AS ps_bos, count() AS n
FROM spans
WHERE time >= now() - INTERVAL 7 DAY
  AND nullIf(http_route, '') IS NULL
  AND nullIf(attr_values[indexOf(attr_keys, 'http.route')], '') IS NULL
  AND nullIf(attr_values[indexOf(attr_keys, 'url.path')], '') IS NOT NULL
GROUP BY kind ORDER BY n DESC
SETTINGS max_execution_time = 60
```

Genel doluluk (lokal, tüm span'ler — tasarımın zemini):

| kind | peer_service dolu | toplam | oran |
|---|---|---|---|
| client | 6.505.714 | 6.545.161 | **%99.4** |
| producer | 344.834 | 344.834 | %100 |
| consumer | 366.878 | 425.632 | %86 |
| server | 614.171 | 3.979.845 | %15 |
| internal | 0 | 502.454 | %0 |

### (c) Grup sayısı projeksiyonları

| Senaryo | Değer | Not |
|---|---|---|
| **Baseline** (mevcut filtre + pathExpr) | **313 endpoint** | lokal, 7g |
| Dar düzeltme (kind gevşet, gruplama aynı) | **313** | **lokal no-op — regresyonsuzluk kanıtı**; etki yalnız url.path/http.target taşıyan prod satırlarında |
| **(c1)** peer_service-öncelikli: eklenen grup | **+191** | `uniqExact(service, peer_service)`, route'suz client satırlarından; LowCardinality → sınırlı |
| **(c2)** ham url.path'e düşülseydi | **lokalde 0** | attr yok; kardinalite riski prod'da nicelenmeli — sorgu aşağıda |

Gevşetilmiş filtreye girecek popülasyon (lokal): **6.546.315 route'suz
client span** — %99.4'ü peer_service'li, yalnız **39.447**'si (%0.6)
fallback yoluna kalıyor. Yani peer_service-öncelikli tasarımda ham-path
fallback'i marjinal bir kuyruk.

(c2) prod sorgusu (opSig-normalize edilmiş VE ham, yan yana):

```sql
SELECT
  uniqExact(service_name, attr_values[indexOf(attr_keys,'url.path')]) AS ham_grup,
  uniqExact(service_name, replaceRegexpAll(replaceRegexpAll(replaceRegexpAll(
    attr_values[indexOf(attr_keys,'url.path')],
    '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}', ':id'),
    '/[0-9a-f]{16,}', '/:id'), '/[0-9]+', '/:id')) AS normalize_grup
FROM spans
WHERE time >= now() - INTERVAL 7 DAY AND kind = 'client'
  AND nullIf(http_route,'') IS NULL
  AND nullIf(attr_values[indexOf(attr_keys,'http.route')],'') IS NULL
  AND nullIf(attr_values[indexOf(attr_keys,'url.path')],'') IS NOT NULL
SETTINGS max_execution_time = 60
```

## 3. Dar düzeltme — kapsam

Brief'teki predicate birebir (double-counting gerekçesi bozulmuyor:
route'LU client span'ler — yani callee tarafında zaten sayılanlar —
dışarıda kalmaya devam ediyor):

```sql
AND (
  kind NOT IN ('client', 'producer')
  OR (
    kind = 'client'
    AND nullIf(http_route, '') IS NULL
    AND nullIf(attr_values[indexOf(attr_keys, 'http.route')], '') IS NULL
  )
)
```

| Site | Değişiyor mu | Neden |
|---|---|---|
| `getEndpointsRaw` `:672` | ✅ | asıl düzeltme |
| `endpointStatusSidecar` `:531` | ✅ **brief'te yoktu** | filtre uyuşmazsa yeni satırlar status kırılımı alamaz |
| `GetEndpointsMV` `:371` | ❌ | MV group-by'ında ne url.path ne peer_service var — Faz 2 (Açık Soru 2). MV `http_route != ''` istediği için gevşetme orada zaten anlamsız |

Not: raw yol yalnız MV kapsam dışı durumlarda koşuyor (env filtresi,
`errEndpointsMVEnv` `:338`) — yani dar düzeltmenin prod'daki görünürlüğü
raw yolun ne sıklıkta seçildiğine de bağlı. Bu, Faz 2'nin (MV boyutu)
neden gerekli olduğunun ikinci yarısı.

## 4. `producer` — aynı mantık GEÇERLİ DEĞİL

- Messaging span'lerinde `http_route`/`url.path` kavramı yok; lokalde
  344.834 producer span'in TAMAMI route'suz ve peer_service'li.
- O trafiğin evi zaten `/messaging` (M1 v0.8.364: produce/consume split,
  p50/p95, destination drawer). Endpoints'e sızdırmak aynı trafiği iki
  sayfada listeler — `feedback-tables-over-cards` ailesinden bir UX
  regresyonu olur.
- **Öneri: producer filtresi olduğu gibi kalsın.** Gevşetme yalnız
  `kind = 'client'` için.

## 5. peer_service-öncelikli tasarım — değerlendirme

Brief'in tasarımı ölçümle destekleniyor ama kabulü operatörde:

- **Lehte:** %99.4 doluluk (client) · +191 grup = sınırlı kardinalite ·
  LowCardinality kolon, GROUP BY ucuz · ingest değişikliği yok ·
  url.path yalnız `anyHeavy()` örneği olarak taşınır, grup anahtarına
  girmez → kardinalite patlaması yapısal olarak imkânsız.
- **Aleyhte / dikkat:** "endpoint" kavramı genişliyor — satır artık bir
  route değil, bir HEDEF SERVİS olabilir. UI'da ayrım gerekir (ör. kind
  chip'i "→ outbound" veya ayrı bir sekme). Fallback yolu (%0.6,
  peer_service'siz) opSigWrap-normalize edilmiş url.path'e düşer —
  prod'da (c2) koşulmadan bu kuyruğun kardinalitesi bilinmiyor.
- Faz sırası önerisi: **Faz 1** = dar düzeltme (raw + sidecar, bu
  doküman) → **Faz 2** = MV'ye boyut (peer_service ± normalize path) +
  peer_service-öncelikli satır tipi. Faz 2, MV DROP+RECREATE veya
  in-place inner-ALTER ister (`reference-ch-inplace-mv-column-add`) —
  kendi audit'ini hak ediyor.

## 6. Doğrulama planı

| Adım | Nerede |
|---|---|
| (a)/(b)/(c2) sorguları | **PROD** — lokalde koşulamaz; sonuçlar bu dokümana işlenir |
| Dar düzeltme regresyonsuzluğu | lokal: baseline 313 = 313 (✅ ölçüldü, §2c) |
| Regresyon testi | `endpoints.go` SQL-shape testi: gevşetilmiş predicate'in üç özelliği — (1) route'lu client hâlâ dışarıda, (2) producer hâlâ dışarıda, (3) route'suz client içeride. Saf, tablo-driven |
| Gate | `go build` → `go test` → `tsc` (fe diff yok) → `make audit` (CHECK 6: yeni SQL LIMIT+max_execution_time korumalı kalmalı) |
| Canlı doğrulama | prod'a inince /endpoints'te url.path'li client satırlarının belirmesi; lokalde GÖRÜNMEZ (url.path yok) |

## 7. Açık sorular — operatör

1. **peer_service-öncelikli tasarım kabul mü?** (§5 — Faz 2'nin şekli
   buna bağlı. Dar düzeltme bu karardan bağımsız ship'lenebilir.)
2. **MV genişletmesi (Faz 2) ayrı audit olarak açılsın mı?** Brief'in
   referans verdiği `endpoints-client-calls-audit.md` mevcut değil —
   istenirse bu dokümanın §5'i onun tohumu olur.
3. **(a)/(b)/(c2) prod sorgularını sen mi koşacaksın, bağlantı bilgisi
   mi vereceksin?** Sonuçlar gelmeden Faz 2 kararı verilmemeli;
   dar düzeltme için gerekmiyor (regresyonsuzluk lokalde kanıtlı).

## Öneri

**Faz 1'i (dar düzeltme: `:672` + `:531`, producer hariç) onayla** —
lokalde kanıtlanmış no-op, prod'da yalnız bugün kaybolan satırları
ekler, double-counting gerekçesi bozulmaz. Faz 2 (MV boyutu +
peer_service-öncelikli model) prod ölçümleri gelene kadar beklesin.

**Onay bekliyor — implementasyona geçilmedi.**
