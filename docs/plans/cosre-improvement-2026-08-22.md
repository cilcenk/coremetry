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
