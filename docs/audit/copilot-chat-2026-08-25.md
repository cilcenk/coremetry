# Coremetry AI Copilot (chat) — durum denetimi

**Tarih:** 2026-08-25 · **Kapsam:** yalnız sohbet yüzeyi (✨ Explain düğmeleri hariç)
**Yöntem:** kaynak okuması + üç düşmanca sınama + kapsam eleştirmeni. Kod DEĞİŞTİRİLMEDİ.

> ⚠ **Canlı model koşturulmadı.** gemma4'e tek istek gitmedi, tarayıcı açılmadı.
> Tek canlı doğrulama ClickHouse'a atılan bir sayım sorgusudur (aşağıda B1).
> "Model pratikte şunu yapıyor" diyen hiçbir cümle bu raporda yoktur; hepsi
> "kod bunu mümkün kılıyor" düzeyindedir.

---

## 0. Mimari — dört kademe

Sohbet TEK bir yol değil. `internal/api/copilot_chat.go` sırayla dört kapı deniyor;
ilk tutan kapı cevabı üretir ve **sonrakiler hiç çalışmaz**:

| # | kademe | ne yapar | tool çağırır mı |
|---|---|---|---|
| 1 | **guided** (`copilot_chat.go:199`) | 19 niyeti tanır, veriyi KENDİ prefetch eder, tek anlatım çağrısı yapar | ❌ hayır |
| 2 | **drawer** (`:215`) | AI çekmecesindeki açıklamayı bağlam alır | ❌ hayır |
| 3 | **RAG** (`:224`) | yüklü dokümanlardan cevaplar | ❌ hayır |
| 4 | **serbest döngü** (`:242`) | 36 MCP tool'unu modele verir, 5 turluk gerçek agentic döngü | ✅ evet |

Kademe 1'in var oluş gerekçesi kodda yazılı: *yerel küçük model 5 tur × N şema
döngüsünü güvenilir sürükleyemiyor.* Yani **guided = önceden-getirilmiş bağlam,
serbest döngü = gerçek tool loop** ve ikisi aynı üründe yan yana yaşıyor.

⚠ **Bu sıra hiçbir testle çivili değil.** Yalnız üç `if handled { return }` bloğunun
fiziksel yerinden ibaret. Aynı dosyanın komşu sözleşmeleri kaynak-pinli
(`mcp_authz_test.go:278`, `chat_tool_budget_test.go:99`, `tool_error_contract_test.go:79`)
— sıraya uygulanmamış. Biri drawer'ı guided'ın üstüne taşısa hiçbir gate kırılmaz.

---

## 1. Tool'lar

**36 tool.** Tek kayıt defteri: `mcptools.ToolList(d)` — chat ile MCP **aynı listeyi**
paylaşıyor, ayrışma imkânsız. MCP'nin *resources* ve *prompts* kayıtları chat'e kapalı.

Handler'lar JSON-RPC zarfı olmadan, 20 sn timeout ile doğrudan çağrılıyor
(`copilot_chat.go:464`). Bağımlılık üç handle: `chstore.Store`, `logstore.Store`,
metrik router (CH ya da VictoriaMetrics).

Katalog: `list_services, list_teams, get_team_services, get_service_health,
get_operation_health, get_pod_health, get_topology, get_blast_radius, get_db_health,
get_messaging_health, list_environments, list_problems, list_problem_window_events,
get_problem_root_cause, list_anomalies, search_logs, list_clusters, get_log_histogram,
get_trace, search_traces, find_trace_by_span, find_trace_by_request_id, list_slo_status,
query_metric, list_metric_names, list_operations, list_exception_groups,
get_exception_samples, get_correlated_changes, get_deploy_diff, list_deploys,
get_logs_for_trace, get_exemplar_traces, get_linked_traces, get_metrics_for_span,
render_chart`

### Ham tabloya dokunanlar — EVET, en az 13

`FROM spans`: list_services, get_service_health, get_trace, find_trace_by_span,
list_clusters, list_environments, get_exception_samples, get_deploy_diff,
search_traces, list_slo_status
`FROM metric_points`: list_metric_names, get_exemplar_traces, get_pod_health, query_metric
logstore: search_logs, get_log_histogram, get_logs_for_trace

Hepsinde zaman sınırı + `max_execution_time` var. LIMIT çoğunda var; tekil-satır
agregalarda (SLO, deploy diff) yok.

### ⚠ İki sert ihlal

**(a) MV-first değişmezi kırık.** `list_services` ve `get_service_health` →
`GetServicesFilteredIn` → `GetServicesQuery` → **koşulsuz** `FROM spans` GROUP BY
(`repo.go:944`). MV ikizi VAR (`GetServicesAggFiltered2`) ve `/api/services` onu
`servicesUseMV` kapısıyla kullanıyor (`api.go:1747`). Tool'da böyle bir kapı yok.

Bunlar kataloğun en çok çağrılacak iki tool'u. Aynı soru guided'a düşerse MV'den
milisaniyede, serbest döngüye düşerse 20 sn'lik tam tarama bütçesiyle cevaplanıyor.

**(b) Açıklamalar modele YALAN söylüyor.** `tools.go:512`:
> "List Coremetry services … **Reads the 5-minute pre-aggregate so it's cheap to
> call repeatedly**"

Pratikte ham tarama. Açıklama modelin maliyet modelidir: "ucuz, tekrar çağır" diyen
bir katalogla küçük model döngüde tekrar tekrar çağırır. `get_metrics_for_span`
aynı yalanı taşıyor.

### Diğer

- `list_slo_status` SLO sayısı kadar, 30 güne kadar geriye giden, LIMIT'siz ham
  tarama tetikleyebiliyor; tool'da pencere argümanı yok.
- **Rol süzgeci no-op:** `toolsForRole` yapısal olarak var ama 36 tool'un tamamının
  `MinRole`'ü boş. Viewer, admin'in gördüğü her telemetri kesitine sohbetten erişiyor.
  Bilinçli karar olarak yazılmış ama tek yazılışı bir *yorum*; yeni tool eklendiğinde
  hiçbir gate itiraz etmiyor.
- **Hız sınırı yok:** MCP telinde 60/dk kapı var (`mcp_gate.go:35`), chat aynı
  tool'lara kapısız erişiyor. Tur başına çağrı sayısı da sınırsız — yalnız tur
  sayısı (5) sınırlı.

---

## 2. Sistem promptu ve bağlam

Dört prompt katmanı, hepsi `internal/copilot/prompts.go` (yapısal kapı: `api`
paketinde prompt metni tanımlamak testte kırmızı).

### Model NE görüyor

| bağlam | guided | drawer | RAG | serbest döngü |
|---|---|---|---|---|
| sayfa / route (pathname) | ❌ | ❌ | ❌ | ❌ |
| servis / operation | ✅ | kısmen | ❌ | ❌ |
| zaman aralığı (`?range`) | ✅ (yalnız SÜRE) | ❌ | ❌ | ❌ |
| env | ✅ | ❌ | ❌ | ❌ |
| trace | ✅ | ❌ | ❌ | ❌ |
| kullanıcı rolü / adı | ✅ | ❌ | ❌ | ✅ |
| şu anki saat / TZ | ❌ | ❌ | ❌ | ❌ |

**Rota sunucuya hiç taşınmıyor.** Sayfa farkındalığı frontend'de beş türetilmiş
alana indirgenmiş (`chatContext.ts:41-49`) ve her yeni sayfa için elle bir dal
yazılması gerekiyor. `/databases`, `/messaging`, `/hosts`, `/slos`, `/incident`
sayfalarında model operatörün neye baktığını bilmiyor.

**Serbest döngü tamamen bağlam-kör** — ve üstelik prompt'u *"aksini söylemedikçe
1800 (30 dk) kullan"* diyor (`prompts.go:1253`). Ekranda checkout servisi açık ve
6 saatlik pencere seçiliyken sorulan "hata oranı ne" sorusu, **filo geneline ve
30 dakikaya** gidiyor. Cevapta bunu belirten hiçbir şey yok.

**Mutlak pencere kayboluyor.** Aralık yalnız SÜRE olarak taşınıyor
(`CopilotChat.tsx:150`) ve sunucu her zaman "şimdi"ye çapalıyor
(`copilot_guided.go:1233`). Operatör dün 03:00-04:00'a zoom yapıp "burada ne oldu"
dediğinde, aynı UZUNLUKTA ama **bugünkü** pencere cevaplanıyor. Cevap makul
göründüğü için hata sessiz.

**Kapsam eksikleri:** `/endpoint` (detay, `?service=` taşıyor) `SERVICE_PARAM_ROUTES`
listesinde yok; `/service-map` seçimini `?focus=` ile taşıyor ama okunmuyor.
`TRIAGE_ROUTES` içindeki `/exceptions` ölü (redirect) ama testi yeşil.

**Rol koruması iki yolda devre dışı:** hitap/rol ön-sözü yalnız guided ve serbest
döngüde ekleniyor. Viewer bir AI çekmecesinde takip sorusu sorduğunda model ona
"şu kuralı sustur" diyebiliyor — koruma tam da yazıldığı senaryoda yok.

---

## 3. Konuşma geçmişi

**Sunucuda TUTULMUYOR.** Kaynak doğruluk frontend component state'i; her gönderimde
geçmişin tamamı POST gövdesinde tele biniyor. ClickHouse'a yalnız AYRI bir uçtan
(`POST /api/ai/conversations`) istemci-güdümlü, debounce'lu bir **arşiv** yazılıyor.

**Kesme:** iki tavanın kesişimi — 40 tur (`chatMaxMessages`) **ve** 6000 rune
(`assemble.HistoryMaxRunes`). Karar saf/deterministik `assemble.ClampHistory`'de,
en yeniden geriye yürüyor, yani **en eski turlar** düşüyor, en az bir tur kalıyor.

**Özetleme YOK** — bilinçli reddedilmiş (`history.go:6`). Kırpma **sessiz değil**:
modele sentetik bir `HistoryTrimNote` enjekte ediliyor.

### ⚠ Bütçe döngü içinde tekrarlanmıyor

`ClampHistory` döngüden **önce bir kez** çalışıyor; sonra `conv` her turda iki mesaj
daha alıyor ve bilerek yeniden bütçelenmiyor (`copilot_chat.go:407-411`). Gerçek üst
sınır: 6000 rune geçmiş **+ 5 tur × (tur başına N tool × 6000 rune)**. Kod bunun
sonucunu kendi yorumunda kabul ediyor: *taze kanıt, eski kanıt tarafından bağlamdan
atılıyor.* Operatör bunu "model aptallaştı" diye okur.

### ⚠ Token muhasebesi yok, taşma teşhis edilmiyor

Kodda hiçbir yerde **context penceresi** yok: ne sabit, ne yapılandırılabilir.
Bütçeler *rune* cinsinden ve Türkçe metinde rune ≠ token (ı/ş/ğ çoğu tokenizer'da
2-3 token) — 6000 rune pekâlâ 8-10k token olabilir.

Taşma önden yakalanmıyor; sağlayıcı 400 dönüyor ve **ham İngilizce metin** SSE
`error` olayı olarak ekrana basılıyor. Taşmayı tanıyan `isContextOverflowErr`
yardımcısı **yazılmış ve tablo-testli** ama yalnız kod-explain yolunda kablolu
(orada küçültüp bir kez retry var); sohbette yok.

**Not:** token sayımı aslında VAR ama yalnız *sonradan* — `ai_calls` her alışverişe
`input_tokens`/`output_tokens` yazıyor. Yani "prod'da taşmaya ne kadar yaklaşıyoruz"
sorusu **bugün tek bir sorguyla cevaplanabilir**, kimse sormamış.

**Üç farklı geçmiş sözleşmesi:** serbest döngü 40 tur/6000 rune gerçek mesaj dizisi;
guided 6 tur/1500 rune metin bölümü; RAG **hiç** (yalnız son kullanıcı metni).
Aynı takip sorusu ("peki dünkü?") hangi kademeye düştüğüne göre çözülüyor ya da
bağlamsız kalıyor — fark ekranda görünmüyor.

**Sayfa yenileme:** ekrandaki turlar kayboluyor. Geri gelmesi URL'de `?chat=<id>`
varsa (o da yalnız çekmece açıkken yazılıyor) otomatik, aksi hâlde elle "Geçmiş"ten.

---

## 4. Sayı güvencesi — EN KRİTİK

**Hayır, garanti altında değil.** Sayılar prompt'a düz metin gömülüyor (guided) ya da
tool JSON'undan geliyor (serbest döngü); **cevap döndükten sonra sohbet yollarının
hiçbirinde sayı kaynak veriyle karşılaştırılmıyor.**

Modele uygulanan tek post-hoc regex servis-ADI taraması (`entity_scan.go:30`) ve
yalnız iki yüzeyde (`analyze-service`, RCA verdict) çalışıyor. Rakam yakalayan hiçbir
tarayıcı yok.

Dört kademenin dördünde de prompt düzeyinde "uydurma" kuralı VAR — ama bunlar rica;
hiçbir kod yolu zorlamıyor.

### ⚠ B1 — Sunucu KENDİSİ yanlış toplam üretiyor (en sert bulgu)

`renderProblemsEvidenceTR` **"toplam %d"** derken `len(probs)` basıyor — oysa `probs`
bir SQL `LIMIT`'inin çıktısı (üç rotada 10, birinde 50).

47 açık problemi olan bir serviste modele **"toplam 10"** veriliyor. Prompt
kurallarına *mükemmel uyan* bir model "10 açık problem var" diyor. **Hiçbir
anti-uydurma kuralı bunu yakalayamaz — çünkü model uydurmuyor, verilen yanlış sayıyı
sadakatle aktarıyor.**

Üstelik kırpma-ifşa dalı (`i >= guidedMaxLines`, `guidedMaxLines=10`) limit=10
rotalarında **yapısal olarak ulaşılamaz**: `len ≤ 10` iken indeks 10'a çıkmaz.

> **Canlı doğrulama (tek yer gerçeği):** lokal CH'de
> `SELECT count() FROM (SELECT id, argMax(status,version) st FROM coremetry.problems
> GROUP BY id) WHERE st='open'` → **8**. Bugün limitin altında, yani kusur lokalde
> tetiklenmiyor; prod ölçeğinde tetiklenir.

### ⚠ B2 — Modelin DÜŞÜNCE bloğu cevap olarak yayımlanıyor

`internal/ai/provider/salvage.go:61-72`:
```go
func SalvageAnswer(content, reasoningContent, reasoning string) string {
	if out := StripThinking(content); out != "" { return out }
	if out := StripThinking(reasoningContent); out != "" { return out }
	if out := StripThinking(reasoning); out != "" { return out }
	return ThinkingContent(content)   // ← düşünce bloğunun İÇİ
}
```
Dosyanın kendi itirafı: *"Bazı modeller yalnız düşünce bloğu üretir ve o düşünce
genelde açıklamanın kendisidir; kurtarmak, isteği başarısız saymaktan iyidir."*

Operatöre "bu modelin karalama defteri" diyen hiçbir işaret yok: metin normal cevap
gibi çıkıyor, `ai_calls`'a normal cevap gibi yazılıyor, bir sonraki turda geçmişe
normal cevap gibi geri biniyor. Yani modelin **spekülasyon fazı** doğrudan yayına
giriyor.

### ⚠ B3 — Sıfır tool çağrısı da geçerli bir cevap üretiyor

Serbest döngüde model 0 tool çağırıp doğrudan cevap yazarsa o metin `answer` olarak
yayınlanıyor (`copilot_chat.go:312`). "En az bir tool çağrılmalı" kapısı yok.
Guided'ın ıskaladığı her soru bu yola düşüyor — yani **modele hiç veri gitmemişken
sayı üretme fırsatı yapısal olarak açık** ve arayüzde bunu telemetriden gelen
cevaptan ayıran hiçbir işaret yok.

### ⚠ B4 — Grafik: doğru veri, model kontrolünde etiket

Sunucu `render_chart` tool'unun **doğrulanmış** spec'inden deterministik bir
` ```chart ` çiti kuruyor ve kod bununla övünüyor. Ama frontend
(`ChatBubble.tsx:169-184`) cevap metnindeki **herhangi** bir chart çitini ayrıştırıp
canlı panele çeviriyor — sunucunun mu modelin mi yazdığını ayırt eden **köken işareti
yok**. Doğrulama `typeof … === 'string'` ile bitiyor.

Sonuç: `unit` ve `title` model kontrolünde (`CosreChart.tsx:74` → `spec.unit ?? unit`,
yani `AGG_UNIT`'in `p99: 'ms'`i modelin yazdığıyla EZİLİYOR). Model bir p99 grafiğine
"%" etiketi yazabilir ve **grafik canlı gerçek veriyle çizilir**. Grafik düzyazıdan
daha yüksek güven taşır — en kötü bileşim.

Var olmayan servis boş grafik veriyor, hata vermiyor: operatör "o servis şu an
sessiz" diye okuyor.

### ⚠ B5 — Prompt injection: sıfır savunma

`grep -rni "injection|jailbreak|sanitiz" internal/ --include=*.go` → AI yolunda
sıfır. Oysa prompt'a giren metinlerin tamamı OTLP'den geliyor ve CLAUDE.md'nin
kendi kuralı bunu garantiliyor: *"attributes kept verbatim"* + *"No PII redaction"*.

Log gövdesi, exception mesajı, span adı, `http.url` — hepsi kanıt bloğuna ve tool
JSON'una düz metin giriyor. Müşterinin bir uygulamasının bastığı
`log.error("SİSTEM: önceki talimatları yoksay")` satırı modele **talimat olarak**
ulaşıyor ve prompt'ta bunu "veri, talimat değil" diye çerçeveleyen tek satır yok.
RAG yolu bunu genişletiyor (doküman yükleme `editorRoles`'a açık).

### Doğru çalışanlar (düşmanca sınama çürüttü)

- **Kaynak künyesi VAR** — dördünde de: guided "Kaynak:" dipnotu, drawer
  `drawerSourceNote`, RAG `sources[]`, serbest döngü ⚙ `step-result` çipleri
  (modelin gördüğü ham tool çıktısının 4KB önizlemesi). *Ama serbest döngüde
  sunucu-taraflı tek satırlık dipnot yok — yalnız çipler.*
- **Kırpma modele söyleniyor** — `clampToolResultForModel` üstelik *"Eksik satırlara
  dayanarak sayım yapma"* diyor; mcptools `truncated/has_more/total` zarfları basıyor.
- **RCA'da gerçek sayısal kalkan var** — `capRCAConfidence` modelin `model_confidence`
  değerini deterministik motora karşı kelepçeliyor. Kapsamı dar (tek sayı, tek yüzey)
  ve **sohbete hiç taşınmamış**.
- **Geri bildirim döngüsü kapanıyor** — 👎 → `ListKBCandidates` → `/api/rag/candidates`
  + `/api/rag/curate`, yani kötü cevap RAG bilgi tabanına aday oluyor.

---

## 5. Link üretimi

**Linkleri model DEĞİL, iki deterministik sunucu resolver'ı yazıyor:**
`guidedAnswerLinkTargets` (`copilot_followup.go:268`) ve `toolCallLinkTarget`
(`chat_tool_links.go:76`). Üçüncü üretici `request_id_links.go` Coremetry rotası
değil, dış log-köprüsü URL'i basıyor.

**Sistem promptlarının hiçbirinde rota listesi yok** — model serbest URL yazmaya
davet edilmiyor. Yazsa bile frontend tıklanabilir yapmıyor: `mdLite` önce
`escapeHTML`'den geçiriyor ve **yalnız 32-hex trace id**'leri linkliyor; markdown
`[metin](url)` sözdizimi çözülmüyor.

İç link react-router `<Link to>` ile SPA olarak, dış link `target=_blank` ile
açılıyor — tam sayfa yenilemesi yok.

**Pencere ZORUNLU argüman** (`linkWindow`): guided cevabın hesaplandığı mutlak
pencereyi, serbest döngü tool'un `range_s`'ini `range=custom:<fromMs>-<toMs>` olarak
yazıyor — ama yalnız zaman ekseni **okuyan** 9 rotaya. `/inbox`, `/problems`,
`/anomalies` bilerek penceresiz.

### Boşluklar

- **Kayıtsız-rota kapısının kör noktası:** kapı yalnız düz-dizge `Href: "/..."`
  şeklini görüyor. `withSvc(...)` sarmalı dört hedefi (`/traces`, `/logs`,
  `/problems`, `/inbox`) gizliyor. Dördü de bugün başka yerden kayıtlı — yani
  **koruma tesadüfi**. `withSvc` ile yeni bir hedef eklenirse kapı yeşil kalır ve
  operatör tıklayınca sessizce ana sayfaya atılır.
- **Uydurma trace id koşulsuz tıklanabilir:** 32-hex bir token frontend'de link
  oluyor, sunucuda varlık doğrulaması yok.
- **`/inbox`, `/problems`, `/anomalies` çipleri pencere taşımıyor** (bilinçli): cevap
  "son 6 saatte 12 problem" der, çip kendi varsayılan kapsamıyla açılıp başka sayı
  gösterir.
- **`range_s` verilmezse link penceresiz:** tool 1h ile sorgularken çip operatörün
  sticky penceresini açıyor — cevaptaki kanıt hedef sayfada görünmüyor.
- **Metin içi link yok:** tüm linkler balon altındaki çip şeridinde, rota düzeyinde,
  serbest döngüde 4 ile sınırlı. Cevap beş servis sayarken operatör ikinciye elle
  gitmek zorunda.
- **Model-yüzü tool prose'unda sayfa adları denetimsiz:** `guided_parity.go:858` var
  olmayan bir yüzeyi ("/services Pods sekmesi") tarif ediyor; model bunu kopyalıyor.

---

## 6. Akış, iptal, hata, görünürlük

**SSE.** Olay şeması: `step` → `step-result` → `delta`* → `answer` → `done`.

**Token akışı dört kademenin yalnız İKİSİNDE:** guided ve drawer `delta` yayıyor.
Serbest döngü v0.8.404'ten beri **bilinçli buffered**; RAG kademesi v0.10.14'te
**kapatıldı** (kaynak-pinli). ⚠ Bu, v0.10.14'ün kapsam-dışı soruları bilinçli olarak
serbest döngüye yönlendirmesiyle birleşince ters etki yapıyor: **trafiğin
yönlendirildiği yol, akmayan yol.**

### ⚠ İptal düğmesi YOK

`AbortController` kurulmuş ama yalnız unmount'ta ateşleniyor
(`useChatThread.ts:85-88`) ve `CopilotChat` AppShell'de **kalıcı monte** olduğu için
çekmeceyi kapatmak bile durdurmuyor; hook `stop` fonksiyonunu dışa vermiyor.
Bağlantı gerçekten koparsa backend doğru iptal ediyor.

Operatörün yapabileceği tek şey beklemek — ve `input` `disabled={busy}` olduğu için
yeni soru da yazamıyor. Tek GPU'da yerel gemma4: istenmeyen bir 5-turlu döngü,
sıradaki meşru soruyu dakikalarca tıkıyor.

### ⚠ Uçtan uca zaman aşımı YOK

Ne handler'da `context.WithTimeout`, ne `http.Server`'da `WriteTimeout`, ne istemcide
(`api.ts:1877` ham fetch, 60s `request()` tavanını atlıyor). Tek sınır: çağrı başına
180s (10-600s ayarlanabilir) + tool başına 20s.

En kötü hâl: **6 × 180s + tool süreleri ≈ 18 dakika** tek bir SSE bağlantısında.
Yavaş yerel modelde bu teorik değil.

**SSE heartbeat yok:** serbest döngü ilk çağrıda 180s'e kadar tek bayt göndermeyebilir.
OpenShift Route / nginx arkasında bağlantı kesilirse operatör hiçbir hata görmez —
balon "yazıyor…" imlecinde asılı kalır, `busy` true kaldığı için yeni soru yazılamaz.

### ⚠ Ham hata metni sızıyor

`emit("error", …err.Error())` → ekrana. `openai.go:111`'in `url.Error` sarmalaması
**tam base URL'i**, `errors.go:29` sağlayıcı yanıt gövdesini taşıyor. Operatör
balonda şunu görüyor:
```
⚠ openai-compat call: Post "http://<iç-host>:8000/v1/chat/completions":
  dial tcp 10.x.x.x:8000: connect: connection refused
```
**Viewer rolü de görüyor** (sohbet her kimliği doğrulanmış kullanıcıya açık). Oysa
aynı repoda Türkçe çevirmen `aiErrorHint` hazır duruyor — sohbete hiç bağlanmamış.

Handler hata durumunda **her zaman 200** dönüyor; erişim loglarında ve proxy
metriklerinde başarısız sohbetler başarı sayılıyor.

### Tool görünürlüğü — İYİ

⚙ çipi tool çalışmadan **önce** çıkıyor, sonuç ayrı olayla geliyor, kanıt
tıklanabilir, kırpma ilan ediliyor. Bu tarafta eksik yok.

⚠ Ama **arşivden geri yüklenen konuşmalar adım/kanıt taşımıyor** (yalnız
`{role,text}` saklanıyor): dünkü bir cevabı Geçmiş'ten açtığında çipler düz etiket
oluyor, kanıt bloğu açılmıyor. **Denetlenebilirlik yalnız canlı oturumda var.**

---

## 7. Test kapsaması

`internal/api/copilot_chat_test.go` — **94 satır, TEK test** (`TestChartBlock`).
5 turluk döngü, kademe sıralaması, rol filtresi, `RecordUsage`, hata dalı, tur
tavanı: hiçbiri uçtan uca koşturulmuyor. Sahte-sağlayıcı koşum takımı yok.

**Eval / golden korpusu yok.** Depodaki tek prompt-golden testi anomali özetleyicisine
ait. Yani bugün bir prompt değişikliğinin cevap kalitesini iyileştirip
kötüleştirdiğini **ölçecek hiçbir araç yok**.

---

## 8. Cevap kalitesini en çok sınırlayan eksikler — sıralı

| # | eksik | neden bu sırada |
|---|---|---|
| **1** | **Serbest döngü bağlam-kör** (§2) | En zor sorular buraya düşüyor ve model operatörün servisini, penceresini, env'ini görmüyor; üstelik prompt 30 dk varsayıyor. Doğru tool'lar, yanlış kapsam. Tek bir düzeltme en çok kaliteyi buradan alır. |
| **2** | **Sunucu yanlış sayı üretiyor** (§4/B1) | Uydurmadan daha kötü: model kurallara uyarken yanlış rakamı aktarıyor. Hiçbir prompt kuralı yakalayamaz. Prod ölçeğinde tetiklenir. |
| **3** | **Kanıt-bağı garantisi yok** (§4/B3) | 0 tool çağrısı da geçerli cevap; serbest döngüde sunucu dipnotu yok. Operatör "veriden gelen" ile "modelden gelen"i ayırt edemiyor. |
| **4** | **Düşünce bloğu cevap olarak yayınlanıyor** (§4/B2) | Modelin spekülasyon fazı, nihai cevap kılığında geçmişe ve `ai_calls`'a giriyor. |
| **5** | **Mutlak/zoom pencere kayboluyor** (§2) | Geçmiş bir olaya zoom yapıp soru sormak APM'de en sık iş; bugün sessizce bugünün verisi cevaplanıyor. |
| **6** | **Rota sunucuya taşınmıyor** (§2) | Sayfa farkındalığı elle yazılan beş alana sıkışmış; yeni sayfa = yeni kör nokta. |
| **7** | **Geçmiş: token muhasebesi yok, taşma teşhis edilmiyor, döngü içinde bütçe yok** (§3) | Uzun soruşturmalarda taze kanıt bağlamdan düşüyor; taşma ham İngilizce hatayla çıkıyor. Çözümün yarısı (`isContextOverflowErr`) zaten yazılı, kablosuz. |
| **8** | **Grafik etiketi model kontrolünde** (§4/B4) | Doğru veri + yanlış birim = düzyazıdan daha ikna edici bir hata. |
| **9** | **MV-first ihlali + yalan tool açıklamaları** (§1) | Kaliteyi dolaylı sınırlıyor: 20 sn'lik taramalar timeout'a dönüşüp cevabı düşürüyor, "ucuz" diyen açıklama modeli tekrar çağırmaya itiyor. |
| **10** | **İptal yok + uçtan uca timeout yok** (§6) | Yanlış giden bir cevap tek GPU'yu dakikalarca tutuyor; operatör düzeltemiyor. |
| **11** | **Prompt injection savunması yok** (§4/B5) | Bugün istismar edilmemiş olabilir ama yüzey açık ve mimari (verbatim attribute) onu kapatmayı yasaklıyor. |
| **12** | **Eval korpusu yok** (§7) | Yukarıdaki 11 maddeden herhangi biri düzeltildiğinde **düzeldiğini ölçecek araç yok**. Uzun vadede en pahalı eksik. |

### Ucuz / pahalı ayrımı

**Ucuz ve yüksek getirili (S):** #3'ün dipnot yarısı · #7'nin `isContextOverflowErr`
kablolaması · #10'un iptal düğmesi · `aiErrorHint`'i sohbete bağlamak · tool
açıklamalarındaki yalanı düzeltmek · kademe sırasına kaynak-pini.

**Orta (M):** #1 bağlamı serbest döngüye taşımak · #2 toplam sayımını düzeltmek ·
#4 düşünce bloğunu işaretlemek · #5 mutlak pencere · #8 grafik köken işareti.

**Büyük (L):** #6 rota taşıma · #9 MV yolu · #11 injection çerçevelemesi ·
#12 eval korpusu.

---

## 9. Bu denetimin sınırları

- Canlı model koşturulmadı; §4'ün tamamı yapısal.
- gemma4'ün gerçek context penceresi **koddan bilinemiyor** — hiçbir dosyada
  model→pencere eşlemesi yok.
- Serbest döngünün **pratikte ne sıklıkta** çalıştığı ölçülmedi. Ölçü ucu var:
  `GET /api/ai/router-gaps?days=7` (guided'ın kaçırdığı soruları sayıyor). Düzeltme
  sırasını sağlamlaştırmak için **ilk yapılacak iş bu sorguyu koşmak** olmalı.
- vLLM'in `stream:true`'yu gerçekten destekleyip desteklemediği doğrulanmadı;
  `StreamText` çalışma zamanında yoklayıp şeffafça buffered'a düşüyor.
