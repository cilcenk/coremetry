# Kibana-Parite: /logs Arama Deneyimi (2026-08-21)

Operatör hedefi: "Elastic Kibana kadar iyi log search deneyimi olsun."

Keşif sonucu: /logs sanılandan yakın — KQL + iki-düzey autocomplete,
alan paneli (top-5 + % + ⊕/⊖), brush-zoom histogram, doc viewer
(Table/JSON), ±50 bağlam modalı, pill'ler (negatifle/devre-dışı),
SSE live tail (Kibana'yı aşar), kayıtlı aramalar, Kibana derin linki
zaten gemide. Boşluklar cilalar + birkaç orta dilim.

# Kibana-parite matrisi — /logs (kod doğrulamalı, 2026-08-21)

Taranan: `frontend/src/pages/Logs.tsx` (1250), `components/{KqlSearchInput,LogFieldsPanel,LogsHistogram,LogTable,LogContextModal,SavedViewsBar}.tsx`, `lib/{logFilters,logsUrl}.ts`, `lib/api.ts` logs istemcileri.

## Matris

| Kibana bileşeni | Durum | Kanıt (file:line) | Boşluk | Değer | Efor |
|---|---|---|---|---|---|
| KQL çubuğu + saha-farkında autocomplete | **VAR** | `KqlSearchInput.tsx:88-118` token parse; `:204` değer önerisi (`api.logsFieldValues` → ES `_terms_enum`, `since`-sınırlı v0.9.291, 30s sunucu cache, 180ms debounce); `:224-234` alan-adı tamamlama + otomatik `:` (v0.9.955, sıfır ek ES turu); `:355-360` kırpık-katalog dürüstlüğü | ~~AND/OR/NOT önerisi~~ + ~~gönderim öncesi doğrulama~~ → **v0.9.1216** (kqlLint Enter kapısı + operatör tamamlama). KALAN (bilinçli, değer/efor altı): imleç-ortası tamamlama — yalnız en sağdaki token tamamlanır | — | — |
| Alan paneli top-values %li + tıkla-filtrele | **VAR** | `LogFieldsPanel.tsx:39-104` accordion top-5 + % bar + ⊕/⊖; fetch YALNIZ expand'de, staleTime 60s = sunucu TTL (`:19-21` sözleşme); `:109-120` Popular fields; `:272-277` "first N of M" | ~~Kapsama yüzdesi~~ + ~~exists filtresi~~ → **v0.9.1217** (sıfır ek sorgu — FieldStats.Total zaten exists sayısı; ⊕∃/⊖∃). KALAN (bilinçli): top-5 sabit — "daha fazla" genişletmesi yok | — | — |
| Sürükle-zoom histogram | **VAR** | `LogsHistogram.tsx` TimeChart `onBrush` + çift-tık geri; `usePageZoomRange`; brush/sparse-bucket bug'ı v0.9.218'de çözülmüş | ~~Breakdown seçici~~ → **v0.9.1220** (seviye\|servis, top-5+"diğer", ES OTHER/CH LIMIT dürüstlüğü). KALAN (takip dilimi): namespace/cluster ekseni — ES `histogramGroupField` + CH whitelist alan haritası ister, sessiz `_total` düşüşü olmasın diye seçenek hiç sunulmadı | — | — |
| Genişleyen doküman satırı JSON/tablo | **VAR** | `LogTable.tsx` Table sekmesi (prettyMaybe gövde + attr kv-table + Resource details); JSON sekmesi tam kayıt + kopya | ~~Alan→sütun ekle~~ → **v0.9.1215** (KvRow ▤/▣ üçüncü ikonu). KALAN (bilinçli): tek-doküman kalıcı linki — by-id uç + `?doc=` gerektirir | — | — |
| Surrounding documents | **VAR** | `LogContextModal.tsx` ±50, pivot vurgusu, trace-peek; `api.ts` `logsContext` | ~~N sabit~~ → **v0.9.1218** (+50 artımlı, tavan 200 = mevcut sunucu sınırı); ~~yalnız servis kapsamı~~ → kısmen 1218 ("⇲ Tüm servisler" anahtarı); ~~vurgu yok~~ → **v0.9.1215**. KALAN (takip dilimi): pod/namespace kapsamı — `logstore.Filter.Pod` + iki backend clause ister; filtre-koruma anahtarı (bilinçli) | — | — |
| Filtre pill'leri düzenle/negatifle/devre-dışı | **VAR** | `Logs.tsx` pill barı (≠/◐/×/Clear all); `logFilters.ts` model+toggle; `?filters=` URL'de → Share+SavedViews taşır | ~~EDIT~~ → **v0.9.1219** (popover: alan/operatör/değer); ~~exists~~ → 1217; ~~is-one-of~~ → 1219 (`key:("a" OR "b")`, URL 6. tuple geriye-uyumlu). KALAN (bilinçli): sayısal aralık operatörü (>= / <=) — phraseQuote'suz ayrı derleme dalı ister, her-birim-testli | — | — |
| Vurgulama | **VAR** | `logFilters.ts` istemci-tarafı term çıkarımı + 4KB scan cap; `LogTable.tsx` `<mark>` | ~~Genişletilmiş doküman + context modalı~~ → **v0.9.1215**. (ES highlight API'siz olması KASITLI spec — korundu) | — | — |
| Canlı takip | **VAR** (Kibana'yı aşar) | `Logs.tsx:434-479` SSE `/api/logs/stream`, LIVE_CAP 1000, dedup, `gap` olayı, `document.hidden` kapat/aç + `since` yakalama | — | — | — |
| Kayıtlı arama + sütun seti | **VAR** | `SavedViewsBar.tsx:35-90` kişisel+paylaşımlı, modified rozeti, 1-9 kısayol; `Logs.tsx:191-204` sütunlar localStorage + `?cols=` (varsayılansa yazılmaz); saved view tüm QS'i (filters+cols+severity+asc) saklar | — | — | — |

Parite ÜSTÜ mevcutlar: dürüstlük zarfı (partial/timed_out `Logs.tsx:854-863`, envUnapplied `:802-809`, hasTraceUnapplied `:838-845`), yerel "narrow within results" (`:1089-1158`, sıfır ES turu), CSV/NDJSON yüklü-satır exportu, Kibana derin linki (`:654-673`).

## Uygulama durumu (2026-08-21 gecesi)
- Dilim 6 → v0.9.1215 · Dilim 4 → v0.9.1216 · Dilim 5 → v0.9.1217 ·
  Dilim 2 → v0.9.1218 (artımlı ±N tavan 200 + servis-kapsam anahtarı;
  POD/NAMESPACE kapsamı bilinçli kesildi — logstore.Filter'a Pod alanı
  + iki backend clause'u ister, takip dilimi olarak açık).
- Dilim 1 → v0.9.1219 (pill EDIT popover: alan/operatör/değer(ler),
  is-one-of `key:("a" OR "b")`, öneriler mevcut logsFieldValues ucundan).
- Dilim 3 → v0.9.1220 (histogram kırılım seçicisi seviye|servis — İKİ
  backend'in de desteklediği eksenler; namespace/cluster alan haritası
  ister, takip dilimi. Servis modu: top-5 çizgi + "diğer", oran ekseni
  yalnız seviye modunda. + çift-fetch katlaması: severity=0'da çipler
  histogramın onSeries'inden, 'sev-volume' sorgusu yalnız seviye tabanı
  veya servis kırılımı aktifken — /logs açılışında 1 ES _search eksik).
- **TÜM 6 DİLİM GEMİDE** (v0.9.1215-1220).

## Kapanış durumu (2026-08-22)

**Teknik kapsam KAPALI:** matristeki her boşluk ya sürümüyle
damgalandı ya da açıkça bilinçli kesim / takip dilimi olarak
gerekçelendirildi (yukarıdaki Boşluk sütunu artık son durumu söylüyor).
Değer/efor çizgisinin altında bırakılan 7 kalem: imleç-ortası KQL
tamamlama · alan panelinde top-5 sabiti · tek-doküman kalıcı linki ·
context filtre-koruma anahtarı · sayısal aralık pill operatörü ·
pod/namespace context kapsamı (takip) · kırılımda namespace/cluster
ekseni (takip). Hiçbiri sessiz düşmüyor — hepsi ya UI'da hiç vaat
edilmiyor ya dürüst notla sınırlanıyor.

## Çizgi-altı artıkların kapanışı (2026-08-22/23 damgası)

Kapanış bölümündeki 7 kalemin dördü SONRADAN kapandı, biri bayat çıktı:
- ~~Sayısal aralık pill operatörü~~ → **v0.9.1222** (≥/≤, URL 7. tuple).
- ~~Alan paneli top-5 sabiti~~ → **v0.9.1223** ("daha fazla (20)",
  yalnız iki basamak, cache-key'de).
- ~~Context filtre-koruma anahtarı~~ → **v0.9.1224** ("⧩ sorguyu koru").
- ~~İmleç-ortası KQL tamamlama~~ → **ZATEN VARDI** (v0.9.955'ten beri):
  `detectFieldToken(text, cursor)` imleçten geri yürür
  (KqlSearchInput.tsx:89-119), `insertValue` valueStart/valueEnd
  dilimleriyle ORTADAN değiştirir + imleci geri koyar (:248-260).
  Matristeki boşluk metni bayattı — kod değil damga eksikti.
KALAN 3 (kuyrukta, operatör seçimli): tek-doküman kalıcı linki ·
pod/namespace bağlam kapsamı · kırılımda namespace/cluster ekseni.

**Öznel kabul AÇIK:** "Kibana kadar iyi" hükmü operatörün. Elde
doğrulama listesi (lokal v0.9.1220+, http://localhost:8090/logs):
1. Pill'e tıkla → EDIT popover (operatör değiştir, is-one-of dene).
2. Histogram başlığı → kırılım: servis (top-5 + diğer, lejant).
3. Alan paneli → kapsama %si + ⊕∃ exists.
4. Bozuk KQL yaz + Enter → sorgu ES'e gitmeden uyarı.
5. Satır genişlet → alan yanı ▤ ile sütuna ekle; vurgunun gövdede
   ve ±bağlam modalında sürdüğünü gör.
6. Bağlam modalı → +50 büyüt, "⇲ Tüm servisler".

## Önerilen 6 dilim (değer/efor sırasıyla)

1. **Pill EDIT popover + operatör sözlüğü** (değer 4, efor M) — pill'e tıkla → alan/operatör/değer düzenle; `is / is not / exists / is one of` (exists → `_exists_:field`, is-one-of → `key:("a" OR "b")`). *ES korkuluğu:* her şey `compileSearch` (`logFilters.ts:33-40`) üzerinden AYNI tek query-string'e derlenir — yeni endpoint yok, sorgu sayısı değişmez; değer önerisi mevcut `logsFieldValues` (since-sınırlı, 30s cache) ile.
2. **Surrounding context v2** (değer 4, efor S-M) — "±50 daha yükle" artımı, pod/namespace kapsam anahtarı, pivot sorgu terimlerinin vurgusu (mevcut `highlightSegments` yeniden kullanılır). *ES korkuluğu:* büyütme yalnız TIKLAMAYLA, her adım tek `size=N` + zaman-bantlı sorgu; kapsam anahtarı filtre EKLER (sonuç seti küçülür); otomatik yükleme/scroll-fetch yok.
3. **Histogram breakdown seçici** (değer 4, efor M) — severity yerine service/cluster/namespace kırılımı (top-5 + Other). *ES korkuluğu:* tek `date_histogram` + `terms(size=5)` bileşik sorgu — bugünkü severity sorgusuyla AYNI maliyet sınıfı; `logsBucketSec` basamakları (≤240 kova) + `min_doc_count` korunur; varsayılan severity kalır (o veri zaten geliyor, `Logs.tsx:395-412`), kırılım yalnız açık seçimde çalışır; cache-key alan adını içerir, 30s TTL.
4. **KQL ön-doğrulama + operatör autocomplete** (değer 3, efor S) — istemci-tarafı hafif parse: dengesiz tırnak/paren uyarısı gönderim öncesi; boşluk-sonrası konumda AND/OR/NOT önerisi. *ES korkuluğu:* SIFIR ek ES çağrısı — tamamen istemci; hatta geçersiz sorguyu hiç göndermeyerek ES turu AZALIR (bugün her bozuk KQL bir başarısız `_search`).
5. **Alan kapsama yüzdesi + exists filtresi** (değer 3, efor S) — accordion'a "dokümanların %X'inde" satırı + "⊕ alan var" pill'i; panel filtre kutusuna exists kısayolu. *ES korkuluğu:* kapsama, MEVCUT fieldstats sorgusuna tek `filter(exists)` agg olarak biner — ek round-trip yok; fetch yine yalnız expand'de, staleTime 60s aynen.
6. **Doc-viewer sütun-toggle + vurgu tamamlama** (değer 2-3, efor XS) — KvRow'a "sütun yap/kaldır" üçüncü ikonu (mevcut `toggleColumn` `Logs.tsx:320-321` zaten hazır); `highlightTerms`'i genişletilmiş gövde (`prettyMaybe` çıktısı) ve context modal satırlarına uygula. *ES korkuluğu:* SIFIR backend etkisi — her ikisi de elde olan veri/callback üzerinde saf istemci işi; vurgu 4KB scan-cap'i (`logFilters.ts:98`) aynen taşınır.

---

# EK — FE tam yetenek haritası

## /logs yetenek haritası (koddan doğrulanmış, file:line)

Tüm yollar `/Users/cenk/Documents/gotrace/frontend/src/` göreli.

### 1. Sorgu girişi
- **Serbest metin + KQL/Lucene tek satır kutu**: `KqlSearchInput` (`components/KqlSearchInput.tsx:131`), gövdeye serbest metin VEYA `field:value` KQL — placeholder/örnekler `pages/Logs.tsx:742-750`. Tek satır, yatay scroll (`KqlSearchInput.tsx:179-181,308-326`). Enter=ara (`:287-291`), `/` kısayolu + kbd rozeti (`Logs.tsx:729,738`; `KqlSearchInput.tsx:331`).
- **Autocomplete iki düzey**: (a) **alan adı** — `/api/logs/fields` kataloğundan (panelle AYNI state, sıfır ek istek; `Logs.tsx:716,722`), ranked, ':' otomatik eklenir, kırpık katalog "ilk N/M" dürüstlük başlığı (`KqlSearchInput.tsx:146-166,224-234,355-361`); (b) **alan değeri** — ES `_terms_enum`, 12 öneri, 180ms debounce, pencereye `since` sınırı (sunucu 7d clamp) (`KqlSearchInput.tsx:185-217,204`; `Logs.tsx:139`). Ok/Tab/Enter/Esc klavye (`:258-292`).
- **Operatör (AND/OR/NOT) autocomplete YOK**; sözdizimi vurgulama/inline doğrulama YOK — hata ancak sorgu reddedilince "Query failed" kutusunda KQL ipucuyla görünür (`Logs.tsx:1039-1066`).
- **NL→sorgu köprüsü /logs'ta YOK**: `copilotNLToQuery` yalnız /explore'da (`pages/explore/NLQueryBox.tsx:34`; backend `internal/api/copilot_nl_query.go:11-25` — çıktı FilterBuilder filtresi, KQL değil). /logs'un AI'ı anlatıcı, sorgu üretmez (bkz. §7).
- Ek girişler: ServicePicker (`Logs.tsx:687`), cluster `<select>` (`:694-702`, /api/clusters), Trace ID kutusu (trim+lowercase+0x soyma, retention-boyu arama; `:759-767`), "◆ With trace" toggle (`:772-777`), Reset (`:779`).

### 2. Alan paneli (LogFieldsPanel)
- Kibana Discover sol rayı (`components/LogFieldsPanel.tsx:9-21`); **varsayılan KAPALI**, "ƒ Fields" dikey çubukla açılır, tercih localStorage (`:140,172-187`).
- Gruplar: Selected (aktif tablo kolonları) / Popular (backend `conventionalLogFields` aynası, yalnız mapping'de varsa; `:106-120,153-159`) / Available; istemci-yerel ad süzgeci (`:141,160-170`).
- **Top-5 değer + % bar + ⊕/⊖ pill + kolon ekle/çıkar** akordeonu (`:64-101`).
- **Fetch disiplini (ES sözleşmesi)**: tek ağ çağrısı = genişletmede fieldstats, prefetch yok, staleTime 60s = sunucu TTL (`:17-21,47-52`). Katalog kırpığında "first N of M" beyanı (`:272-277`). Satırlar content-visibility (`:205-212`). CH backend'de fields boş → "No mapping discovery" (`:263-266`).

### 3. Histogram (LogsHistogram)
- **Sürükle-brush = zaman seç** (`components/LogsHistogram.tsx:142-144` → `Logs.tsx:1028-1030` → paylaşılan `usePageZoomRange` zoom yığını `Logs.tsx:132`), **çift-tık = geri** (`LogsHistogram.tsx:145`; ipucu dürüst, yalnız bağlıyken vaat: `:126-134`). Tek çubuğa tıkla-zoom YOK; hover crosshair TimeChart'tan.
- **Severity kırılımı**: v0.9.218'den beri tam yığılmış bant DEĞİL — toplam (gri) + warn+err (amber) + error (kırmızı) overlay + sağ eksende error-rate çizgisi (`:14-29,240-253`); seviye süzgeci açıkken oran dürüstçe gizlenir (`:119-124,229-231`). Lejant operatör isteğiyle kapalı (`:147-149`).
- Kova çözünürlüğü tek kaynak `logsBucketSec` (≤240 kova; `:158-163`, `Logs.tsx:106-114`). Penceresiz+trace'siz fetch yok (`:68-75`).
- Ayrıca **seviye facet chip'leri** canlı sayaçlı (All/ERROR/WARN/INFO/DEBUG; kasıtlı severity'siz sorgu — Kibana facet davranışı) `Logs.tsx:381-424,910-1001`. Not: chip sorgusu (`:395-412`) ile histogram kendi fetch'i aynı seriyi ayrı çeker; `onSeries` kancası (`LogsHistogram.tsx:58-66`) /logs'ta bağlanmamış — severity=0 iken çift tur.

### 4. Satır deneyimi (LogTable)
- **Genişletme = doc viewer, Table/JSON sekmeleri** (yapışkan per-row; `components/LogTable.tsx:344-347,494-500`): Table = JSON-ise pretty-print gövde (`prettyMaybe` 200KB cap `:111-125`) + attribute kv-tablosu ⊕/⊖'lı + katlanır Resource bölümü (`:511-546`); JSON = tel-şekli tam kayıt + kopyala (`:547-566`).
- **Vurgu**: SADECE istemci tarafı `<mark>` — uygulanmış serbest-metin terim/deyimleri, field-clause'lar bilerek hariç, ES highlight API'sine bilerek girilmiyor (`lib/logFilters.ts:64-131`, 4KB tarama cap'i `:98`; render `LogTable.tsx:393-396`).
- **Sütun özelleştirme**: dinamik orta kolonlar; fields panelinden ekle/çıkar + başlıkta hover-× (`LogTable.tsx:263-287`); Message konumlanabilir ama kaldırılamaz (`:33-46`); ?cols= deep-link + localStorage tercih (`Logs.tsx:186-204,314-321`). Sürükle-yeniden-sırala YOK (sıra = ekleme sırası).
- **Sıralama**: kolon sort bilinçli YOK (server-paged, resize-only — `LogTable.tsx:20-24`; CLAUDE.md server-paged muafiyeti); zaman yönü toggle'ı VAR "↓ newest / ↑ oldest first", URL'de, cursor reset'li (`Logs.tsx:243-261,1149-1156`).
- **BAĞLAM görünümü VAR** (Kibana surrounding documents karşılığı): "≡ View ±50 surrounding context" (`LogTable.tsx:502-508`) → `LogContextModal` — pivot ±50, aynı servis, 30dk simetrik pencere, sarı pivot şeridi, satırdan trace peek (`components/LogContextModal.tsx:9-26,110-165`). N ayarlanamaz (sabit 50), modal içi süzgeç/genişletme yok.
- Diğer: j/k klavye nav + Enter/o genişlet (`Logs.tsx:620-623`), trace kolonu link+kopyala+👁 peek → CorrelationContextDrawer (`LogTable.tsx:449-477`; `Logs.tsx:1231-1233`), satır severity renk şeridi (`LogTable.tsx:373`), content-visibility 28px (`:374-378`), span-event rozetli dürüst satır (`:384-392`), **"narrow within results"** yerel süzgeç (sıfır ES turu, kasıtlı URL-dışı; `Logs.tsx:236-239,606-612,1089-1170`), CSV/NDJSON export (yalnız yüklü satırlar, kasıtlı; `:1118-1141`), ACC_CAP 2000 + düşen satır beyanı (`:76,496-516,1213-1224`), dürüstlük çipleri: envUnapplied (`:795-809`), hasTraceUnapplied (`:832-845`), ES partial/timeout/shard (`:847-863`), total "gte" → Pager 'capped' (`:1199-1207`).

### 5. Filtre pill'leri
- Kibana Discover pill modeli (`lib/logFilters.ts:1-17`): **negatifle (≠), geçici kapat (◐, sorgudan düşer görselde kalır), kaldır (×), Clear all** — hepsi auto-apply (`Logs.tsx:865-908,566-573`). Toggle semantiği: aynı polarite=kaldır, zıt=çevir+re-enable (`logFilters.ts:47-54`).
- Kaynaklar: genişletilmiş satır KvRow ⊕/⊖ (`Logs.tsx:575-580`), fields paneli top-değerleri.
- Derleme: pill'ler + serbest metin → TEK KQL string, backend sözleşmesi değişmez; disabled pill hiçbir tura girmez (`logFilters.ts:33-40`; `Logs.tsx:344-347`). URL `?filters=` kalıcı (`logFilters.ts:59-62`; `lib/logsUrl.ts:28-53` sig-guard üçlüsü).
- **Yerinde DÜZENLEME YOK** (Kibana'nın edit-filter popup'ı: key/operatör/değer değiştirme) — yalnız kaldır+yeniden ekle; pill'ler arası OR kombinasyonu yok (hep AND).

### 6. Canlı takip / kayıtlı arama / paylaşım
- **Live tail VAR, SSE**: `/api/logs/stream` EventSource, filtreler sunucuya geçer, LIVE_CAP 1000, id-dedup, `since` ile reconnect catch-up, `gap` event'i "high volume — lines skipped" rozeti, document.hidden'da kapat/görününce aç (`Logs.tsx:425-479,781-792`). Live'da sort ve pager gizli (`:1149,1205`).
- **Kayıtlı aramalar VAR**: `SavedViewsBar page="logs"` (`Logs.tsx:645`) — URL query-string'i saklar (pill+cols+severity dahil), kişisel + admin-paylaşımlı, "● modified"+revert, 1-9 kısayolları (`components/SavedViewsBar.tsx:26-60`).
- **Paylaşım**: ShareButton = mevcut URL'yi kopyala; tüm filtre durumu URL'de, alıcı Coremetry'ye giriş yapar — public link bilinçli yok (`Logs.tsx:48-60,780`). Ek: **↗ Discover in Kibana** derin linki — filtre→KQL + pencere→zaman aralığı taşır, Settings'te Kibana yoksa hiç çizilmez (`Logs.tsx:654-673`; `lib/kibanaLink.ts`).

### 7. Desenler (Drain)
- **/logs'ta etkileşimli Patterns sekmesi/listesi YOK.** Drain şablonları backend'de yaşıyor (`chstore.ListLogTemplates`) ama frontend'de hiçbir yüzey tüketmiyor (grep: 0 sonuç).
- Mevcut iki dolaylı yüzey: (a) **"✨ Desenleri anlat"** butonu — tıkla-yalnız fetch, `/api/copilot/explain-log-patterns`: anomaly.DetectLogPatterns (yeni/patlayan, batched _msearch) + Drain ListLogTemplates (sürekli gürültü) → AI markdown anlatım + 👍/👎 (`Logs.tsx:166-178,647-653,811-830`; `internal/api/log_patterns_explain.go:14-50`); (b) anomali akışındaki log-pattern bölümü /logs dışında (`features/anomalies/streams.tsx:53,102`).

## Kibana'ya kıyasla EKSİKLER
1. **Patterns sekmesi**: desen listesi (şablon + sayı + trend + tıkla-filtrele) yok; yalnız AI anlatımı. En büyük Kibana-parite açığı.
2. **Sorgu editörü**: sözdizimi vurgulama, yazarken doğrulama, operatör (AND/OR/NOT) önerisi, son-aramalar geçmişi yok; NL→KQL /logs'ta yok (yalnız /explore, o da DSL üretir).
3. **Alan kataloğu 500-cap**: cap alfabetik, panel içi arama ve autocomplete yalnız kırpık liste üstünde İSTEMCİ tarafı — cap dışı alan hiçbir yoldan bulunamaz (dürüstçe beyan ediliyor ama sunucu-tarafı alan araması yok; `LogFieldsPanel.tsx:272-277` başlık metni "type above to find one that isn't shown" bu yüzden fazla vaat). Alan tip ikonları, sayısal/tarih dağılımları, "Visualize" sıçraması, alan satırından exists-filtresi yok.
4. **Filtre pill düzenleme**: yerinde key/op/value edit popup'ı yok; OR kombinasyonu/custom DSL pill yok.
5. **Bağlam görünümü**: ±N sabit 50 + yalnız aynı-servis kapsamı; Kibana'nın "load 5 more" artımlı genişletmesi ve bağlam-içi filtre/genişletme yok (`LogContextModal.tsx:49-54`).
6. **Vurgu**: yalnız serbest-metin terimleri, istemci tarafı; field-clause eşleşmeleri işaretlenmez (bilinçli — "ES highlight API'sine girme" spec'i, `logFilters.ts:70-71`).
7. **Histogram**: tam per-severity stacked görünüm yok (bilinçli v0.9.218 redesign), tek kovaya tıkla-zoom yok.
8. **Kolon sıralama/taşıma**: kolon sort bilinçli yok (server-paged muafiyeti), sürükle-reorder yok.
9. **Export/rapor**: tam-sorgu CSV / zamanlanmış rapor yok (yalnız yüklü satırlar — bilinçli ES-maliyet kararı, `Logs.tsx:1118-1124`).
10. Küçük gözlem: severity-chip sorgusu ile histogram aynı `logsTimeseries` dilimini iki ayrı turda çekiyor (severity=0 iken birebir aynı); `LogsHistogram.onSeries` (v0.9.358) kancası /logs'ta bağlı değil (`Logs.tsx:395-412` vs `:1028`).

"Eksik" sanılmaması gerekenler (VAR): surrounding context (±50), live tail (SSE), saved searches, tıkla-filtrele ⊕/⊖ her yüzeyde, JSON doc viewer, sütun özelleştirme + ?cols= deep-link, match highlight, brush-zoom + çift-tık geri, alan adı+değer autocomplete, Kibana deep-link, dürüstlük çipleri (partial/env/hasTrace/capped-total/dropped-rows).

---

# EK — Backend yetenek haritası

# Log arama BACKEND yetenek haritası (koddan doğrulandı)

## 1. `logstore.Store` arayüzü — internal/logstore/logstore.go:362-470
```go
Search(ctx, Filter) (*Page, error)                                          // :363
CountPatterns(ctx, []PatternSpec, curStart, baseStart, now) ([]PatternStats, error) // :374 (ES: _msearch batch; CH: sıralı)
Histogram(ctx, Filter, bucketSec int, groupBy string) ([]LogSeries, error)  // :382
EQLSearch(ctx, EQLQuery) ([]EQLSequence, error)                             // :393 (CH: typed "unsupported")
RawSearch(ctx, indices, body json.RawMessage, trackTotalCap) (int64, error) // :406 (watcher Faz-1; CH stub)
RawSearchPayload(...) (json.RawMessage, int64, error)                       // :415 (watcher Faz-2 agg; CH stub)
RawSearchSamples(ctx, indices, body, n) ([]string, error)                   // :425 (CH stub)
Indices(ctx) ([]IndexInfo, error)                                           // :433 (CH: nil)
FieldValues(ctx, field, prefix, limit, from, to) ([]string, error)          // :450 (CH: boş stub)
FieldStats(ctx, Filter, field, limit) (*FieldStatsResult, error)            // :460
Backend() string / Ping(ctx) error                                          // :464/:469
```
- `Filter` (logstore.go:36-124): Service, Cluster, Env, Search, From/To, SeverityMin, TraceID, TraceIDs[], SpanID, HasTrace, Limit, Offset, WantCursor (v0.9.286 PIT niyet beyanı), Cursor (opak keyset), Ascending, SinceNs (forward-tail), envField (ES-internal).
- `Page` dürüstlük zarfı (logstore.go:144-194): NextCursor, EnvUnapplied, HasTraceUnapplied, Partial, ShardsFailed, TotalIsLowerBound — yalnız ES set eder.
- Paket-seviyesi (interface değil): `SearchWithTimeout`/`MapBackendSlow` (:529/:557, ErrBackendSlow→200 degraded sözleşmesi), `LogsForTrace`/`LogsForSpan` (:603/:617, 3s PivotTimeout).
- Opsiyonel yetenekler `Unwrap` type-assert ile (Switchable bilerek FORWARD ETMEZ, switchable.go:17-21,56): `Diagnoser` (:301), `TraceContextDiagnoser` (:357), `ListFields/ListFieldsBounded` (elasticsearch.go:535/552), `ExecSQL` (elasticsearch.go:1182 → api/sql_playground.go:266-295).
- Runtime backend takası: `Switchable` + ESManager (switchable.go:22; es_config_persist.go).

## 2. Arama / sorgu dili
**ES** — `buildQuery` elasticsearch.go:2379-2660:
- Serbest metin → Lucene **`query_string`** (:2640-2648): `default_field`=Body, `default_operator:"AND"`, `allow_leading_wildcard:false` (perf), `lenient:true`. Yani `field:value`, AND/OR/NOT, trailing-wildcard (`c9ea*`) destekli; **leading wildcard kapalı**; regex sorgusu YOK.
- Kısaltma genişletici `expandShorthand` (:2665-2725): `level:`/`service:`/`trace:`/`pod:`/`container:`/`namespace:`/`cluster:`/`host:`… → çoklu-şekil OR grubu (`level:error` → `(log.level:error OR level:error OR severity:error …)`).
- **Case**: `case_insensitive` query_string'te YOK — v0.5.201'de eklendi, ES 8.x reddedince v0.5.231'de kaldırıldı (:2629-2639). Text-analyzed alanlar analyzer'la fold eder; keyword alanlar exact-case.
- Filtreler: service exact-both-shapes + env-eki-soyulmuş değer + container/labels.app fan-out (:2485-2552, `exactTermsBothShapes` :2369); cluster 4 yol dahil `openshift.labels.cluster` (:2560-2591); env self-discovered field (:2602-2609, es_env_field.go:182); trace/span `traceTermsAny` = 4 yazım + body `match` + `multi_match` catch-all (:2984-3044); TraceIDs `terms` (:2429); HasTrace `exists` fan-out (:2465).
- **Highlight: ES highlight API'si KULLANILMIYOR** (logstore+api_logs'ta 0 grep hit). Vurgu tamamen istemci tarafı `<mark>` (frontend/src/pages/Logs.tsx:352-355 `extractHighlightTerms`, "Client-side matching only").
- Sayfalama: PIT + `search_after` `_shard_doc` tiebreak (:1328-1450); PIT yalnız `WantCursor` ile açılır (:1368-1381), 403'te kalıcı plain-paging'e düşer (:1506-1521); keepAlive 2m (:89). Uzun ES cursor'ları için `POST /api/logs/search` (api_logs.go:276-303, 1MB gövde).
- Maliyet: `track_total_hits:10000` + soft `timeout:10s` + `request_cache:false` + docvalue epoch_millis (:1435-1481); trace lookup'ta sayfa-1 `terminate_after` (:1451-1467); pencere yoksa son 10 dk clamp (:1305, es_indices.go:182); indeks daraltma dailies (queryIndices, es_indices.go:273-308).

**CH** — iki AYRI yol:
- Liste (`CHStore.Search` → `chstore.GetLogs`, repo.go:4169-4369): serbest metin = **`body LIKE '%q%'`** (repo.go:4187) — TEK literal substring, **case-SENSITIVE**, `field:value` semantiği YOK. Keyset cursor cityHash64 rowKey (:4120-4144), count ayrı sorgu `max_execution_time=25` (:4257), limit ≤1000 (:3993).
- Histogram/FieldStats (clickhouse.go:219-240, 534-544): **`multiSearchAnyCaseInsensitive(body,[?])`** (tokenbf_v1 index, store.go:999) + **bare-hex 32/16 id → `OR trace_id=? OR span_id=?`** yükseltmesi (v0.8.521; `isBareHexID` trace_fallback.go:238).

## 3. Alan istatistikleri (FieldStats)
- Uç: `GET /api/logs/fieldstats` (api.go:786; api_logs.go:857-900) — yalnız accordion **expand'inde** fetch, **60s cache**, 20s ctx, ErrBackendSlow→200 `{degraded:true}`.
- ES (elasticsearch.go:1034-1136): tek bounded `terms` agg — `size:0`, size≤20 (default 5), `shard_size:size*10`, `track_total_hits:false`, timeout 10s, **`request_cache:true`**; `.keyword`-önce + boşsa bare-field tek retry.
- CH (clickhouse.go:491-589): GROUP BY + window-fn tek taramada top-N + toplam; `max_execution_time=15`, **`max_rows_to_group_by=100000` + `group_by_overflow_mode='any'`**; attr yolu res-array indexOf (:138).
- FieldValues'tan farklıdır (bkz. §6).

## 4. Bağlam sorgusu — VAR
- `GET /api/logs/context` (api.go:795; api_logs.go:745-841): pivot ts ±30 dk, iki paralel Search — before=DESC LIMIT n, after=**Ascending** LIMIT n (v0.7.83, her iki backend :101/repo.go:3974); n≤200, 15s cache, yarı-pencere başına 10s bütçe (:43), degrade→200.
- ES'te ZATEN ucuz kurulu: cursorsuz Search **PIT açmaz** (v0.9.361, elasticsearch.go:1368-1375), sort `[timestamp, _doc]` + bounded range; her yarı tek LIMIT-n okuma olduğundan search_after'a gerek yok. Filtre kapsamı yalnız service+env — search/severity/cluster context'e taşınmıyor (Kibana "surrounding documents"ta filtre korunumu opsiyoneldir; burada hiç yok).

## 5. Histogram/timeseries maliyet şekli
- Uç: `GET /api/logs/timeseries` (api_logs.go:902-969) — 30s cache, bucketSec [1,86400] + **`floorBucketByWindow` ≤5000 kova** (:230-256, v0.9.287), 30s Go tavanı, degrade→boş dizi (şekilde flag yeri yok; bayrağı eşlik eden liste yanıtı taşır).
- ES (elasticsearch.go:1734-2080): `size:0` + `date_histogram fixed_interval` + **`min_doc_count:1`** (v0.8.3 dense-grid olayı) + `track_total_hits:false` + timeout 10s + **`request_cache:true`** (:1964). groupBy=service: terms size 50/shard_size 500 + `total_buckets` yan-agg → **OTHER bandı sentezi** (:2041-2078). groupBy=severity: 5 kanonik bant keyed `filters` agg (prefix case_insensitive × aday alanlar + severity_number range, :1876-1914).
- CH (clickhouse.go:178-318): top-20 grup CTE + gruplu count, `max_execution_time=25`, `distributed_product_mode='global'`; sparse çıktı.
- CountPatterns (dedektörler; anomaly/log_patterns.go + ai/insight/signals.go): ES tek `_msearch`, gövde başına timeout + `track_total_hits:false` (:2100-2330); CH tokenbf-önfiltre + regex `match()` sıralı (clickhouse.go:326-406).

## 6. Autocomplete uçları
- **Alan ADLARI**: `GET /api/logs/fields` (api.go:782; api_logs.go:501-544, 60s cache) ← ES `ListFieldsBounded` mapping-walk, cap 500 + konvansiyonel alan geri-ekleme (elasticsearch.go:552-592, 3148). CH implement etmez → boş + total.
- **Alan DEĞERLERİ**: `GET /api/logs/field-values` (api.go:790; api_logs.go:648-697) ← ES **`_terms_enum`** (elasticsearch.go:964-1023): `case_insensitive:true`, `index_filter` range ile shard-skip, `.keyword`-önce; limit ≤50. ES maliyet notları: pencere ≤7g + **saate SNAP'li** cache key (yoksa `to=now` her girişi tekil key yapar), 30s cache, istemci 180ms debounce; v0.9.291 öncesi tüm retention'ın term dictionary'sini yürüyordu. CH: stub `nil` (clickhouse.go:476-482) — autocomplete CH'de hiç görünmez.

## 7. İki impl arasında YETENEK FARKLARI
| Yetenek | ES | CH | Kanıt |
|---|---|---|---|
| `field:value`/Lucene sözdizimi | ✓ query_string+shorthand | ✗ literal substring | es:2640 / repo.go:4187 |
| Serbest metin case | insensitive (analyzed) | Liste: **sensitive** LIKE; Histogram: insensitive | repo.go:4187 / clickhouse.go:237 |
| Wildcard (trailing) | ✓ | ✗ | es:2645 |
| EQL sequence | ✓ (:612) | typed hata (:426) | **Hiçbir HTTP route/FE tüketicisi yok** — repo-wide grep yalnız logstore pkg; ölü yüzey |
| Watcher RawSearch×3 | ✓ es_rawsearch.go | stub (clickhouse.go:436-455) | |
| Indices/ILM | ✓ (:853) | nil (:462) | |
| FieldValues autocomplete | ✓ _terms_enum | boş stub (:476) | |
| ListFields(Bounded) / ExecSQL / Diagnoser | ✓ (opsiyonel, Unwrap) | yok → temiz boş/400 | es:552/1182/369 |
| Partial/ShardsFailed/TotalIsLowerBound/EnvUnapplied/HasTraceUnapplied | set eder | asla (exact count) | logstore.go:159-193 |
| Trace-id alan self-discovery | field_caps raporu | sabit kolon raporu | es_trace_context.go / clickhouse.go:624 |

### Tespit edilen GAP'ler (koddan doğrulandı, mevcut değil)
1. **CH listesi `Filter.Cluster`'ı sessizce yok sayar**: `chstore.LogFilter`'da Cluster alanı hiç yok (repo.go:3952-3982), `CHStore.Search` map'lemiyor (clickhouse.go:29-45) — ama CH Histogram+FieldStats cluster'ı uyguluyor (clickhouse.go:202,520). CH backend'de /logs: histogram daralır, altındaki tablo daralMAZ (v0.9.216'nın ayna-görüntüsü, backend tarafında).
2. **CH listesinde v0.8.521 bare-hex yükseltmesi YOK**: commit 3c81bfa5 yalnız clickhouse.go Histogram+FieldStats'ı düzeltti; liste yolu `GetLogs` (repo.go:4186-4188) hâlâ salt `LIKE` — id'yi body'ye yazmayan CH kurulumunda hex yapıştırınca histogram dolu, liste boş.
3. **CH listesi case-sensitive, CH histogramı insensitive** — aynı ekranda iki farklı popülasyon (aynı sınıf).
4. `Filter.TraceIDs` CH Search'te düşer (LogFilter'da alan yok); bugün tek tüketici DQL join Histogram yolunda (api/dql.go:139-173) olduğundan latent.
5. ES highlight API'si kullanılmıyor (bilinçli görünürlük: istemci `<mark>`; server-side vurgu istenirse Search gövdesine `highlight` bloku eklenebilir — bugün yok).