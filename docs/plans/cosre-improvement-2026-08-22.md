# CoSRE iyileştirme planı — 2026-08-22

Operatör hedefi: "CoSRE daha iyi hale getir" + iki netleştirme:
"Coremetry ile daha entegre bir chat deneyimi" · "LLM modelini daha
iyi kullanabiliriz, MCP iyileşebilir".

İki çok-ajanlı denetim (58 doğrulanmış bulgu, 1 çürütme):
- Kod-bağlamı ("Kodu da incele"): 38 bulgu — tam liste
  `/tmp .../tasks/w4x4c81kc.output` (journal: wf_834a2458-472).
- Chat/LLM/MCP: 20 bulgu — `wy7n2i78w.output` (wf_1f24d952-bfd).

## Sıralı dilimler (her biri kendi v0.9.X'i)

GEMİDE:
- ✅ v0.9.1225 — exception kod-çekicisine HAM stack + log-fallback
  (+StackService override; pickExceptionStack saf çekirdek + test).
- ✅ v0.9.1226 — chat servis bağlamı 9 liste rotasından
  (chatContext.serviceFromRoute; sıfır backend).
- ✅ v0.9.1227 — MCP get_operation_health (katalog 33; smoke canlı:
  p99-sıralı satırlar + dürüst truncated).
- ✅ v0.9.1228 — toolCallLink köprüsü (çip "↗ Üründe aç" + cevap
  linkleri; K4-denetimli harita; ham args QueryEscape'siz girmez).
- ✅ v0.9.1229 — guided adım çiplerine kanıt: withStepIDs tek sayaç
  (SSE sarmalayıcı, chat_step_ids.go), 26 guided + drawer/deps/pods/
  shift emit'i çift (step + step-result), yapısal kapı testleri;
  sıfır FE değişikliği. (Shipper ajanı; imaj -dirty→retag, ağaçtaki
  yabancı chstore değişikliklerine dokunulmadı.)
- ✅ v0.9.1230 — katalog diyeti + tool-sonuç tavanı: mcp.Tool.
  ShortDescription (aynı literalde, ChatDescription() erişimi; MCP dış
  yüzeyi TAM İngilizce kaldı — pin testli), 33 kompakt TR açıklama;
  model turu 41.940→23.343 B (−%44,3; şemalar bilinçli dokunulmadı —
  doğru çağrının ön koşulu). chatToolResultMaxRunes=6000, rune-güvenli,
  kırpma İÇERİKTE söylenir; step-result önizlemesi kesilmemiş boyu
  raporlamaya devam eder. 6/6 mutasyon-doğrulamalı test.
- ✅ v0.9.1231 — guided narration'a konuşma geçmişi: guidedHistorySection
  (1500 rune / 6 tur; free-loop 6000'inin ÜSTÜNE ikinci kesim yok —
  zaten-kırpılmış dilimi devralır), bölüm VERİ'den SONRA ("cevabın
  dayanağı VERİ bloğudur" yönergesiyle), kırpma söylenir, sentetik
  HistoryTrimNote turu operatör konuşması gibi ASLA basılmaz;
  v0.9.478 bayt-kimlik pinleri korundu, 4/4 mutasyon ısırdı.
- ✅ v0.9.1232 — free-loop TR-native prompt + registry: systemChat
  Türkçe-native yeniden yazıldı, TUR TAVANI eki systemChatRoundCap
  olarak registry'de (SystemPromptChatRoundCap accessor'ı +
  base-verbatim pin testi); classDirective→classTurkishNative.
- ✅ v0.9.1233 — MCP get_exception_samples: GetExceptionGroupSamples
  yeniden kullanıldı (yeni SQL yok; REST eşi /samples viewer-açık),
  katalog 34 (beş pin + runbook), range_s dürüstçe FİLTRE (pencere
  grubun ömrü — küçük range ucuzlatmaz, yalan pencere yok), üç yol
  canlı smoke; lokal stack'lerin boş gelmesi VERİ (Go self-obs
  errorString), eşleme kaybı değil — ajan memory'ye yazdı.
- ✅ v0.9.1234 — yapısal tool hataları: mcp.ClassifyToolError
  {timeout|backend_unavailable|bad_args|not_found|internal, retryable,
  TR hint, 300-rune detay}; sınıflandırıcı SIFIR-bağımlılık
  internal/mcp'de (bağımlılık kapısı testli — mcptools mcp'yi import
  ediyor, tersi olmaz), üç merkezi çağrı noktası (chat döngüsü + MCP
  teli + guided kanıt çipi); 4 canlı hata-yolu smoke'u.
- ✅ v0.9.1235 — Caused-by kök neden pencereleme [5/S]: Frame.Segment
  ("Caused by:" sayacı; Suppressed bilinçli kapsam dışı), AppFrames
  en-derin-önce, prompt cümlesi (:408); tek-segment bayt-parite
  (legacyAppFrames diff testi), fingerprint izolasyonu grep-kanıtlı
  (chstore stackparse'ı hiç görmüyor). ÇIKARIM: make image
  VERSION=vX ile damga temiz — -dirty retag'i artık gereksiz.
- ✅ v0.9.1236 — depo çözüm kaçış kapısı: MatchRepoName üç basamak
  (exact→fold→loose ayraç-soyulmuş; basamak içi sıralı-ilk + Alts;
  ıskada Near önerileri), liste yalnız çözüm HATASINDA + 10dk cache +
  4MB/5000 tavan + 401/403'te asla; PickBranch case-insensitive
  (tam-yazım önce); pin fail-CLOSED (GetServiceMetadataStrict — ham
  CH hatası DSN taşıyabilir, Reason'a girmez); FE 📄 Kaynak satırında
  düzeltme izi. 7 mutasyon ısırdı; canlı TFS smoke'u YOK (lokalde
  sunucu yok — dürüstçe httptest kanıtı; prod'da ilk bakılacak:
  "depo adı sunucudan düzeltildi" satırı).
- ✅ v0.9.1237 — FetchCode döngü disiplini: codeWindowLimit=3 artık
  KESİLEN pencereyi sayar (aday tavanı 10, sabır tavanı 6 lookup),
  (file,line) dedup + aynı-dosya içerik yeniden kullanımı,
  codeFetchDeadline=25s (ingress 60s altında; caller-cancel ile
  AYRIŞIK — ajanın testi gerçek kusur yakaladı: tarayıcı kopuşu
  "DevOps yanıt vermedi" diye raporlanıyordu). 11/11 mutasyon kırmızı
  (ikisi test-seam eklenince ısırır oldu — 25s sabitine kapı
  dayanamazdı). Devir notu: code.reason yalnız sıfır-dosya dalında
  çiziliyordu → 1238'e.
- ✅ v0.9.1238 — includeCode tercihi localStorage'da (cm.ai.includeCode,
  varsayılan KAPALI — v0.9.831 kararı; lazy-seed, auto-run yarışı yok;
  codeCapable kapısı Spinner yalanını önler). Kısmi-sonuç satırı bayat
  önculdü (1236'da zaten gemide) — test kilidi kondu. 4/4 mutasyon.
- ✅ v0.9.1239 — pencere kalitesi paketi (4/4 canlıydı): centerToBudget
  frame satırını asla düşürmez (düşecekse pencere düşer + Reason);
  ">>> " işareti PromptBlock render'ında (Content bozulmaz, tek yazım
  const); "pencere 1/3 — kök neden segmenti" etiketi yalnız o segment
  gerçekten pencere verdiyse; log-stack tekrarları "(stack
  yukarıdakiyle aynı)" katlaması; fence uzantıya göre. Golden →
  contract assertion (216 üst-kesim artıefaktıydı); 7/7 + 1 mutasyon.
  Açık FE mini-dilim adayı: ">>> " oku operatör panelinde de görünsün.
- ✅ v0.9.1240 — katalog-pin proje türetimi: ProjectHint + parsePinnedRepo
  (repo | Project/repo | URL | scp biçimleri; muhafazakâr — koleksiyon
  proje sanılmaz), pinin projesi kazanır, projesiz pinde önek türetimi
  pinli yolda da çalışır, çıkmaz Reason üç kaynağı söyler; 1183'ün
  buggy-pinleyen testi korkusu korunarak düzeltildi; 7/7 mutasyon
  (mutasyon-derlenmeli kuralıyla). Kaçış kapısı pinli yolda zaten
  bağlıymış (kısmi bayat) — e2e testle kilitlendi.
- ✅ v0.9.1241 — kod-çekme sayaçları: /admin/stats "CoSRE kod bağlamı"
  bloğu (süreç-içi atomik; sınıf RETURN'da atanır — Reason'dan geri
  ayrıştırma yok; partial KAYIPTAN türer, nottan değil; FetchCode'a
  ulaşmayan çıkmazlar da oranda; "hiç denenmedi" ≠ "%0 isabet";
  entegrasyon-çıkmazları ⚠, veri-şekilli olanlar değil). AÇIK YARIM:
  ai_calls maskeli kopyasına [kod alınamadı: reason] işareti — satır
  "hiç istenmedi" ile "istendi-ıskaladı"yı ayıramıyor (XS aday).
- ✅ v0.9.1242 — Settings dry-run: POST /api/devops/resolve-dryrun,
  GERÇEK zincir (resolveChain/pickProject çıkarımı davranış-değişimsiz
  — mevcut testler değişmeden geçti = drift kanıtı); sayaç izolasyonu
  YAPISAL (dry-run FetchCode'a hiç girmez; pozitif-kontrollü test);
  bilinçli: ServicePicker değil düz Field (katalogda olmayan ad da
  test edilebilmeli); unconfigured'da bile dürüst adım çıktısı, 500
  değil (canlı smoke).
- ✅ v0.9.1243 — ai_calls [kod alınamadı: sınıf] işareti: Outcome
  FetchCode'un TEK defer'ından (sayaç ile satır ayrışamaz); kısmide
  "(kısmi: not)" eki; overflow-drop kendi diliyle; Halved() artık
  kaybını söylüyor; maskeleme değişmezi üç-vaka pinli.
- ✅ v0.9.1244 — takım/sahiplik araçları: list_teams +
  get_team_services (tek service_summary_5m okuması, hata-oranı
  sıralı, silent_services adlı; katalog 36). Kişisel kapsam BİLİNÇLİ
  dışarıda: cmk_ token rol taşır, kullanıcı değil — "benim takımım" o
  yolda tanımsız; guided'ın kimlikli yolu dokunulmadı. Guided okuması
  KOPYALANMADI, mcptools'a taşındı (drift imkânsız). Not: grouping
  testlerinin "tools/list sıralı gelir" gerekçesi bayat — registry-map
  sırası; kozmetik, önceden vardı.

## KAPANIŞ (2026-08-23) — hedef: "CoSRE daha iyi hale getir"

**20 sürüm: v0.9.1225 → v0.9.1244.** İki denetimin 58 doğrulanmış
bulgusunun dispozisyonu:

- **Chat/LLM/MCP (20 bulgu):** değer-4+ olanların TAMAMI gemide, tek
  istisna router-ıska tek-tur intent-seçimi [4/M] — küçük-model
  doktrininde davranış değişikliği, OPERATÖR KARARI (yukarıda).
  Değer-3 kuyruğu (5 kalem) adlı adına açık.
- **Kod-bağlamı (38 bulgu):** değer-4+ olanların TAMAMI gemide
  (mercek-tekrarları tekilleştirilmiş, aynı-gün-bayatlayanlar
  kanıtla kapatılmış); değer-2/3 kuyruğu (6 kalem) açık.
- Somut iyileşmeler: model turu −%44 · /logs sınıfı kanıt çipleri +
  "Üründe aç" köprüleri · guided çok-tur bellek · exception zinciri
  uçtan uca (grup→örnek→stack→trace) · kod pencereleri kök nedene
  iner + frame satırı garantili · depo çözümü yazım-dayanıklı + pin
  disiplini · isabet oranı /admin/stats'ta + ai_calls işaretli +
  Settings dry-run · yapısal tool hataları · katalog 33→36.
- Süreç notu: 1225-1228 ana oturumda elle; 1229-1244 SERİ shipper
  ajanlarıyla (her sürüm kendi kapıları + mutasyon doğrulaması;
  toplam ~2,9M ajan-token). Öznel "daha iyi" hükmü operatörde —
  elde-doğrulama en hızlı şurada: chat'te bir servis sayfasından
  "neden yavaş?" sor (bağlam + çipler + linkler), bir exception'da
  "Kodu da incele" (kök-neden penceresi + Reason), Settings'te
  dry-run, /admin/stats'ta CoSRE bloğu.

## Kapanış sonrası operatör istekleri

- ✅ v0.9.1246 — "Takımımın exceptionları → takım filtreli exceptions"
  (operatör isteği): /api/inbox?team= sunucuda 1244 seam'inden çözülür,
  semantik BİRLİK (owner ∪ SRE — canlı kanıt: sre-only takımda team=13
  satır, ownerTeam=0); guided my_exceptions + team_services cevapları
  /inbox?kind=exception&team=<kanonik-ad> köprüsü taşır (kimlik
  sunucuda, URL paylaşılabilir); SY/UG direktifi aynı sürümde (taban
  3→2, fold-eşleşme zaten vardı, çipler katalog yazımıyla); cache
  anahtarı NormTeamName'li. 14/14 mutasyon (2 gerçek kapsama deliği
  test kazandı). Dürüst boşluk: chat köprüsü canlıda doğrulanamadı —
  lokal guided hiç koşmuyor (copilot mock uçta; operatör gemma girince
  doğal doğrulanır).

## Bilinçli açık kalanlar (2026-08-22 gecesi)

- **Router-ıska tek-tur intent-seçimi [4/M]** — OPERATÖR KARARI İSTER.
  KARAR BRİFİNGİ (2026-08-23):
  * Bugün: guided router regex/token eşleşmesiyle rotalar; ıskalayan
    soru 5-turlu serbest tool döngüsüne düşer — küçük modelde en
    pahalı ve en kırılgan yol (tur başına katalog + geçmiş).
  * Öneri: ıskada ÖNCE tek, şemalı "intent seç" çağrısı (JSON enum:
    mevcut guided intent listesi + 'serbest'); model intent seçerse
    guided bundle'ı koşar (ucuz, deterministik), 'serbest' derse
    bugünkü döngü. Tur tavanı 5→(1+gerekirse döngü).
  * Risk: gemma4 şemalı enum seçiminde tutarsızsa yanlış-intent
    cevapları "emin ama alakasız" olur (döngü en azından veriye
    bakıyor). Bu yüzden ölçüm ŞART.
  * Ölçüm planı (yarım günün içinde): 30 gerçek ıska sorusu (ai_calls
    kayıtlarından, router-miss etiketli) → offline iki kol (mevcut
    döngü vs intent-seç) → isabet, tur sayısı, token, süre; eşik:
    intent-seç isabette ≥ döngü VE token ≤ %40'ı değilse GEMİYE
    ALINMAZ. Rollback: tek bayrak (settings blob), sessiz fallback
    yok — seçim yolu ayrı ai_calls surface etiketi taşır.
  * Onay verirsen: ölçüm koşusu + rapor ayrı, gemi kararı rapora.
- ~~Değer-3 kuyruğu (chat)~~ → GEMİDE: env handoff v0.9.1259 · konuşma
  deep-link v0.9.1258 (?chat=) · triage bağlam çipleri v0.9.1260 ·
  katı-JSON sıcaklık-0 v0.9.1261. FİNAL CEVAP STREAM'İ [3/S] BİLİNÇLİ
  ÇİZGİ-ALTI (2026-08-23 değerlendirmesi): yalnız tur-tavanı yolunda
  geçerli (nadir) ve doğru uygulama copilot çekirdeğinde kayıtsız-stream
  + kullanım-dönen yeni API ister (StreamText kendi ai_calls satırını
  basar, chat tur-toplamı TEK satır basar — çift kayıt/attribution
  kırılır; M'e büyür). Operatör isterse ayrı dilim. Değer-2/3 kuyruğu (kod-bağlamı): monorepo duvarları [3/M] ·
  refs sayfalama · api-version drift · singleflight · inner-class
  fallback · framework-only sebep ayrıştırması. Hiçbiri sessiz vaat
  değil; operatör seçerse dilimlenir.
- FE mini aday: ">>>" işaret oku operatör kod panelinde (1239 önerisi).
- NOT (bayat-kayıt): "1800-rune kırpık stack" + "FromLog fallback"
  v0.9.1225'te kapandı — ajan devir notları bunları açık sanabilir,
  yeniden kuyruğa almayın. Gerçek açıklar: katalog-pin proje türetimi
  [4/S] · sayaçlar [4/S] · dry-run [4/M].

SIRADA (chat/LLM/MCP ekseni — operatör önceliği):
1. v0.9.1226 [4/XS] Chat servis bağlamı: CopilotChat currentService
   memo'su yalnız /service|/pod okuyor; Traces/Endpoints/Deploys/
   Inbox/Metrics/Explore'daki ?service= körü. Rota-allowlist + memo
   genişletme, sıfır backend. (CopilotChat.tsx:115-121)
2. v0.9.1227 [5/S] MCP get_operation_health: operation_summary_5m'den
   endpoint-bazlı RED (service, range_s, sort=p99|rate|error_rate,
   limit≤50); guided_parity subset+snake_case doktrini; ÖNCE
   /mcp-tools skill yükle. (list_operations sayı taşımıyor)
3. v0.9.1228 [4/S] toolCallLink saf eşleyici (tool adı+doğrulanmış
   args → {label,href}; args tc.Input HAM — mapper kendi doğrular!)
   → step-result'a href (ToolEvidence "Üründe aç") + free-loop cevap
   linkleri (iki emit noktası, dedupLinksByHref, ≤4). Emsal:
   guidedAnswerLinks copilot_followup.go:227 + K4 ölü-param disiplini.
4. v0.9.1229 [4/S] Guided adım çipleri ölü etiket: 'i' id'siz +
   step-result'sız (copilot_guided.go:1196,1199,1220,1438). stepN
   sayacı + clipStepPreview ile çift emit; FE değişmez.
5. v0.9.1230 [4/S] Katalog maliyeti: ~40KB/tur tool kataloğu her
   turda gemma4'e gidiyor + tool sonuçlarına boyut tavanı yok.
   Kompakt TR açıklamalar / tur-içi yeniden bütçeleme.
6. v0.9.1231 [4/S] Guided narration'a konuşma geçmişi (baskın yol
   tek-tur-kör).
7. [4/XS] Free-loop sistem prompt'u TR-native + registry'ye (round-cap
   İngilizce ve kayıt dışı).
8. [4/S] MCP get_exception_samples (stack+trace pivot) · [4/S] yapısal
   tool hataları · [3/S] env handoff · [3/S] konuşma deep-link
   (?conversation=) · [3/S] yüzey-başına temperature (strict-JSON=0) ·
   [3/S] final cevap stream'i.

KOD-BAĞLAMI ekseni (sırada sonra, değer sırasıyla):
- [5/S] Caused-by kök neden pencereleme: ParseJava segment etiketi
  ("Caused by:" sayacı) + AppFrames en-derin-önce + prompt cümlesi
  (exception_context.go:367). SAF, tablo-testli.
- [4/S] Depo çözümü: _apis/git/repositories listesine karşı
  EqualFold + ayraç-soyulmuş eşleşme, ~10dk cache, kanonik adla
  retry, Reason'a not; branş seçimi de case-insensitive; CH pin-drop
  transient'te konvansiyona SESSİZ kaçma yok.
- [4/XS] codeFrameLimit aday değil PENCERE sayar (3 ıska = av biter,
  4. frame isabet edecekken) + aynı dosya:satır dedup.
- [4/XS] includeCode tercihi hatırlansın (localStorage) · [4/XS]
  FetchCode toplam süre tavanı · [4/S] pencere kalitesi paketi
  (bütçe kırpması frame satırının ÜSTÜNü kesmesin; frame satırı
  işaretli; ```java yerine uzantıya göre fence; prompt'ta 13× stack
  tekrarı dedup) · [4/XS] kısmi ıska Reason'da görünür.
- [4/M] Settings'te dry-run çözüm (LLM'siz test) · [4/S] kod-çekme
  sayaçları (self-observability).

Kibana artıkları (ayrı hedef, PARK): kırılım cluster/namespace ekseni,
pod context kapsamı, tek-doküman linki, imleç-ortası damgası + matris.
