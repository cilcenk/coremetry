# Audit — Root span cluster etiketi (`openshift.cluster.name`)

**Tarih:** 2026-07-15 · **HEAD:** `199a6dcf` (haritalama raporlarının bir kısmı `d180966` üzerinde koştu; sorgulanan dosyalarda diff yok) · **Durum:** salt okuma, hiçbir dosya değiştirilmedi

---

## Kısa hüküm

- **ANA SORU CEVAPLI: attribute ZATEN sorgulanabilir.** `spans.cluster` MATERIALIZED kolonu `openshift.cluster.name`'i çözüyor (`internal/chstore/repo.go:294-303`, `store.go:547`). Yeni ingest alanı GEREKMİYOR.
- Ama **frontend'in kolona bile ihtiyacı yok**: `resourceAttributes` map'i `/api/traces/{id}` payload'ında zaten gidiyor (`repo.go:3118` → `model.go:248` → `types.ts:1315`) ve frontend'de aynı coalesce hazır: `frontend/src/lib/otel/semconv.ts:56`.
- **Sonuç: iş SAF FRONTEND.** Backend/CH/API değişikliği yok, yeni endpoint yok, `registerXxxRoutes` sorusu MOOT.
- Tek dokunuş: `frontend/src/components/TraceWaterfall.tsx:641` — `wf-cat` chip'inin hemen ardına koşullu `<span>` + `globals.css`'e bir class.
- **Root tespiti frontend'de** — backend hiçbir yerde root işaretlemiyor (`GetTrace` sadece `ORDER BY time ASC`). Etiket için `!s.parentSpanId` kullanılmalı, `depth === 0` DEĞİL (orphan'ları da root sayar).
- **"Sessizce gizle" bedava gelir**: `{cluster && <span…>}` — `first()` bulamazsa `undefined`, chip render edilmez.
- **⚠️ En önemli pratik kısıt: lokalde `openshift.cluster.name` YOK — 0/11.367.918 span.** Lokal `k8s.cluster.name` kullanıyor (10.778.793 span). Coalesce k8s'i ÖNCE denediği için `openshift.cluster.name` dalı lokalde ÖLÜ — o dal lokalde test edilemez.
- İyi haber: **her iki görünür dal da lokalde test edilebilir** — dolu (`api-gateway` → `prod-eu-west`) ve boş (`coremetry-monolithic`, cluster `''`, 40.751 span) fixture'ları aynı cluster'da mevcut.

---

## 1. Ana soru: attribute span'e ulaşıyor mu?

**EVET — iki ayrı yoldan, ikisi de canlı doğrulandı.**

### 1.1 Ingest: jenerik resource attr olarak ham geçiyor

`internal/otlp/` altında `openshift` / `cluster` kelimesi **0 kez** geçiyor. Attribute hiçbir özel muamele görmüyor:

```
OTLP ResourceSpans.Resource.Attributes
  → attrsToArrays()          convert.go:48   [filtre/cap/allow-list YOK]
  → resK, resV               convert.go:56   [resource başına 1×, span'lere paylaşılır]
  → Span.ResKeys/ResValues   convert.go:150
  → spansInsertColumns       repo.go:85 / :101   [res_keys, res_values listede ✅]
  → CH spans.res_keys/res_values              store.go:542-543
```

`attrsToArrays` (`convert.go:434-445`) koşulsuz döngü — hiçbir attr düşmez. "critical 5" hiyerarşisi var (`otel-conventions/SKILL.md:67-78`) ama `openshift.cluster.name` onun parçası değil; skill açıkça **"don't enforce one name at ingest — coalesce at read time"** diyor ve kod bunu birebir uyguluyor.

### 1.2 CH: `cluster` MATERIALIZED kolonu attribute'u ÇÖZÜYOR

**Bu, sorunun cevabı.** `internal/chstore/repo.go:294-303`:

```go
const clusterDeriveExpr = `coalesce(
	nullIf(res_values[indexOf(res_keys, 'k8s.cluster.name')], ''),
	nullIf(res_values[indexOf(res_keys, 'openshift.cluster.name')], ''),
	nullIf(res_values[indexOf(res_keys, 'cluster')], ''),
	nullIf(attr_values[indexOf(attr_keys, 'k8s.cluster.name')], ''),
	nullIf(attr_values[indexOf(attr_keys, 'openshift.cluster.name')], ''),
	nullIf(attr_values[indexOf(attr_keys, 'cluster')], ''),
	''
)`
```

Canlı `SHOW CREATE TABLE coremetry.spans` çıktısı **birebir bu ifadeyi** doğruladı. Kolon `store.go:547`'de DDL'e gömülü, `store.go:1482`'de idempotent ALTER ile de geliyor. Shipped: **v0.8.132** (`76681855`, "promote cluster derive to MATERIALIZED column") — v0.8.185 değil; o sürüm probe'un panic fix'iydi (`store.go:3046`).

### 1.3 API: resource attr'lar zaten frontend'e gidiyor

`GetTrace` SQL'i (`repo.go:3082-3091`) `res_keys, res_values` seçiyor → `repo.go:3118` `arraysToMap()` → `model.go:248` `json:"resourceAttributes"` → `types.ts:1315` `resourceAttributes: Record<string, string>`.

`cluster` kolonu payload'da **YOK** (`repo.go:3054-3125` içinde 0 eşleşme) — ama gerek de yok, çünkü kolonun türetildiği ham diziler zaten gidiyor.

### 1.4 Frontend: coalesce zaten yazılmış

`frontend/src/lib/otel/semconv.ts:56`:
```ts
cluster: first(a, 'k8s.cluster.name', 'openshift.cluster.name', 'cluster'),
```

CH'deki 6-permütasyonun resource-yarısıyla **aynı öncelik sırası**. Yani frontend zaten bu attribute'u okuyabilecek durumda — sadece hiçbir yerde render etmiyor.

**Tek cümlelik hüküm:** İş "yeni ingest alanı" değil; **"zaten var olan veriyi waterfall satırında göstermek."**

---

## 2. Etkilenen dosyalar

| Katman | Dosya:satır | Değişecek mi |
|---|---|---|
| Ingest | `internal/otlp/convert.go:48, :150, :434-445` | ❌ hayır |
| CH şema | `internal/chstore/store.go:547`, `repo.go:294-303` | ❌ hayır |
| CH sorgu | `internal/chstore/repo.go:3082-3091` (`GetTrace`) | ❌ hayır |
| Model | `internal/chstore/model.go:234-257` (`SpanRow`) | ❌ hayır |
| API | `internal/api/api.go:557` (`GET /api/traces/{id}`), `:3512` (`getTrace`) | ❌ hayır |
| TS tip | `frontend/src/lib/types.ts:1301-1324` (`SpanRow`) | ❌ hayır (`resourceAttributes` yeterli) |
| Semconv | `frontend/src/lib/otel/semconv.ts:56` | ❌ hayır (coalesce hazır) |
| **Render** | **`frontend/src/components/TraceWaterfall.tsx:636-641`** | ✅ **EVET** |
| **CSS** | **`frontend/src/styles/globals.css:738-755` sonrası** | ✅ **EVET** |

Toplam: **2 dosya.**

### 2.1 Protokol tag'lerinin render yeri — bulundu

`TraceWaterfall.tsx:77-86` sınıflandırma:
```tsx
function categoryOf(s: SpanRow): SpanCategory | null {
  const a = s.attributes ?? {};
  if (a['db.system'])        return { tag: 'DB',   color: 'var(--warn)' };
  if (a['messaging.system']) return { tag: 'MQ',   color: 'var(--teal)' };
  if (a['rpc.system'])       return { tag: 'RPC',  color: 'var(--purple)' };
  if (a['http.method'] || a['http.request.method'])
    return { tag: 'HTTP', color: 'var(--accent)' };
  return null;
}
```

`TraceWaterfall.tsx:636-641` render (aranan yer):
```tsx
{cat && (
  <span className="wf-cat" title={`Category: ${cat.tag}`}
        style={{ color: cat.color, borderColor: cat.color }}>
    {cat.tag}
  </span>
)}
```

Satır içi komşuları (`.wf-row-name-inner` flex kabı, :615): `wf-svc` → **`wf-cat`** → `wf-name` → `wf-group` → `wf-err-dot` → log chip → `wf-pct`.

Ayrı bir tag/badge component'i **YOK** — inline `<span>` + CSS class. Emsal olarak alınacak desen `wf-group` (`TraceWaterfall.tsx:649-660` + `globals.css:723-736`): koşullu, class-based, theme-token'lı outlined pill, inline style yok. (Log chip `:664-683` inline style kullanıyor — CLAUDE.md'ye aykırı, emsal alınmamalı.)

### 2.2 Kapsam uyarısı

`TraceWaterfall` üç sayfada paylaşılıyor: `pages/Trace.tsx:485`, `pages/PublicTrace.tsx:149`, `pages/TraceCompare.tsx:234`. Tek dokunuş üçünü birden etkiler. Sadece `/trace` isteniyorsa opsiyonel `showRootCluster` prop'u gerekir → **Açık soru #1**.

---

## 3. Önerilen değişiklik planı

**Adım 1 — `frontend/src/components/TraceWaterfall.tsx`**

Import: `semconv.ts`'in `cluster` resolver'ını kullan (yeniden yazma — `semconv.ts:56` tek doğruluk kaynağı).

Satır kapsamında (`:518`) `s` zaten var. `:521` `const cat = categoryOf(s)` yanına:
```tsx
const rootCluster = !s.parentSpanId ? clusterOf(s.resourceAttributes) : undefined;
```

`:641` — `wf-cat` bloğunun hemen ardına:
```tsx
{rootCluster && (
  <span className="wf-cluster" title={`Cluster: ${rootCluster}`}>
    {rootCluster}
  </span>
)}
```

**Adım 2 — `frontend/src/styles/globals.css`** (`:755` sonrası)

`.wf-group` (`:723-736`) deseninin kopyası: outlined pill, `color-mix(in srgb, var(--accent) 10%, transparent)` bg + `35%` border, `flex-shrink: 0` (**şart** — `.wf-name` `flex: 1`, aksi halde ezer), `margin-right: 8px`, token-only renk (üç tema: dark/light/redhat otomatik gelir).

**Adım 3 — gate + release** → §6.

**Yapılmayacaklar (bilinçli):**
- `spans` SELECT'ine `cluster` eklemek — gereksiz; eklenseydi **çıplak `cluster` YAZILAMAZDI** (external Distributed prod'da code 47), `s.clusterExpr()` (`repo.go:325-331`) zorunlu olurdu. Kaçınıldı.
- Yeni endpoint / `registerXxxRoutes` — gerek yok. (Not: trace çekirdek rotaları `api.go:532-569`'da inline duruyor, pattern'de değil — bilinen borç, bu iş kapsamı dışı.)
- `SpanRow`'a `cluster` alanı — `resourceAttributes` yeterli.

---

## 4. Root span tespiti — FRONTEND

**Kanıt:** Backend hiçbir yerde root işaretlemiyor. `GetTrace` (`repo.go:3054`) `ORDER BY time ASC` döner, `parent_id` yalnızca SELECT/Scan'de geçer. Handler (`api.go:3512`) `map[string]any{"traceId","spans","source"}` döner — root alanı yok.

Frontend'de `!s.parentSpanId` deseni 4 yerde tekrarlanıyor: `Trace.tsx:295`, `PublicTrace.tsx:90`, `TracePeekDrawer.tsx:65`, `TraceHonesty.tsx:44`.

**⚠️ Kritik ayrım — `depth === 0` KULLANMA.** `TraceWaterfall.tsx:238-245` tree kurulumu:
```tsx
if (s.parentSpanId && map.has(s.parentSpanId)) children.get(s.parentSpanId)!.push(s);
else roots.push(s);
```
Buradaki `roots` **orphan'ları da içerir** (parent'ı var ama trace'te yok — üst servis örneklenmemiş). `TraceHonesty.tsx:35-36` ikisini açıkça ayırıyor, yani orphan vakası gerçek ve biliniyor. Operatör "parent_span_id'si olmayan" dedi → **`!s.parentSpanId`**. `depth === 0` kullanılırsa parçalı trace'lerde orphan'lar da cluster etiketi alır — yanlış.

**Not (kapsam dışı, bilinen tutarsızlık):** `Trace.tsx:295` (`find(!parentSpanId) ?? spans[0]`) ile `Trace.tsx:157-159` (orphan-aware) arasında fark var. Bu işi etkilemiyor (waterfall kendi tree'sini kuruyor) ama ayrı bir kalem olabilir → **Açık soru #4**.

---

## 5. "Sessizce gizle"

Üç kat, hepsi bedava:

1. `semconv.ts:56` `first()` — hiçbir key yoksa `undefined` döner.
2. `!s.parentSpanId` — root değilse hesaplama bile yapılmaz.
3. `{rootCluster && <span…>}` — `undefined` / `''` → chip DOM'a girmez.

`''` de falsy olduğu için CH'nin `coalesce(…, '')` boş dalı da otomatik gizlenir. **"unknown" / boş chip / placeholder YOK.** Emsal: `wf-cat` zaten aynı davranışta (`categoryOf` → `null` → render yok).

---

## 6. Doğrulama planı

| Gate | Komut |
|---|---|
| TS | `cd frontend && npx tsc --noEmit` |
| Lint | `cd frontend && npx eslint src/components/TraceWaterfall.tsx` |
| Go | `go build ./...` (değişiklik yok — sanity) |
| Test | `go test ./...` (değişiklik yok — sanity) |
| Audit | `make audit` — 🔴 tag'i bloklar |

**Regresyon testi:** CLAUDE.md bug-fix için zorunlu kılıyor; bu bir **feature**, backend değişikliği yok → Go testi yok. Eğer `clusterOf` helper'ı `semconv.ts`'e yeni saf fonksiyon olarak eklenecekse vitest tablo testi eklenmeli (3 key önceliği + yok → `undefined`) → **Açık soru #2**.

### 6.1 Manuel — lokalde ne test EDİLEBİLİR

Canlı ölçüm (son 24h, `spans`):

| Dal | Fixture | Kanıt |
|---|---|---|
| **Etiket GÖRÜNÜR** | `api-gateway` root span | `trace_id 2570760007bb2a0f29bbf20eb36769ac` → cluster `prod-eu-west` |
| **Etiket GİZLİ (boş)** | `coremetry-monolithic` (40.751 span), `coremetry-frontend` (3.601) | `k8s.cluster.name` set etmiyor → cluster `''` |
| **Etiket GİZLİ (non-root)** | Yukarıdaki trace'in child span'leri | `parentSpanId` dolu |

Root span cluster dağılımı (24h): `prod-eu-west 68910`, `(boş) 42318`, `prod-ap-south 18828`, `prod-us-east 7984`, `prod-eu-central 3098`, `dr-eu-west 3059`.

**Yani "sessizce gizle" davranışı lokalde canlı fixture ile doğrulanabilir — ayrı ortam gerekmez.** Coremetry'nin kendi self-telemetry'si boş dalı besliyor.

### 6.2 ⚠️ Lokalde test EDİLEMEYEN — planın en önemli kısıtı

**`openshift.cluster.name` lokalde HİÇ YOK.**

```
res_openshift  attr_openshift  total_all_time
0              0               11367918
```

`%cluster%` / `%openshift%` içeren TÜM res_key'ler (all-time): tek satır → `k8s.cluster.name 10778793`. res_keys envanteri (son 1h) 18 satırda bitti (LIMIT 25'e ulaşmadı) — lokalde başka resource attribute yok.

Kaynak: demo generator `k8s.cluster.name` yazıyor (`cmd/demo/main.go:454, :1278, :1752` → `clusterFor`, `:163`; `docker-compose.yml:312, :381` `OTEL_RESOURCE_ATTRIBUTES`).

**Sonucu:**
1. Operatörün istediği spesifik anahtar (`openshift.cluster.name`) lokalde ÜRETİLEMEZ → o key'in çözüldüğü hâl lokalde doğrulanamaz.
2. Daha kötüsü: `k8s.cluster.name` coalesce'te **1. sırada**. Prod OpenShift'te ikisi de set ediliyorsa `openshift.cluster.name` dalı **prod'da da hiç çalışmaz** — etiket `k8s.cluster.name` değerini gösterir. Operatör "değer `openshift.cluster.name`'den" dediyse ve prod'da ikisi birden geliyorsa, **görünen değer yanlış olur.** → **Açık soru #3, bu iş için en riskli belirsizlik.**
3. Bu davranış CH'de de, frontend'de de aynı — yani `/services` cluster filtresi de aynı önceliği kullanıyor; tutarlıyız, ama tutarlı bir şekilde operatörün istediğinden farklı olabiliriz.

**Yapay test yolu (öneri):** `docker-compose.yml`'deki java/jboss demo'ya geçici `OTEL_RESOURCE_ATTRIBUTES=openshift.cluster.name=ocp-test` ekle — **ve `k8s.cluster.name`'i O SERVİSTEN ÇIKAR**, aksi halde dal yine ölü kalır. Sadece manuel doğrulama için, commit edilmez.

**DOĞRULANAMADI:** prod'da `openshift.cluster.name`'in tek başına mı yoksa `k8s.cluster.name` ile birlikte mi geldiği. Bu cluster'dan ölçülemez.

---

## 7. Rapor çelişkileri — hakemlik

| Çelişki | Hüküm |
|---|---|
| İşin adı "yeni ingest alanı taşıma" mı? | **HAYIR.** Kolon v0.8.132'den beri var, resource attr'lar zaten payload'da. Saf frontend işi. Kanıt: `repo.go:294-303` + canlı SHOW CREATE + `types.ts:1315`. |
| Kolon eklenişi v0.8.185 mi? | **HAYIR — v0.8.132** (`76681855`). v0.8.185 probe panic fix'i (`store.go:3046`). |
| Operatör "openshift.cluster.name genelde resource-level, span-level değil" | **DOĞRU ve önemsiz** — `clusterDeriveExpr` her ikisini de tarıyor, resource-first (`repo.go:296` res, `:299` attr). |
| `openshift.cluster.name` lokalde var mı? | **YOK — 0/11.367.918.** Ölçüldü. |
| `cluster` her yerde boş mu? | **HAYIR — 5 cluster, ~1.4M satır dolu.** Ölçüldü. |

**DOĞRULANAMADI (kapsam dışı, ayrı kalemler):**
- `repo.go:314` `clusterColExpr` fallback'inin ölü kod olup olmadığı (CH `ADD COLUMN` default-expression semantiği kod okuyarak kapatılamaz; `EXPLAIN` / `query_log.read_columns` ölçümü gerekir). Bu işi etkilemez — frontend kolonu kullanmıyor.
- `internal/logstore/clickhouse.go:118-122` `chLogsClusterExpr` **3-key, sadece res** — spans'in 6-key'i ile asimetrik. Bilinçli mi drift mi çıkaramadım. Bu işi etkilemez.
- `api.go:3538` `source: "tempo"` dalında `SpanRow` alan paritesi — `internal/tempo/` okunmadı. Tempo dalı `resourceAttributes` doldurmuyorsa etiket o dalda sessizce gizlenir (istenen davranışa uyar, ama farkında olalım).

**İlave bulgu (bu işle ilgisiz, standing-rule ihlali):** `project-prod-distributed-es` hafızası "NEVER put the customer name in the repo" diyor. HEAD'de 5 yorum satırında müşteri adı geçiyor: `internal/chstore/store.go:1912, :3046, :3103`, `cluster.go:143`, `probe_close_test.go:23`. Sürüm etiketleri olayı zaten tekil tanımlıyor — ad gereksiz. Ayrı kalem olarak kuyruğa alınabilir.

---

## Öneri

1. **Ship et — küçük iş.** 2 dosya, backend/CH/API dokunuşu yok. `v0.8.535` olarak tek slice.
2. **Ama önce Açık soru #3'ü kapat.** Prod'da `k8s.cluster.name` de geliyorsa etiket onun değerini gösterir; operatörün "değer `openshift.cluster.name`'den" gereksinimi karşılanmamış olur. Bu, koddan değil prod'dan ölçülecek bir şey — tek `SELECT countIf(has(res_keys,'openshift.cluster.name')), countIf(has(res_keys,'k8s.cluster.name')) FROM spans WHERE time >= now() - INTERVAL 1 HOUR` yeter.
3. Cevap "ikisi de geliyor" ise iki seçenek: (a) coalesce sırasını değiştirmek — **YAPMA**, `/services` filtresini ve MV okumalarını etkiler; (b) etiket için `semconv.ts` yerine ayrı bir `openshift.cluster.name`-öncelikli resolver — küçük, izole, ama iki farklı cluster tanımı doğurur (tehlikeli). Operatör seçer.
4. Lokal doğrulama §6.1'deki iki fixture ile; `openshift` dalı için §6.2'deki yapay compose değişikliği (commit edilmez).

---

## Açık sorular

1. **Kapsam:** Etiket `TraceWaterfall`'ı paylaşan üç sayfada da (Trace, PublicTrace, TraceCompare) görünsün mü, yoksa sadece `/trace`'te mi (→ opsiyonel prop)? Varsayılan önerim: üçünde de — davranış tutarlı, prop'suz.
2. **Helper:** `clusterOf` `semconv.ts`'e mi eklensin (tek doğruluk kaynağı, +vitest testi) yoksa `TraceWaterfall` içinde inline `first()` çağrısı mı (daha az dosya)? Önerim: `semconv.ts`.
3. **🔴 EN KRİTİK:** Prod OpenShift'te span'ler `k8s.cluster.name`'i DE taşıyor mu? Taşıyorsa etiket `openshift.cluster.name` değerini **göstermeyecek** (coalesce k8s'i önce alıyor). Bu kabul edilebilir mi, yoksa etiket için openshift-öncelikli ayrı resolver mı isteniyor?
4. **Ayrı kalem mi:** `Trace.tsx:295` header root'u orphan-aware değil (`Trace.tsx:157` öyle) — parçalı trace'te header `spans[0]`'a düşüyor, waterfall doğru gösteriyor. Şimdi mi düzeltilsin, kuyruğa mı?
5. **Görsel:** Chip rengi — nötr (`--muted` outlined, `wf-group` gibi) mi, yoksa cluster'a özel bir token mı? Root span zaten `wf-svc` + `wf-cat` taşıyor; üçüncü chip satırı kalabalıklaştırabilir. Mockup istenir mi?

**Onay bekliyor — implementasyona geçilmedi.**
