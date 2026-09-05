package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/ai/assemble"
	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/mcp"
	"github.com/cilcenk/coremetry/internal/mcptools"
)

// chatMessageTexts — geçmiş turlarının METİN yarısı (rune bütçesi için).
//
// assemble paketi transport tipini BİLMİYOR ve bilmemeli (saf bütçe
// hesabı, şekil-bağımsız); dönüşüm burada, tipin evinde yaşıyor.
func chatMessageTexts(msgs []copilot.ChatMessage) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Text
	}
	return out
}

// In-app AI chatbot (v0.6.53). An agentic loop that lets the
// operator ask free-form questions ("why is payment-service slow?",
// "errors in the last hour") and answers them grounded in their own
// telemetry. The LLM's function-calling backend is the SAME tool set
// the MCP server exposes (mcptools.ToolList — TEK kayıt defteri;
// güncel sayı ve katalog orada yaşar, burada sayı tutmuyoruz: iki kez
// bayatladı v0.9.1141'e gelene dek) — so the chat can read live data
// without any new query plumbing. ToolList is the single source; this
// comment stopped counting so it can't drift again.
//
// Transport: POST with the full conversation, response streamed as
// SSE. v1 is STEP-streaming (operator decision 2026-05-28): we emit
// a `step` event per tool call so the operator sees "⚙ list_services"
// progress, then an `answer` event with the final prose. v0.8.404
// adds token streaming to the GUIDED path (copilot_guided.go emits
// `delta` events from StreamText, with a transparent buffered
// fallback); this free tool loop stays buffered — tool-call streaming
// is a different beast (see internal/copilot/stream.go header).
//
// Conversation is EPHEMERAL — the frontend holds history in
// component state and sends it whole each turn; BU handler hiçbir şey
// kalıcılaştırmaz — konuşma kalıcılığı v0.9.1139'dan beri AYRI uçta
// (ai_conversations.go, istemci-güdümlü upsert). Auth: any
// authenticated user — tool'lar read-only + MinRole süzgeçli
// (v0.9.1136), so a viewer chatting is safe.

const (
	chatMaxToolRounds = 5  // guardrail: cap the agentic loop so a model can't fan tool calls forever
	chatMaxMessages   = 40 // cap conversation length fed back to the LLM (token budget)
)

type chatRequest struct {
	Messages []copilot.ChatMessage `json:"messages"`
	// Context (v0.9.164) — frontend'in bulunduğu sayfadan geçirdiği ipucu
	// (context-awareness): mesaj bir servis ADI taşımıyorsa guided router bu
	// servisi varsayılan alır ("neden yavaş?" checkout sayfasında → checkout).
	// Şeffaf: chat banner'ı "checkout servisindesin" der.
	Context struct {
		Service string `json:"service,omitempty"`
		// Operation (v0.9.184) — the ?op= the operator is viewing on a
		// service page; lets a bare "bu operasyonun durumu" scope RED to
		// that span name (guided router's operation fallback).
		Operation string `json:"operation,omitempty"`
		// Explain (v0.9.479) — AI çekmecesindeki sohbetin bağlam devri:
		// operatörün AZ ÖNCE OKUDUĞU açıklamanın metni (+ kanıt id'leri).
		// Boşken bu dosyadaki her yol bayt-bayt eski davranıştadır; dolu
		// olduğunda guided/explain-grounded ayrımını copilot_drawer.go
		// yönetir (kök neden + tasarım orada yazılı).
		Explain string `json:"explain,omitempty"`
		// Subject (v0.9.482) — çekmecenin öznesi, frontend'in `?ai=` kodeği
		// biçiminde ("trace:<id>", "span:<trace>:<span>", "exception:<fp>").
		// Sunucu bundan İLGİLİ EXPLAIN'İN KANIT PAKETİNİ yeniden kurar
		// (copilot_drawer.go): açıklamanın metni takip sorularına yetmiyordu
		// — "logda ne yazıyor" kör cevaplanıyordu (operatör raporu).
		// Boşken v0.9.479 davranışı bayt-bayt korunur.
		Subject string `json:"subject,omitempty"`
		// RangeS (v0.9.529) — operatörün EKRANDAKİ zaman aralığı,
		// saniye. Soru AÇIK bir pencere taşımıyorsa guided router sabit
		// 30dk yerine bunu kullanır: 6 saatlik pencereye bakarken "hata
		// oranı ne" diye soran operatör, baktığından BAŞKA bir pencerenin
		// cevabını alıyordu ve fark görünmüyordu. Açık pencere taşıyan
		// soru bunu EZER. 0/absent = eski istemci, davranış değişmez.
		RangeS int64 `json:"rangeS,omitempty"`
		// ToMs (v0.10.33) — pencerenin BİTİŞ anı, yalnız operatör MUTLAK
		// bir aralık seçtiğinde (custom/zoom) gönderiliyor. Göreli
		// aralıkta boş kalır ve sunucu şimdiye çapalar; sabitlemek uzun
		// bir soruşturmada cevabı DONDURURDU.
		ToMs int64 `json:"toMs,omitempty"`
		// Env (v0.9.1259) — Topbar'daki global env seçimi. Soru AÇIK bir
		// env adı taşımıyorsa guided router bunu varsayılan alır: ekran
		// uat gösterirken cevabın sessizce başka env'i kapsaması (CoSRE
		// denetim bulgusu) biter. Açık env sorusu her zaman ezer.
		Env string `json:"env,omitempty"`
		// v0.10.183 — istek başına model profili (çoklu model dilim C); bilinmeyen
		// kimlik sessizce varsayılana düşer (copilot.WithProfile aynı kuralı uygular).
		Profile string `json:"profile,omitempty"`
		// Trace (v0.9.537) — operatörün EKRANDA baktığı trace'in ID'si
		// (/trace?id=). "bu trace neden yavaş" gibi ID'siz sorular
		// bununla çözülür; mesajda açık 32-hex varsa o kazanır.
		Trace string `json:"trace,omitempty"`
		// TzOffsetMin (v0.10.437, D6) — tarayıcının UTC'den dakika ofseti
		// (doğu pozitif; İstanbul +180). Mutlak tarih/saat soruları
		// ("08/08/2026 04-08 arası") operatörün yerel saatinde yorumlanır;
		// yoksa UTC (eski istemci).
		TzOffsetMin int `json:"tzOffsetMin,omitempty"`
		// Tz (v0.10.445) — tarayıcının IANA saat dilimi adı ("Europe/Istanbul").
		// Sabit ofset DST'yi bilmez: kışın sorulan yaz tarihi bir saat kayıyordu;
		// ad varsa time.LoadLocation (tzdata gömülü), yoksa ofset.
		Tz string `json:"tz,omitempty"`
	} `json:"context,omitempty"`
}

// copilotChat is the SSE chat endpoint. Runs the agentic loop and
// streams progress + the final answer. One ai_calls row is written
// per exchange (summing per-round token usage) via RecordUsage.
func (s *Server) copilotChat(w http.ResponseWriter, r *http.Request) {
	chatT0 := time.Now() // v0.10.397 — sohbet süresi ai_calls'a
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		http.Error(w, `{"error":"messages required"}`, http.StatusBadRequest)
		return
	}
	// v0.9.1187 (AI Faz 4.5, K3) — geçmiş bütçesi SAYIDAN RUNE'A geçti.
	//
	// Eskiden yalnız "son 40 tur" vardı ve 40 sayısı BOYUT hakkında hiçbir
	// şey söylemiyor: operatörün yapıştırdığı bir log yığını ya da modelin
	// uzun cevapları, 40 turu rahatça bir 2B modelin bağlamının üstüne
	// çıkarıyordu. Belirtisi de sinsi olurdu — kırılma değil, taze kanıtın
	// eski konuşma tarafından bağlamdan atılması.
	//
	// Sayı tavanı KALDI (kısa turlarda bile 40 tur küçük modelde odak
	// kaybettirir); rune bütçesi onun üstüne bindi. Karar saf ve
	// deterministik: assemble.ClampHistory.
	keep, trimmed := assemble.ClampHistory(
		assemble.RuneLens(chatMessageTexts(req.Messages)),
		chatMaxMessages, assemble.HistoryMaxRunes)
	req.Messages = req.Messages[len(req.Messages)-keep:]
	// Kırpma modele SÖYLENİR. Sessiz kırpma, modelin olmayan bir konuşmayı
	// hatırlıyormuş gibi davranmasına yol açar ("az önce dediğin gibi…") ve
	// operatör bunu uydurma sanır — kırpmanın kendisinden pahalı bir hata.
	if note := assemble.TrimNoteIfNeeded(trimmed); note != "" {
		req.Messages = append([]copilot.ChatMessage{{Role: "user", Text: note}}, req.Messages...)
	}

	// SSE plumbing — same header set + flusher assert the sse.Broker
	// handler uses.
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	// v0.10.27 — YAZIM KİLİDİ. Heartbeat goroutine'i aynı ResponseWriter'a
	// yazıyor ve http.ResponseWriter eşzamanlı yazıma GÜVENLİ DEĞİL;
	// paylaşılmayan bir kilit yarışı engellemez, bozuk çerçeve üretir.
	var wmu sync.Mutex
	emit := func(event string, payload any) {
		b, _ := json.Marshal(payload)
		wmu.Lock()
		defer wmu.Unlock()
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		flusher.Flush()
	}
	// v0.9.1229 — ⚙ çip kimliği TEK sayaçtan (chat_step_ids.go). Bir
	// istekte birden çok yol adım yayınlayabiliyor (guided bağlam çipini
	// basıp rotayı devredebilir, sonra çekmece ya da serbest döngü
	// çalışır); ayrı sayaçlar aynı `i`yi iki kez üretir ve frontend
	// kanıtı `i` ile eşlediği için YANLIŞ çipe yapıştırırdı.
	emit = withStepIDs(emit)

	// v0.10.27 — HEARTBEAT. Serbest döngü buffered; ilk LLM çağrısı
	// bitene kadar (180s'e kadar) tek bayt gitmeyebiliyor ve sessiz bir
	// bağlantı proxy arkasında koparıldığında operatör hiçbir hata
	// görmüyordu — balon "yazıyor…"da asılı, `done` hiç gelmiyor.
	// Stop() SENKRON: handler döndükten sonra yazılan bir ping,
	// ResponseWriter'ı ömrünün dışında kullanmak olurdu.
	hb := startSSEHeartbeat(&wmu, w, flusher, sseHeartbeatEvery)
	defer hb.Stop()

	// Attribution: tag ctx so RecordUsage attributes the exchange to
	// the "chat" surface on the /ai page.
	//
	// exchangeID (v0.8.399, AI audit feedback slice) — one crypto/rand
	// hex id per exchange. Emitted to the UI in the answer event so the
	// thumbs up/down can POST it to /api/ai/feedback, and threaded via
	// CallMeta into the ai_calls row (exchange_id) so the verdict joins
	// back to the exact call it rates. Provider-agnostic plumbing —
	// works identically for anthropic / openai-compat / github, and for
	// both the guided path and the free tool loop.
	c := auth.FromContext(r.Context())
	uid, email := "", ""
	if c != nil {
		uid, email = c.UserID, c.Email
	}
	exchangeID := newRandID(16)
	// v0.10.24 — UÇTAN UCA TAVAN. Hiçbir katmanda yoktu: handler yalnız
	// tool başına 20s taşıyordu, http.Server'da tavan yok (SSE için
	// DOĞRU — sunucu geneli WriteTimeout her akışı keserdi), istemci ham
	// fetch. Gerçekten sınırsız olan eksen tur başına tool çağrısı
	// SAYISIydı; tool bağlamı bu ctx'ten türediği için tek deadline
	// hepsini kapatıyor (chat_deadline.go).
	exchangeMax := chatExchangeTimeout(s.copilot.ClientTimeout())
	dctx, cancelExchange := context.WithTimeout(r.Context(), exchangeMax)
	defer cancelExchange()
	ctx := copilot.WithMeta(dctx, copilot.CallMeta{
		Surface: "chat", UserID: uid, UserEmail: email, ExchangeID: exchangeID,
	})
	// v0.10.425 (CoSRE denetimi O2) — alışverişin kök span'ı (chat_span.go):
	// kademeler, turlar, araçlar ve CH sorguları bunun altında iç içe geçer.
	// defer: dört erken-dönüş kademesi var, aksi hâlde span sızar.
	ctx, cspan := s.beginChatSpan(ctx, exchangeID)
	defer cspan.end()
	if p := strings.TrimSpace(req.Context.Profile); p != "" {
		// v0.10.183 — seçilen profil bütün alışverişe (guided/intent/loop) uygulanır;
		// yüzey eşlemesini ezer (operatörün açık seçimi). Bilinmeyen id → varsayılan.
		ctx = copilot.WithProfile(ctx, p)
	}
	// v0.9.528 Faz 2 — model kiminle konuştuğunu bilsin: ad (hitap için)
	// ve rol (viewer'a yapamayacağı eylemi ÖNERMEMESİ için). Ad çözümü
	// /api/auth/me'nin 30s cache'ini kullanır, yani sohbet başına yeni
	// bir FINAL satır okuması EKLEMEZ. Çözülemezse ön-söz boş kalır ve
	// iki prompt da bayt-bayt eskisi olur.
	addressee := s.chatAddressee(r.Context(), c)
	ctx = ctxWithAddressee(ctx, addressee)

	// v0.8.397 (AI audit A3) — guided mode first, for EVERY provider:
	// a deterministic intent router recognises the highest-value
	// question shapes, the server prefetches the data, and the model
	// makes exactly ONE tool-less narration call (copilot_guided.go).
	// Deterministic beats tool-roulette on these shapes even for
	// frontier models; the 2B-class primary target (qwen3.5-2b) can't
	// drive the 5-round × 11-schema loop reliably at all. No match →
	// the free tool loop below runs UNCHANGED.
	// v0.10.33 — ZAMAN ÇIPASI, kademelerden ÖNCE hesaplanıyor: guided de
	// serbest döngü de aynı pencereyi görmeli, yoksa aynı soru hangi
	// kademeye düştüğüne göre farklı bir zaman diliminden cevaplanır.
	anchorTo, anchored := chatAnchorTime(req.Context.ToMs, time.Now())
	if anchored {
		// Çıpa sessizce uygulanmamalı: operatör cevabın GEÇMİŞ bir
		// pencereden geldiğini görebilmeli.
		emit("step", map[string]string{
			"label": "pencere: " + anchorTo.UTC().Format("2006-01-02 15:04 UTC") + "'de bitiyor",
		})
	}

	if handled, gok := s.copilotChatGuided(ctx, emit, req.Messages, req.Context.Service, req.Context.Operation, req.Context.Explain, req.Context.RangeS, req.Context.Trace, req.Context.Env, anchorTo, req.Context.TzOffsetMin, req.Context.Tz); handled {
		cspan.tier("guided", gok)
		emit("done", map[string]bool{"ok": gok})
		return
	}

	// v0.9.479 — AI çekmecesi sohbeti: ekrandaki açıklama bağlam olarak
	// geldiyse (context.explain) ve guided somut bir özneye oturmadıysa,
	// cevabı tek narration çağrısıyla O AÇIKLAMAYA dayandır. Sıra
	// bilinçli: guided (canlı telemetri, somut özne) > çekmece bağlamı >
	// dokümanlar > serbest döngü. Çekmece sohbeti özne-kapsamlıdır;
	// filo/doküman soruları global CoSRE penceresinde kalır.
	// (ai_calls satırını guided'da olduğu gibi tek narration çağrısı
	// KENDİSİ yazar — burada ikinci bir RecordUsage yok.)
	// v0.9.482 — özne (context.subject) doluysa aynı yol ilgili explain'in
	// HAM KANITINI da yeniden kurup anlatıma katar; kanıt çekilemezse
	// v0.9.479'un metin-tabanlı anlatımı aynen sürer (soft-fail).
	if handled, dok := s.copilotChatDrawer(ctx, emit, req.Messages, req.Context.Explain, req.Context.Subject, req.Context.Service); handled {
		cspan.tier("drawer", dok)
		emit("done", map[string]bool{"ok": dok})
		return
	}

	// v0.8.438 — doküman RAG yolu: guided telemetri router'ı
	// eşleşmediyse ve soru yüklü dokümanlara yeterince benziyorsa
	// (skor tabanı) tek narration çağrısıyla kaynak atıflı cevap.
	// Sıra bilinçli: telemetri şekilleri > dokümanlar > serbest döngü.
	if handled, rok := s.ragChatAnswer(ctx, emit, req.Messages, req.Context.Service); handled {
		cspan.tier("rag", rok)
		emit("done", map[string]bool{"ok": rok})
		return
	}

	// v0.10.172 — kademe 3.5: LLM niyet sınıflandırıcısı (copilot_intent.go).
	// Deterministik router (kademe 1) ve RAG eşleşmeyen serbest soruyu
	// küçük model tek katı-JSON çağrısıyla kılavuz niyetine eşler; eşlerse
	// cevap AYNI prefetch→anlatım yolundan (runGuidedRoute). Eşlemezse
	// ayara göre öneri çipleri (on_no_loop, prod varsayılanı) ya da aşağıdaki
	// serbest döngü (on). Sınıflandırıcı hatası sessizce düşer — döngü sürer.
	if handled, iok := s.copilotChatIntent(ctx, emit, req.Messages, req.Context.Service, req.Context.Operation, req.Context.Explain, req.Context.RangeS, req.Context.Env, anchorTo); handled {
		cspan.tier("intent", iok)
		emit("done", map[string]bool{"ok": iok})
		return
	}

	// Build the tool set once (closures over the live store + logs)
	// and the LLM-facing specs from the same list.
	//
	// v0.9.1136 (AI Faz 3.1) — rol filtresi: MinRole'ü çağıranın
	// rolünü AŞAN tool ne LİSTELENİR ne de ÇAĞRILABİLİR. Gizlemek
	// reddetmekten iyidir (reddedilen tool bir tur + bağlam harcar),
	// ama yalnız gizlemek yetmez: serbest döngü ismi byName'den
	// çözüyor, yani filtre İKİ map'e de uygulanır — model adı
	// tahmin etse bile "unknown tool" alır.
	role := ""
	if c != nil {
		role = c.Role
	}
	tools := toolsForRole(mcptools.ToolList(s.mcpDeps()), role)
	byName := make(map[string]func(context.Context, json.RawMessage) (any, error), len(tools))
	specs := make([]copilot.ToolSpec, 0, len(tools))
	// v0.9.1230 (AI perf) — katalog DİYETİ: spec'e t.Description değil
	// t.ChatDescription() girer.
	//
	// Ölçüm: 33 tool = 24.268 B İngilizce açıklama + 17.672 B şema =
	// 41.940 B, ve bu katalog her ChatWithTools turunda (aşağıdaki
	// döngü, chatMaxToolRounds=5) YENİDEN gönderiliyordu. Yerel gemma4
	// için bu, copilot_guided.go başlığında adı konmuş başarısızlık
	// modunun ("schema soup") doğrudan kaynağı. ChatDescription() aynı
	// KAYIT DEFTERİNDEN türeyen kompakt Türkçe görünümü verir; MCP
	// tools/list dış istemciye tam İngilizce sözleşmeyi servis etmeye
	// devam eder (mcptools/short_desc_test.go iki yönü de pinler).
	//
	// Şemalar bilinçli olarak DOKUNULMADAN kalıyor: arg adları ve
	// varsayılan/tavan şerhleri doğru ÇAĞRININ koşulu — orada kazanılan
	// bayt, yanlış argümanla harcanan bir tura değmez.
	for _, t := range tools {
		byName[t.Name] = t.Handler
		specs = append(specs, copilot.ToolSpec{
			Name: t.Name, Description: t.ChatDescription(), InputSchema: t.InputSchema,
		})
	}
	// v0.10.88 (dilim ③) — DIŞ MCP tool'ları yerli kataloğun yanına.
	// Aynı toolsForRole süzgeci, aynı spec şekli; döngü dış/yerli
	// ayrımını görmez — bütçe (clampToolResultForModel) ve hata
	// (ToolErrorJSON) yolları değişmeden işler. Katalog Registry'nin
	// 5 dk TTL'li önbelleğinden gelir; sunucu erişilemezse katalog boş
	// düşer ve sohbet YERLİ tool'larla aynen sürer (soft-fail).
	extNames := map[string]bool{} // v0.10.425 — ai.tool köken etiketi (native | external)
	if s.mcpClient != nil && s.mcpClient.Configured() {
		ext := externalChatTools(
			s.mcpClient.Registry().Tools(ctx),
			s.mcpClient.ToolRules,
			s.mcpClient.Registry().Call,
			func(server, tool string, args json.RawMessage) {
				// Her dış çağrı iz bırakır: modelin konuştuğu dış uç,
				// operatörün "bunu kim/ne çağırdı" sorusunun konusu.
				s.audit(r, "mcp.call", "mcp_server", server, mcpCallAuditDetails(tool, args))
			})
		for _, t := range toolsForRole(ext, role) {
			extNames[t.Name] = true
			byName[t.Name] = t.Handler
			specs = append(specs, copilot.ToolSpec{
				Name: t.Name, Description: t.ChatDescription(), InputSchema: t.InputSchema,
			})
		}
	}

	// CoSRE Faz-2 — render_chart server-side emission: the model PICKS
	// the chart by calling the render_chart tool; the SERVER builds the
	// deterministic ```chart``` fence from the handler's VALIDATED spec
	// (chartFence, copilot_guided.go) and appends it to the final
	// answer. A gemma4-class small model never formats chart JSON, and
	// a hallucinated service/metric never reaches the UI (the handler
	// returns ok:false for those). Blocks accumulate across rounds,
	// deduped by service+operation+agg.
	var chartBlocks []string
	chartSeen := map[string]bool{}
	appendCharts := func(text string) string {
		// v0.10.47 — MODELİN KENDİ ÇİTİ ÖNCE SÖKÜLÜR.
		//
		// Sunucu çitleri metnin SONUNA ekleniyor; modelin metnine hiç çit
		// koymuyoruz. Yani `text` içinde bulunan her ```chart``` çiti tanım
		// gereği model-yazımıdır ve kapsamı DOĞRULANMAMIŞTIR — arayüz ikisini
		// ayırt edemiyor (chatMarkdown.ts yalnız lang'a bakıyor) ve uydurma
		// bir kapsamla CANLI grafik çiziyordu.
		//
		// Sökme, erken dönüşün ÜSTÜNDE: hiç sunucu grafiği olmayan bir turda
		// da model çit yazabilir — asıl tehlikeli hâl tam olarak odur.
		text, _ = stripModelChartFences(text)
		if len(chartBlocks) == 0 {
			return text
		}
		return strings.TrimRight(text, "\n") + "\n" + strings.Join(chartBlocks, "")
	}

	conv := req.Messages
	// Taşma yeniden denemesi TEK sefer: ikinci bir deneme aynı duvara
	// bir çağrı daha yakar (kod-explain'in `hb == block` kontrolüyle
	// aynı gerekçe).
	overflowRetried := false
	// v0.10.29 — döngü boyunca çağrılan araçlar; cevabın altındaki
	// deterministik "Kaynak:" künyesini besliyor (chat_source_note.go).
	var calledTools []string
	var totalIn, totalOut uint32
	var lastErr error
	var finalText string
	// v0.9.1181 (Faz 4.3) — ⚙ çipi ile onun "veriyi göster" bloğunu eşleyen
	// sayaç. İstek boyunca tekil olmalı: çipler tur döngüsü boyunca birikiyor,
	// tur-içi indeks ikinci turda çakışırdı. v0.9.1229 — sayının kendisi
	// artık emit sarmalayıcısında; burada yalnız SON çipin kimliği tutulur
	// (eşli step-result için).
	stepN := 0
	// v0.9.1228 — döngü boyunca biriken ürün köprüleri (toolCallLink):
	// cevap çipi olarak yayınlanır; request-ID linkleriyle birleşir.
	var loopLinks []guidedAnswerLink
	// v0.9.528 Faz 2 — serbest döngünün sistem prompt'u da kiminle
	// konuşulduğunu taşır. Ön-söz boşsa sabitin aynısı.
	// v0.10.32 — EKRAN BAĞLAMI. İlk üç kademe req.Context.* alıyordu,
	// serbest döngü HİÇBİRİNİ almıyordu; üstelik prompt "aksini
	// söylemedikçe 1800 (30 dk) kullan" diyor. Ekranda checkout-service
	// açık ve 6 saatlik pencere seçiliyken sorulan soru filo geneline ve
	// 30 dakikaya gidiyordu (chat_screen_context.go).
	screenCtx := ChatScreenContext{
		Service:   req.Context.Service,
		Operation: req.Context.Operation,
		Env:       req.Context.Env,
		RangeS:    req.Context.RangeS,
		AnchorTo:  anchorTo,
		Anchored:  anchored,
	}
	// v0.10.50 — ÇIPA ARAÇLARA DA GEÇER.
	//
	// v0.10.33 çıpayı burada operatöre (çip) ve modele (önsöz) İLAN
	// ediyordu, ama araç katmanına hiç ulaşmıyordu: mcptools.rangeWindow
	// koşulsuz `time.Now()` kuruyordu ve hiçbir tool mutlak pencere
	// argümanı almıyor (ev kuralı — küçük modele epoch hesaplatma yok).
	// Yani model BUGÜNÜN sayısını okuyup önsöze uyarak DÜNÜN penceresi
	// diye yazıyordu; çip de o yanlışı TEYİT ediyordu.
	//
	// Etiketli yanlış, etiketsiz yanlıştan tehlikeli: sorgulanmıyor.
	if anchored {
		ctx = mcptools.WithAnchor(ctx, anchorTo)
	}
	// Bağlam SESSİZCE uygulanmamalı: operatör cevabın neden o kapsamda
	// olduğunu görebilmeli (v0.9.1259 env şeffaflığının kalan yarısı).
	if chip := screenContextChipTR(screenCtx); chip != "" {
		emit("step", map[string]string{"label": chip})
	}
	loopPrompt := screenContextPreambleTR(screenCtx) +
		withAddressee(addressee, copilot.SystemPromptChat())

	// v0.10.88 — TEKRAR MUHAFIZI (exchange kapsamı). v0.10.84 prompt'u
	// "aynı tool'u aynı argümanlarla iki kez çağırma" diyor; bu harita
	// yasağı ZORLAMAYA çevirir (devops huntWindows `tried` deseni).
	// Dış tool'da bedel ağ + audit satırı, yerlide CH sorgusu — ikisi de
	// aynı cevabı ikinci kez satın almaya değmez.
	seenToolCalls := map[string]bool{}

	for round := 0; round < chatMaxToolRounds; round++ {
		tctx, endTurn := cspan.turn(ctx, round, overflowRetried) // v0.10.425 — ai.chat.turn
		turn, err := s.copilot.ChatWithTools(tctx, loopPrompt, conv, specs)
		endTurn(turn.InputTokens, turn.OutputTokens, err)
		totalIn += turn.InputTokens
		totalOut += turn.OutputTokens
		// v0.10.26 — BAĞLAM TAŞMASI. isContextOverflowErr yazılı ve
		// tablo-testliydi ama tek çağrı yeri copilot_code.go'ydu; sohbet
		// ham İngilizce sağlayıcı gövdesini ekrana basıyordu. Emsalin
		// aynısı: küçült + BİR KEZ yeniden dene (chat_overflow.go).
		if err != nil && isContextOverflowErr(err) && !overflowRetried {
			if shrunk, ok := shrinkConvForRetry(conv); ok {
				overflowRetried = true
				conv = shrunk
				emit("step", map[string]string{"label": "bağlam taştı — geçmiş küçültülüp yeniden deneniyor"})
				round-- // bu tur sayılmasın: yeniden deneme, ilerleme değil
				continue
			}
		}
		if err != nil {
			lastErr = err
			// Tavan dolduysa ham `context deadline exceeded` metni yanıltır:
			// operatör bunu "model zaman aşımına uğradı" diye okur ve modeli
			// suçlar, oysa olan şey ALIŞVERİŞİN tavana dayanmasıdır — farklı
			// bir eylem gerektiriyor (soruyu daralt).
			msg := err.Error()
			switch {
			case dctx.Err() != nil:
				msg = chatDeadlineMessageTR(exchangeMax)
			case isContextOverflowErr(err):
				msg = chatOverflowMessageTR(overflowRetried)
			}
			emit("error", map[string]string{"error": msg})
			break
		}
		// No tool calls → this turn's text is the final answer (plus any
		// chart blocks accumulated from earlier render_chart rounds).
		if len(turn.ToolCalls) == 0 {
			// v0.10.29 — KAYNAK KÜNYESİ. Diğer üç kademede vardı, modelin
			// EN SERBEST olduğu bu yolda yoktu. En değerli hâli araç
			// listelemek değil: hiç araç çağrılmadıysa cevabın canlı
			// veriye DAYANMADIĞINI söylemek.
			finalText = appendCharts(turn.Text) + chatSourceNoteTR(calledTools)
			// v0.9.709 (operatör-bildirimi) — cevaptaki request_id'ler log
			// köprüsü çipi olur; altyapı (links + ChatBubble çipleri)
			// v0.9.419'dan beri hazırdı, yalnız guided yayınlıyordu.
			// v0.9.1228 — tool köprüleri önce (yapısal, döngüden), sonra
			// metin-madeni request-ID linkleri; href'e göre tekil.
			links := loopLinks
			for _, l := range s.answerRequestIDLinks(ctx, finalText, req.Context.Service) {
				links = mergeToolLinks(links, l)
			}
			emit("answer", map[string]any{"text": finalText, "exchangeId": exchangeID,
				"links": links})
			break
		}
		// Record the assistant's tool-call turn, then execute each
		// call and feed results back as a user turn.
		conv = append(conv, copilot.ChatMessage{
			Role: "assistant", Text: turn.Text, ToolCalls: turn.ToolCalls,
		})
		results := make([]copilot.ToolResult, 0, len(turn.ToolCalls))
		for _, tc := range turn.ToolCalls {
			// v0.9.1181 (Faz 4.3) — çipin kimliği. `step` tool ÇALIŞMADAN
			// önce çıkar (ilerleme geri bildirimi), sonuç ise çalıştıktan
			// sonra ayrı bir olayla; ikisini bu sayı eşler. Tur içindeki
			// indeks YETMEZ, çünkü çipler turlar boyunca birikiyor —
			// sayaç istek boyunca tekil.
			// v0.9.1229 — kimliği sarmalayıcı veriyor (emitStepChip);
			// döngünün kendi sayacı guided'ın çipleriyle çakışabiliyordu.
			// v0.10.53 — künye DENENEN aracı değil, VERİ DÖNDÜREN aracı sayar.
			//
			// v0.10.29'da bu satır tam burada duruyordu, yani `byName`
			// kontrolünden ÖNCE: modelin UYDURDUĞU, var olmayan bir araç adı
			// künyeye "Kaynak:" diye giriyordu. Hata dönen araç da öyle.
			//
			// İkisi de künyenin iddiasını çürütüyor: künye cevabın CANLI
			// VERİYE dayandığını söylüyor. Çalışmamış bir araç veri
			// döndürmez; hata metni telemetri değildir. Uydurma bir adı
			// kaynak diye göstermek, uydurmayı KANITLA süslemek olurdu —
			// künyenin var olma sebebinin tam tersi.
			//
			// Kayıt artık başarılı çalıştırmadan SONRA (aşağıda). Çip yine
			// burada çıkıyor: operatör modelin NE DENEDİĞİNİ görmeli,
			// başarısız denemeler dâhil — o ayrı bir soru.
			stepN = emitStepChip(emit, tc.Name, string(tc.Input))
			h, found := byName[tc.Name]
			if !found {
				msg := fmt.Sprintf("unknown tool %q", tc.Name)
				emit("step-result", map[string]any{
					"i": stepN, "tool": tc.Name, "ok": false,
					"preview": msg, "truncated": false, "bytes": len(msg),
					"durationMs": 0, // v0.10.161 — yürütülmedi; Σ süre hesaplanabilir kalsın
				})
				results = append(results, copilot.ToolResult{
					CallID: tc.ID, Name: tc.Name, IsError: true,
					Content: msg,
				})
				continue
			}
			// v0.10.88 — aynı çağrının ikinci kopyası YÜRÜTÜLMEZ; model
			// ToolErrorJSON sözleşmesindeki alanlarla (error/retryable/
			// hint) yönlendirilir. Anahtar kanonik: JSON anahtar sırası
			// değişse de aynı çağrıdır (chat_mcp_bridge.go).
			if markRepeatedCall(seenToolCalls, tc.Name, tc.Input) {
				preview, _ := clipStepPreview(repeatedCallJSON)
				emit("step-result", map[string]any{
					"i": stepN, "tool": tc.Name, "ok": false,
					"preview": preview, "truncated": false, "bytes": len(repeatedCallJSON),
					"durationMs": 0, // v0.10.161 — tekrar koruması, yürütülmedi
				})
				results = append(results, copilot.ToolResult{
					CallID: tc.ID, Name: tc.Name, IsError: true,
					Content: repeatedCallJSON,
				})
				continue
			}
			// v0.10.161 — araç çağrısı SÜRESİ tele biner (step-result.durationMs):
			// şeffaflık paneli «N araç · M hata · Σ s» ve satır başına çubuk
			// bundan; alan yoksa frontend «—» çizer (guided ön-yüklemeleri
			// ölçülmez). Ölçüm yalnız gerçekten çalışan araç için — bilinmeyen
			// araç / tekrar koruması yürütülmez, süre yazılmaz.
			tctx, endTool := cspan.tool(ctx, tc.Name, extNames[tc.Name]) // v0.10.425 — ai.tool
			toolT0 := time.Now()
			out, herr := runChatTool(tctx, h, tc.Input)
			toolDur := time.Since(toolT0)
			// CoSRE Faz-2 — intercept render_chart: parse the handler's
			// validated output (never tc.Input — the model's raw args may
			// name a service that doesn't exist) into a ```chart``` fence.
			if tc.Name == "render_chart" && herr == nil {
				if block, key := chatChartBlock(out); block != "" && !chartSeen[key] {
					chartSeen[key] = true
					chartBlocks = append(chartBlocks, block)
				}
			}
			tr := copilot.ToolResult{CallID: tc.ID, Name: tc.Name}
			if herr != nil {
				tr.IsError = true
				// v0.9.1234 — MCP telinin gördüğü sözleşmenin AYNISI
				// (mcp.ToolErrorJSON): sınıf + tekrar denenebilirlik +
				// Türkçe "şimdi ne yap" ipucu + kırpılmış ham metin.
				// Öncesinde ham sürücü dökümü doğrudan gemma4'e ve
				// oradan ⚙ çipine gidiyordu. Çipin kendisi aşağıda bu
				// metni okuyor, yani operatör modelin GÖRDÜĞÜNÜ görür.
				tr.Content = mcp.ToolErrorJSON(herr)
			} else {
				tr.Content = out
				// v0.10.53 — künyeye YALNIZ buradan giriliyor: araç vardı,
				// çalıştı ve veri döndürdü. Çip zaten tc.Name basıyor; künye
				// AYNI adı kullanıyor ki cevabın altındaki atıf ile üstündeki
				// çipler ayrışmasın.
				calledTools = append(calledTools, tc.Name)
			}
			// Kanıt tele biner: modelin GÖRDÜĞÜ metnin ta kendisi, kırpılmışsa
			// kırpıldığı SÖYLENEREK. Özet göndermek daha ucuz olurdu ama
			// özetlenmiş kanıt kanıt değildir — operatörün sınayacağı şey
			// modelin okuduğu şey olmalı. `bytes` kırpılmamış gerçek boy,
			// yani "ne kadarını görmüyorum" cevaplanabilir.
			endTool(len(tr.Content), tr.IsError) // v0.10.425 — bayt + ok; gövde yok
			preview, truncated := clipStepPreview(tr.Content)
			stepEv := map[string]any{
				"i": stepN, "tool": tc.Name, "ok": !tr.IsError,
				"preview": preview, "truncated": truncated, "bytes": len(tr.Content),
				"durationMs": toolDur.Milliseconds(),
			}
			// v0.9.1228 — çipe ürün köprüsü: başarılı çağrının hedef
			// görünümü (K4-denetimli harita, chat_tool_links.go). Eski FE
			// alanı yok sayar (links/suggestions ileri-uyum sınıfı).
			if !tr.IsError {
				// v0.9.1321 (§3.1 K6) — köprü çipi tool'un GERÇEKTEN
				// sorguladığı pencereyi taşır ([now-range_s, now]);
				// arg'da range_s yoksa pencere yazılmaz.
				if l, ok := toolCallLink(tc.Name, tc.Input, time.Now()); ok {
					stepEv["href"] = l.Href
					loopLinks = mergeToolLinks(loopLinks, l)
				}
			}
			emit("step-result", stepEv)
			// v0.9.1230 — MODEL tarafındaki bütçe, çipten SONRA uygulanır:
			// önizleme ve `bytes` kırpılmamış GERÇEK boyu göstermeye devam
			// etsin (operatörün "ne kadarını görmüyorum" sorusu orada
			// cevaplanıyor), modele giden metin ise tavana insin — kırpma
			// içeriğin içinde SÖYLENEREK (chat_tool_budget.go).
			//
			// Konuşmanın tur başında yeniden bütçelenmesi (ClampHistory'nin
			// döngü içinde tekrarı) BİLEREK yapılmıyor: asistanın tool-call
			// turu ile onun ToolResults turu ikizdir, birini düşürmek
			// sağlayıcıda sahipsiz tool sonucu bırakır. Kaynağı burada
			// kesmek doğru yer.
			tr.Content, _ = clampToolResultForModel(tr.Content)
			results = append(results, tr)
		}
		conv = append(conv, copilot.ChatMessage{Role: "user", ToolResults: results})

		// Hit the round cap with tool calls still pending → ask the
		// model for a best-effort answer with what it has, no more
		// tools, so the operator isn't left hanging.
		//
		// v0.9.1232 — tavan yönergesi burada satır-içi İngilizce bir
		// literaldi; prompt METNİ olduğu için internal/copilot/prompts.go'ya
		// taşındı (SystemPromptChatRoundCap = taban + ek). Sicil
		// accessor'lardan türediğinden bu ek artık dil kapısının kapsamında.
		if round == chatMaxToolRounds-1 {
			capPrompt := withAddressee(addressee, copilot.SystemPromptChatRoundCap())
			tctx2, endTurn2 := cspan.turn(ctx, round, false) // v0.10.425 — tur tavanı da bir tur
			turn2, err2 := s.copilot.ChatWithTools(tctx2, capPrompt, conv, nil)
			endTurn2(turn2.InputTokens, turn2.OutputTokens, err2)
			totalIn += turn2.InputTokens
			totalOut += turn2.OutputTokens
			if err2 != nil {
				lastErr = err2
				// v0.10.61 — HATA SINIFLANDIRMASI BURADA DA.
				//
				// Bu dal ham `err2.Error()` basıyordu, yani v0.10.24 ve
				// v0.10.26'nın düzelttiği iki kusur tur-tavanı yolunda AYNEN
				// duruyordu: operatör ham İngilizce sağlayıcı gövdesini
				// görüyor ve `context deadline exceeded` metnini "model zaman
				// aşımına uğradı" diye okuyup MODELİ suçluyordu — oysa olan
				// şey alışverişin tavana dayanması ve farklı bir eylem
				// gerektiriyor.
				//
				// Sınıflandırma normal yolun AYNISI; ikisinin ayrışmaması
				// için kalıp birebir kopyalanmadı, aynı yardımcılar çağrıldı.
				msg := err2.Error()
				switch {
				case dctx.Err() != nil:
					msg = chatDeadlineMessageTR(exchangeMax)
				case isContextOverflowErr(err2):
					msg = chatOverflowMessageTR(overflowRetried)
				}
				emit("error", map[string]string{"error": msg})
			} else {
				// v0.10.61 — KAYNAK KÜNYESİ BURADA DA.
				//
				// Künye (v0.10.29) normal yolda vardı, tur-tavanı yolunda
				// YOKTU. Oysa bu yol tanımı gereği EN ÇOK araç çağıran yol:
				// tavana ancak tur tur araç çağırarak varılıyor. Yani atıfın
				// en anlamlı olduğu cevap, tek atıfsız cevaptı.
				finalText = appendCharts(turn2.Text) + chatSourceNoteTR(calledTools)
				// v0.9.709 (operatör-bildirimi) — cevaptaki request_id'ler log
				// köprüsü çipi olur. v0.9.1228 — tool köprüleri de burada:
				// tur-tavanı cevabı da döngünün kanıt linklerini taşır.
				links := loopLinks
				for _, l := range s.answerRequestIDLinks(ctx, finalText, req.Context.Service) {
					links = mergeToolLinks(links, l)
				}
				emit("answer", map[string]any{"text": finalText, "exchangeId": exchangeID,
					"links": links})
			}
		}
	}

	// One ai_calls row per exchange. Prompt sample = the operator's
	// last user message; response sample = the final answer.
	status, errMsg := "ok", ""
	if lastErr != nil {
		status, errMsg = "error", lastErr.Error()
	}
	cspan.finish(totalIn, totalOut, lastErr) // v0.10.425 — span ve satır aynı toplamlar
	s.copilot.RecordUsage(ctx, chatT0, totalIn, totalOut, status, errMsg, lastUserText(req.Messages), finalText)

	emit("done", map[string]bool{"ok": lastErr == nil})
}

// runChatTool invokes a tool handler with a bounded timeout and
// JSON-stringifies the result for feeding back to the LLM. The
// per-tool clampLimit caps (in mcptools) already bound result size;
// the timeout guards a slow CH query from stalling the whole chat.
func runChatTool(ctx context.Context, h func(context.Context, json.RawMessage) (any, error), args json.RawMessage) (string, error) {
	tctx, cancel := context.WithTimeout(ctx, mcp.ToolCallBudget) // v0.10.401 — telle aynı bütçe
	defer cancel()
	out, err := h(tctx, args)
	if err != nil {
		return "", err
	}
	b, merr := json.Marshal(out)
	if merr != nil {
		return "", merr
	}
	return string(b), nil
}

// chatChartBlock (CoSRE Faz-2) parses the render_chart handler's output
// — the server-VALIDATED spec, not the model's raw tool input — and
// returns the deterministic ```chart``` fence (chartFence,
// copilot_guided.go) plus a service+operation+agg dedup key. Empty
// block = not renderable (ok:false, malformed, or incomplete spec).
// Pure — table-tested in copilot_chat_test.go.
func chatChartBlock(out string) (block, key string) {
	var rc struct {
		OK   bool `json:"ok"`
		Spec struct {
			Service   string `json:"service"`
			Operation string `json:"operation"`
			Agg       string `json:"agg"`
			RangeS    int64  `json:"rangeS"`
			GroupBy   string `json:"groupBy"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(out), &rc); err != nil || !rc.OK || rc.Spec.Service == "" || rc.Spec.Agg == "" {
		return "", ""
	}
	titleBase := rc.Spec.Service
	if rc.Spec.Operation != "" {
		titleBase = rc.Spec.Operation
	}
	title := titleBase + " · " + rc.Spec.Agg
	if rc.Spec.GroupBy != "" {
		title += " · " + rc.Spec.GroupBy
	}
	fence := chartFence(guidedChartSpec{
		Title:     title,
		Service:   rc.Spec.Service,
		Operation: rc.Spec.Operation,
		Agg:       rc.Spec.Agg,
		RangeS:    rc.Spec.RangeS,
		GroupBy:   rc.Spec.GroupBy,
	})
	// v0.9.1186 — kırılım DEDUP anahtarına girdi. Girmeseydi aynı servis+
	// agg'ın kırılımlı ve kırılımsız hâli "aynı kart" sayılır, ikincisi
	// sessizce düşerdi — oysa model ikisini bilerek isteyebilir ("toplamı
	// göster, sonra endpoint kırılımını").
	return fence, rc.Spec.Service + "\x00" + rc.Spec.Operation + "\x00" + rc.Spec.Agg + "\x00" + rc.Spec.GroupBy
}

// lastUserText pulls the most recent user-typed message for the
// ai_calls prompt sample (skips tool-result turns).
func lastUserText(msgs []copilot.ChatMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && strings.TrimSpace(msgs[i].Text) != "" {
			return msgs[i].Text
		}
	}
	return ""
}
