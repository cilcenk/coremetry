# Trace arama audit'i — milyarlarca trace, her attribute'a göre filtre, attribute eklendikçe hız düşmesin

Operatör brief'i (2026-09-03): *"Milyarlarca trace arasında hızlı search listeleme ve her attribute'a göre filtreleme yapmak istiyorum. Ayrıca attribute'lar ekledikçe de hız düşmesin."*

Rubrik: Datadog "facet" modeli (seçili attribute'lar indeksli, kalanı en-iyi-çaba), Honeycomb (her alan kolon), Dynatrace (indexed attributes). Ölçümler LOKAL fixture üzerinde (CH 26.2, tek node, 24 s = 868k span; prod 1B+ span/gün, 2 shard × 2 replica dış Distributed) — oranlar ölçekten bağımsız, duvar saatleri değil.

## 0. Özet

| # | Bulgu | Sınıf |
|---|---|---|
| **G1** | Terfi etmemiş HER attribute filtresi `attr_values[indexOf(attr_keys,'k')] = v` — **hiçbir skip index kullanmaz**: pencerenin TAMAMI okunur, değer nadir de olsa yok da olsa (ölçüldü: 868k/868k satır, 155 MiB, her vakada). "Attribute ekledikçe hız düşer"in mekanizması bu: her ek filtre aynı tam taramaya bir dizi araması daha ekler. | 🔴 |
| **G2** | Servis verilmeyince aday kümesi **en yeni 5000 trace** (`traceRecencySliceN`), servis verilince PK dilimi; attribute filtreleri bu dilimin İÇİNDE (aşama 2) uygulanır. UI bunu dürüstçe söylüyor ("filter is applied within the newest N traces") ama sonuç şu: pencerenin başındaki nadir bir attribute değeri **bulunamaz**. "Milyarlarca trace arasında" aramanın önündeki asıl engel. | 🔴 |
| **G3** | Aşama 2 sabit maliyeti lokalde ~1–1.3 s (187k satır, 31 MiB; sonuç 0 olsa bile) — 5000 aday × pencere. Terfi kolonu + set index bile bunu düşürmüyor (aşama 2 aday listesiyle çalışıyor, filtreyle değil). | 🟡 |
| **G4** | Terfi kolonları (channel_code, function_code, function_id, k8s.*) **kodda sabit** (`promoted_attr.go`); operatör yeni bir facet'i ancak sürümle alır. | 🟡 |
| **G5** | Anahtar keşfi (`/api/attribute-keys`) 200k satır örnekli — "her attribute" görünür, sorun keşif değil sorgu. | 🟢 |

**Karar önerisi (tek cümle):** Attribute eşitliği için **tek, sabit maliyetli bir indeks** — `attr_kvh Array(UInt64) MATERIALIZED cityHash64(k=v)` + `bloom_filter` — ve aday-kümesi akışını "indeksin buduğu span'lerden trace_id" olacak şekilde çevirmek (G1+G2 birlikte kapanır); üstüne operatör-yönetimli facet kaydı (G4). Ölçülmüş: nadir değerde okunan satır **13×** az (868k → 65k), indeks 0.3 B/satır, kolon 10 B/satır.

## 1. Bugünkü yol

### 1.1 Filtre derlemesi (`internal/chstore/filterexpr.go`)
- `FilterExpr{Key, Op, Values}`; ops `= != =~ !~ LIKE NOT LIKE IN NOT IN EXISTS NOT EXISTS > >= < <=`.
- Anahtar çözümü: `promoted` (terfi kolonu) → `wellKnown` (kolon) → **dizi**: `attr_values[indexOf(attr_keys, ?)]`; `resource.` öneki `res_*`; EXISTS → `has(attr_keys, ?)`.
- Terfi kolonları `promotedCols()` — boot'ta probe ile ("kolon VAR ≠ DOLU", v0.9.621) yayınlanan harita; liste `promoted_attr.go:85` sabit.

### 1.2 Skip index envanteri (`spans_local`, lokal = prod şekli)
`idx_trace bloom`, `idx_name set`, `idx_kind set`, `idx_db_system set`, `idx_http_status minmax`, `idx_status set`, terfi kolonları `set(0)`: `attr_channel_code`, `attr_function_code`, `attr_function_id`, `k8s_*` (7), `container_image(_tag)`. **`attr_keys` / `attr_values` üzerinde index YOK.**

### 1.3 İki aşamalı arama (`repo.go getTracesFromMV`)
```
servis VAR   → traceServiceSlice (PK dilimi, ≤ stage1Limit id)          → aşama 2
servis YOK   → having ∈ {RootOnly, HasError, MinMs, MaxMs} (attr DEĞİL)
               having boş / yalnız error → traceRecencySlice (en yeni 5000 id)  → aşama 2
               aksi                       → trace_summary_5m GROUP BY trace_id HAVING … → aşama 2
aşama 2      → spans WHERE trace_id IN (idler) AND time∈[floor,ceil] [AND attr filtreleri] GROUP BY trace_id
```
Attribute filtreleri **yalnız aşama 2'de** (`ApplyFilters(&wc, f.Filters)`) — yani aday kümesi attribute'tan HABERSİZ seçiliyor. `RankedWithin` sayacı ve Traces.tsx'teki not ("ranked within newest N") bu sınırı ilan ediyor.

### 1.4 Aggregate / Explore yolu (`repo.go:3880+`)
Filtreler doğrudan `spans` WHERE'ine giriyor (aday dilimi yok) → G1 aynen: nadir attribute'ta bile tam pencere taraması.

## 2. Ölçümler

### 2.1 EXPLAIN indexes=1 (`spans_local`, 24 s, 9 part / 121 granül)
| Yüklem | Skip | Granül |
|---|---|---|
| `attr_values[indexOf(attr_keys,'peer.service')] = 'ledger-service'` | — | **121/121** |
| `has(attr_keys,'banking.txn_ref') AND attr_values[…] = '<yok>'` | — | **121/121** |
| `attr_channel_code = '<yok>'` (terfi + set) | idx_attr_channel_code | 0/121 |
| `attr_function_code = '<yok>'` | idx_attr_function_code | 0/121 |

### 2.2 Scratch tablo deneyi (tek node MergeTree, 24 s = 867 786 satır; `ON CLUSTER` yok, Keeper'a dokunulmadı; deney sonunda DROP)
Kolonlar: `attr_kv Array(String) MATERIALIZED arrayMap((k,v)->concat(k,'=',v), …)`, `attr_kvh Array(UInt64) MATERIALIZED arrayMap(x->cityHash64(x), attr_kv)`, indeksler `bloom_filter(0.01) GRANULARITY 4` (kv, kvh, keys). Ölçüm `system.query_log`, 3 koşu medyanı, `use_query_condition_cache=0`.

| Vaka | Yüklem | Granül | read_rows | read_bytes | med ms |
|---|---|---|---|---|---|
| dizi, yaygın değer | `attr_values[indexOf(…'peer.service')]='ledger-service'` | 108/108 | 867 786 | 155 MiB | 3 395 |
| dizi, nadir değer | `attr_values[indexOf(…'banking.txn_ref')]='TXN-…'` | 108/108 | 867 786 | 155 MiB | 3 019 |
| dizi, olmayan anahtar | `attr_values[indexOf(…'no.such.key')]='x'` | 108/108 | 867 786 | 155 MiB | 1 894 |
| **kv bloom, nadir** | `has(attr_kv,'banking.txn_ref=TXN-…')` | **8/108** | **65 536** | 17.7 MiB | 1 205 |
| kv bloom, yaygın | `has(attr_kv,'peer.service=ledger-service')` | 95/108 | 764 702 | 198 MiB | 3 336 |
| **kvh bloom, nadir** | `has(attr_kvh, cityHash64('banking.txn_ref=TXN-…'))` | **8/108** | **65 536** | **4.9 MiB** | **407** |
| kvh + tam eşitlik | `has(attr_kvh, …) AND attr_values[…]='TXN-…'` | 8/108 | 65 536 | 6.7 MiB | 1 280 |
| keys bloom, olmayan anahtar | `has(attr_keys,'no.such.key')` | **4/108** | 32 768 | 0.5 MiB | 224 |

Depolama (868k satır, ZSTD(3)): `attr_values` 9.29 MiB · `attr_keys` 1.57 MiB · `attr_kv` 12.60 MiB · **`attr_kvh` 8.34 MiB** · her bloom indeksi 260 KiB.
→ 1B span/gün'e izdüşüm: kvh **≈ +10 GB/gün** (attr_values ≈ 11 GB/gün'ün yanına), indeks ≈ +0.3 GB/gün; 30 g retention'da ≈ +300 GB. `attr_kv` (ham dize) 1.5× daha pahalı ve hiçbir ek kazanç vermiyor → **hash'li varyant**.

Okuma: bloom **nadir/orta** kardinaliteli değerlerde çalışır (id, txn_ref, pod, kullanıcı); **yaygın** değerlerde (servisin her span'inde olan `peer.service`) budamaz — bu Datadog'un da davranışı; yaygın değer için doğru yol zaten aşama-1 aday dilimi (G2'nin meşru olduğu tek yer).

### 2.3 Uçtan uca (`/api/traces`, lokal, refresh=1, TTFB)
| Filtre | 24 s | 7 g | Not |
|---|---|---|---|
| `peer.service = ledger-service` (dizi, yaygın) | 3.98 s | 2.54 s | 50 satır — recency dilimi içinde bulundu |
| `banking.txn_ref = <yok>` (dizi, nadir) | 1.62 s | 1.45 s | 0 satır; aşama 2 187k satır okudu |
| `channel_code = MOBILE` (terfi + set) | 1.38 s | 1.15 s | 0 satır; aynı 187k — index aşama 2'de işe yaramıyor (G3) |

query_log: her istekte aşama-2 ifadesi (`SELECT trace_id, coalesce(nullIf(anyIf(name…`) 187 686 satır / 31 MiB / 1.2–1.3 s. Yani bugün hız zaten filtreye değil **aday dilimine** bağlı — "hızlı ama sınırlı küme".

## 3. Seçenekler

| | Yaklaşım | Kapsam | Ölçülmüş / beklenen | Karar |
|---|---|---|---|---|
| **A** | `attr_kvh` hash kolonu + bloom + `attr_keys` bloom; derleyici `=`/`IN`/`EXISTS` için `has(attr_kvh, cityHash64(?))` + kesin dizi eşitliği (bloom yanlış-pozitif için) | HER attribute, eşitlik/IN/EXISTS; **attribute sayısından bağımsız tek indeks** | 13× az satır (nadir); +10 GB/gün | **Dilim 1** |
| **B** | Aday akışı: indeks-kullanabilir attr yüklemi varsa aşama 1 = `spans` üzerinden bloom-budanmış `trace_id` taraması (LIMIT stage1Limit, zaman sıralı), recency dilimi DEĞİL | G2 kapanır: nadir değer pencerenin tamamında bulunur | aşama-1 maliyeti = budanan granül sayısı; yaygın değerde bugünkü dilime düşülür (RankedWithin korunur) | **Dilim 1** (A ile birlikte, aksi hâlde A'nın kazancı aşama 2'de görünmez) |
| **C** | Operatör-yönetimli **facet** kaydı (Datadog facets): Settings → Traces → Facets; terfi kolonu (`MATERIALIZED coalesce(yazımlar)`) + `set(0)`/bloom index; `promoted_attr.go` listesi sabit değil `system_settings`'ten | yaygın-değerli sık filtreler (channel, function, tenant) için A'nın çalışmadığı yer | CHANNEL_CODE ölçümü: 10M → 1.3M satır (v0.9.623) | **Dilim 2** — küme kipinde iki-boot DDL sözleşmesi, prod'da migration dosyası (0013 emsali) |
| D | Ters indeks tablosu / MV (`span_attr_index(k, v, time_bucket, trace_id)` — SigNoz `tag_attributes` sınıfı) | tüm ops | ikinci yazım yolu, JOIN, 1B × attr satır/gün | **RED** — A aynı işi tek kolonla yapıyor |
| E | `Map(String,String)` kolonuna geçiş + `mapKeys/mapValues` bloom | tüm attr'lar | tablo yeniden yazımı (36B satır) | **RED** — diziler kalır; A dizinin üstüne konur |
| F | Regex/LIKE/aralık için indeks | `=~`, `LIKE`, `>` | bloom yardım etmez; `tokenbf` yalnız kelime | **Dilim 3 (araştırma)**: `ngrambf_v1` on `attr_values` — ölçülmeden söz verilmez |

**"Attribute ekledikçe hız düşmesin" cevabı:** A'da indeks sayısı attribute sayısından bağımsız (tek `attr_kvh`); ingest maliyeti attribute başına bir `cityHash64` (ölçülecek, beklenti <%2 CPU — CHANNEL_CODE terfi kolonu emsali +%1-2); sorgu maliyeti filtre sayısıyla değil **budanan granül** sayısıyla ölçeklenir (her ek `has()` bloom'da AND'lenir → daha az granül, daha çok değil).

## 4. Dilim planı

### Dilim 1 — kvh indeksi + indeks-güdümlü aday akışı (M/L)
| Adım | Dosya | Not |
|---|---|---|
| 1.1 Şema: `attr_kvh Array(UInt64) MATERIALIZED arrayMap(x -> cityHash64(x), arrayMap((k,v) -> concat(k,'=',v), attr_keys, attr_values))`, `res_kvh` (resource ikizi), `INDEX idx_attr_kvh … bloom_filter(0.01) GRANULARITY 4`, `INDEX idx_attr_keys attr_keys bloom_filter(0.01)`, aynı res | `store.go` (alters dilimi, koşullu; küme kipinde ertelenmiş DDL + iki-boot), `migrations/0014_attr_kvh.sql` (+rollback; prod elle) | MATERIALIZED = dağıtık-güvenli (Distributed forward düşürür); eski part'lar okuma anında hesaplar → indeks eski part'larda YOK: kazanç retention boyunca dolar ya da `MATERIALIZE INDEX` (prod'da IO; operatör kararı) |
| 1.2 Derleyici: `=` → `has(attr_kvh, cityHash64(?)) AND attr_values[indexOf(attr_keys,?)] = ?` (bloom yanlış-pozitif için kesin eşitlik KALIR); `IN` → `hasAny(attr_kvh, [cityHash64(k=v1),…]) AND … IN`; `EXISTS` → `has(attr_keys, ?)` (zaten); `!=`/regex/LIKE/aralık → dizi yolu aynen | `filterexpr.go` (+ kolon-var kapısı: `promotedCols()` gibi probe; kolon yoksa eski yol — KIRILMAZ) | `k=v` birleştirmesi `concat(k,'=',v)`: anahtarda `=` olabilir mi? OTel'de yok; yine de hash `k`+0x00+`v` ile güvenli ayırıcı (**karar: NUL ayırıcı**) |
| 1.3 Aday akışı: `traceAttrSlice` — indeks-kullanabilir yüklem varsa aşama 1 = `SELECT trace_id FROM spans WHERE time∈… AND <has yüklemleri> ORDER BY time DESC LIMIT stage1Limit` (PK `service_name,time` → servis varsa PK + bloom; yoksa bloom + MinMax); sonuç boşsa BOŞ (dilime düşme YOK — cevap doğru); `RankedWithin` yalnız yaygın-değer yolunda | `repo.go getTracesFromMV`, `trace_slice.go` | Yaygın değer tespiti: sorgu-öncesi bilinmez → **budget**: aşama 1 `LIMIT stage1Limit` + `max_execution_time`; kesilirse `RankedWithin` = okunan sayı (dürüst not korunur) |
| 1.4 Aggregate/Explore yolu aynı derleyiciden geçer (otomatik) | — | `spanmetric`/`AggregateFilter` ApplyFilters ortak |
| 1.5 FE: not metni ("filter is applied within the newest N") yalnız yaygın-değer yolunda görünür; nadir değerde "pencerenin tamamında arandı" | `Traces.tsx` | sunucu `rankedWithinRecent` = 0 gönderir |
| 1.6 Gözlem: `/api/health` sayaçları `attr_index_used` / `attr_index_fallback` | `api.go` health map | self-observability kuralı |

### Dilim 2 — Facet kaydı (Datadog facets) (M)
`system_settings` `trace_facets` blobu (anahtar, yazımlar, kapsam span/resource, tip LC/String, indeks set/bloom); Settings → Traces → Facets (admin, audit); boot DDL `promoted_attr.go` listesi + blob birleşimi; küme kipinde iki-boot sözleşmesi + prod için üretilen migration metni (UI "SQL'i göster"). `promotedCols()` probe'u aynen (DOLU kolon kapısı).

### Dilim 3 — regex/LIKE/aralık (araştırma)
`ngrambf_v1` on `attr_values`; ölçüm önce (INCIDENTS: tokenbf ölçümleri).

## 5. Riskler
| # | Risk | Şiddet | Aşı |
|---|---|---|---|
| R1 | +10 GB/gün depolama (1B span) | 🟡 | ZSTD(3); operatör onayı; alternatif yalnız `attr_keys` bloom (EXISTS) — yetersiz |
| R2 | Ingest CPU: `cityHash64` × attr sayısı × span | 🟡 | Aşama 0 ölçümü (demo ingest'te CPU farkı); MATERIALIZED sunucuda hesaplanır, Go yoluna dokunmaz |
| R3 | Eski part'larda indeks yok → kazanç kademeli | 🟡 | Dürüst: `attr_index_fallback` sayacı; `MATERIALIZE INDEX` operatör kararı |
| R4 | Bloom yanlış-pozitif | 🟢 | Kesin dizi eşitliği yüklemde KALIR (ölçüldü: +0.9 s lokal, satır aynı) |
| R5 | Yaygın değerde bloom budamaz → aşama 1 tam pencere | 🟡 | LIMIT + `max_execution_time` bütçesi; kesilince RankedWithin |
| R6 | Küme kipi DDL yarışı (v0.9.613) / Keeper (2026-08-28) | 🔴 | Boot koşmaz (0013 sözleşmesi); prod migration elle; lokal minikube'de yalnız `kubectl set image`, chc'ye `apply` ASLA |
| R7 | Distributed forward'da MATERIALIZED kolon | 🟢 | Forward düşürür, sunucu hesaplar (v0.8.185/186 sınıfı DEĞİL) |

## 6. Test planı
- `filterexpr_kvh_test.go`: her op × (kolon var / yok) → SQL şekli + bağlama sırası; `=` yüklemi HEM `has(attr_kvh…)` HEM kesin eşitlik taşır (yanlış-pozitif kapısı); NUL ayırıcı hash tablosu Go↔CH eşitliği (`cityHash64` Go tarafı `github.com/ClickHouse/ch-go/cityhash` ya da CH'de `SELECT cityHash64(…)` altın değerleri).
- `trace_attr_slice_test.go`: aday akışı seçimi (indeks-kullanabilir yüklem var/yok, servis var/yok), LIMIT/bütçe, boş sonuç dilime DÜŞMEZ.
- `store_alters_test.go`: `attr_kvh`/`res_kvh` DDL'i `alters` diliminde koşullu; `partition_dedup_test`/`highVolumeTables` kapıları.
- Canlı: lokal `EXPLAIN indexes=1` (`idx_attr_kvh` Granules N/M), `/api/traces` nadir değer 7 g → bulundu + TTFB; prod'da `query_log` `SelectedMarks` (perf-triage §7 sözleşmesi).

## Onay soruları
1. Dilim 1 = A (kvh hash kolonu + bloom) + B (indeks-güdümlü aday akışı) birlikte — kabul mü? (A tek başına aşama 2'de görünmez.)
2. Depolama bedeli ≈ +10 GB/gün @1B span (ZSTD(3)) — kabul mü? Alternatif: yalnız span attr'ları (resource hariç) → ≈ +7 GB/gün.
3. Prod'da eski part'lar: `MATERIALIZE INDEX` (IO, saatler) mi, retention boyunca kademeli mi?
4. Dilim 2 facet kaydı (Settings UI + üretilen migration SQL) — bu brief'in kapsamında mı, ayrı mı?
5. `!=` / regex / LIKE / aralık dizi yolunda (yavaş, tam tarama) kalıyor — Dilim 3 araştırma olarak kabul mü?

---

## Durum (2026-09-03) — Dilim 1 ve 2 gemide

| Sürüm | Ne |
|---|---|
| v0.10.299 | 1a şema: `attr_kvh`/`res_kvh` `Array(UInt64) MATERIALIZED cityHash64(k'\x1F'v)` + `bloom_filter(0.01)` (kv + anahtar); boot repair/probe, ertelenmiş DDL sonrası yeniden probe; `migrations/0014_attr_kvh.sql` (+rollback, gömülü) |
| v0.10.300 | 1b derleyici: dizi yolundaki `=`/`IN` → `has(attr_kvh, cityHash64(concat(?, '\x1F', ?))) AND kesin eşitlik` / `hasAny`; kapı `AttrIndexAvailable()` (kolon yoksa eski şekil bayt-bayt); metric_points asla; `/api/health` `attr_index_available/used` |
| v0.10.301 | 1c aday akışı: indeks-kullanabilir yüklem varsa aşama 1 `spans` üzerinden (recency/servis diliminden ÖNCE); boş sonuç dilime düşmez; `attr_slice_used` |
| v0.10.302 | 2a facet kaydı: `system_settings['trace_facets']` → yerleşik terfi listesiyle birleşir (`attr_f_<key>` + set(0)); GET/PUT `/api/settings/trace-facets`; prod için üretilen ON CLUSTER SQL |
| v0.10.303 | 2b Settings → "Trace facet'leri" sekmesi |

**Prod'a alma sırası (dış Distributed):** (1) `migrations/0014_attr_kvh.sql` ADIM 1-4 elle (`uptrace_all` → küme adı); (2) pod'ları yeniden başlat → `/api/health` `attr_index_available: true`; (3) doğrulama: nadir bir attribute değeriyle `/traces` 7 g — önce bulunamayan satır artık bulunur, `query_log`'da `has(attr_kvh` ifadesi `SelectedMarks` düşük; (4) facet'ler: Settings → Trace facet'leri → ekle → "Prod SQL'ini göster" → elle koş → pod restart. Eski part'larda indeks yok (kademeli); istenirse ADIM 6 `MATERIALIZE INDEX` mesai dışı.

**Lokal doğrulama notu:** minikube'de chc-0'ın replikasyon kuyruğu Ağustos'tan kalma takılı GET_PART'larla kilitli (partition'larda aktif satır var → düşürme = fixture veri kaybı, operatör kararı); chc-1 kolon+indeksi aldı, chc-0 almadı. Uygulama yarım şemada güvenle eski yola düşer (`AttrIndexAvailable()` probe'u). Pozitif doğrulama (7 g nadir değer) chc-0 açılınca `scratchpad/verify_301.sh`.

**Kalan:** Dilim 3 regex/LIKE/aralık araştırması (`ngrambf_v1` ölçümü, scratch tek-node).

## Dilim 3 — regex / LIKE / önek ölçümü (2026-09-03) — KAPANDI, kod yok

Scratch tek-node MergeTree (24 s = 857 966 satır): `attr_values` üzerinde `ngrambf_v1(3, 8192, 2, 0)`, terfi kolonu `attr_f_txn` (banking.txn_ref) üzerinde `ngrambf_v1(3, 4096, 2, 0)` + `tokenbf_v1(4096, 2, 0)`; query_log 3 koşu medyanı.

| Yüklem | Skip | Granül | read_rows | read_bytes | med ms |
|---|---|---|---|---|---|
| dizi `attr_values[indexOf(…)] LIKE '%177149%'` | — (dizi ngram indeksi UYGULANMAZ: elemana indeksle erişim) | 108/108 | 857 966 | 153 MiB | 8 114 |
| dizi `… LIKE 'TXN-2026-17%'` | — | 108/108 | 857 966 | 153 MiB | 8 012 |
| `arrayExists(v -> v LIKE …, attr_values)` | — | 108/108 | — | — | — |
| terfi kolonu `attr_f_txn LIKE '%177149%'` | ngram 98/108 | 98 | 764 796 | **3.2 MiB** | **1 366** |
| terfi kolonu `match(attr_f_txn, '177149')` | ngram 98/108 | 98 | 764 796 | 3.2 MiB | **678** |
| terfi kolonu `LIKE 'TXN-2026-17%'` | ngram 106/108 | 106 | 848 272 | 7.7 MiB | 873 |
| `hasToken(attr_f_txn, '177149')` (tam token) | tokenbf **0/74** | 0 | — | — | — |

**Sonuç:** LIKE/regex/önek için kazanç **indeksten değil dar kolondan** geliyor — 153 MiB → 3 MiB (dizi dekompresyonu yok), 6–12× hız. ngram bloom'u id-benzeri yüksek kardinaliteli değerlerde doyuyor (98/108, 106/108 — 3-gram'lar her granülde var); yalnız `hasToken` tam-token'da budar, o da alt-dize araması değil. Dizi üzerindeki ngram indeksi `indexOf` erişimine ve `arrayExists` lambdasına hiç uygulanmıyor → **"her attribute'ta indeksli regex" mümkün değil**; Datadog da regex'i facet'lerde koşar.

**Karar (kod yok):** regex/LIKE/önek'in yolu **facet kaydı** (Dilim 2, gemide): terfi kolonu tüm operatörlerde kullanılır (`promoted` haritası op'tan bağımsız kolona yönlendirir), yani `Settings → Trace facet'leri`ne eklenen bir attribute'ta LIKE/regex bugün zaten dar kolondan okur. Facet kolonuna ek `ngrambf_v1` indeksi ölçüme göre değmiyor (ek disk, sıfıra yakın budama); istenirse uzun/ayırt edici alt-dizeler için ayrı ölçümle. Dizi yolu (facet olmayan anahtar) LIKE/regex'te tam tarama olarak kalır ve öyle olduğu belgelenir.
