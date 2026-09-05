package api

// copilot_intent.go — v0.10.172 (operatör: "serbest sorular da sorulursa
// işe yarar mı?" → spec onayı; "RAG'a gitmesine gerek yok").
//
// Kademe 3.5: deterministik router (copilot_guided.go, kademe 1), çekmece
// bağlamı ve RAG eşleşmeyen serbest soruyu küçük model TEK katı-JSON
// çağrısıyla mevcut kılavuz niyetlerinden birine + slotlara (servis/env/
// pencere/kimlik) eşler; eşlerse cevap AYNI prefetch→anlatım yolundan
// (runGuidedRoute) gelir. Yerel 2B-sınıfı modelin iyi yaptığı tek şey
// (kısa yapılandırılmış çıktı) kullanılır, kötü yaptığı (çok turlu araç
// seçimi — "schema soup", copilot_guided.go başlığı) devreden çıkar.
//
// Güvenlik sınırları — model slot UYDURAMAZ:
//   - niyet beyaz listesi (intentAllowed): router'ın çözüm gerektiren
//     şekilleri (family/team/request_id) dışarıda,
//   - servis/env adı CANLI listeye doğrulanır (örneklenmiş alt-küme değil,
//     guidedServiceNames = ListServiceNames 2000); adlandırılmış ama
//     eşleşmeyen servis → none (yanlış kapsamda cevap vermektense sus),
//   - trace/span kimliği hex kontrolü, pencere snapRangeS basamağına,
//   - sınıflandırıcı hatası sessizce düşer (sonraki basamak), asla cevabı
//     kesmez.
// none → ayara göre (copilot.IntentClassifyMode): on_no_loop = tool'suz
// TEK anlatım çağrısıyla genel bilgi cevabı, başında "telemetriyle
// eşleşmedi" notu, altında öneri çipleri (v0.10.194; prod yerel model
// varsayılanı), on = serbest tool döngüsü (frontier). Saf çekirdekler
// parseIntentJSON + intentGeneralAnswer, copilot_intent_test.go'da
// tablo-testli; basamak sırası kaynak kapısıyla.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
)

// intentAllowed — sınıflandırıcının üretebileceği niyetler (prompts.go
// systemIntentClassify listesiyle birebir).
var intentAllowed = map[string]guidedIntent{
	"problems": guidedProblems, "service_health": guidedServiceHealth, "root_cause": guidedRootCause,
	"slow_traces": guidedSlowTraces, "deploy_impact": guidedDeployImpact, "log_errors": guidedLogErrors,
	"pod_health": guidedPodHealth, "db_health": guidedDBHealth, "messaging_health": guidedMessagingHealth,
	"shift_summary": guidedShiftSummary, "my_services": guidedMyServices, "my_problems": guidedMyProblems,
	"my_exceptions": guidedMyExceptions, "self_meta": guidedSelfMeta, "trace_by_id": guidedTraceByID,
	"span_by_id": guidedSpanByID,
	// v0.10.429 (D1) — adı geçen takımın servisleri; `team` slotu canlı
	// takım kataloğuyla doğrulanır (uydurma takım → none).
	"team_services": guidedTeamServices,
	// v0.10.433 (D5) — alan süzgeçli log araması; logField/logValue slotları.
	"log_field": guidedLogField,
	// v0.10.436 (D2b) — içinde parça geçen trace'ler; searchText slotu.
	"trace_search": guidedTraceSearch,
}

// intentNeedsService — servissiz anlamsız şekiller: model servis vermediyse
// ve ekranda bağlam servisi yoksa none. pod_health DIŞARIDA: servissiz hâli
// filo-geneli sıralama (copilot_guided.go, bilinçli).
var intentNeedsService = map[guidedIntent]bool{guidedServiceHealth: true, guidedRootCause: true}

func intentClassifySchema() map[string]any {
	names := make([]string, 0, len(intentAllowed)+1)
	for k := range intentAllowed {
		names = append(names, k)
	}
	sort.Strings(names)
	names = append(names, "none")
	return objSchema(map[string]any{
		"intent":  map[string]any{"type": "string", "enum": names},
		"service": strProp(), "env": strProp(), "rangeS": numProp(),
		"traceId": strProp(), "spanId": strProp(),
		"team":     strProp(),                        // v0.10.429 (D1)
		"logField": strProp(), "logValue": strProp(), // v0.10.433 (D5)
		"searchText": strProp(), // v0.10.436 (D2b)
	})
}

type intentJSON struct {
	Intent  string  `json:"intent"`
	Service string  `json:"service"`
	Env     string  `json:"env"`
	RangeS  float64 `json:"rangeS"`
	TraceID string  `json:"traceId"`
	SpanID  string  `json:"spanId"`
	Team    string  `json:"team"` // v0.10.429 (D1)
	// v0.10.433 (D5)
	LogField string `json:"logField"`
	LogValue string `json:"logValue"`
	// v0.10.436 (D2b)
	SearchText string `json:"searchText"`
}

var (
	intentHex32 = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	intentHex16 = regexp.MustCompile(`^[0-9a-fA-F]{16}$`)
)

// stripJSONFence — ```json … ``` çitini ve önsöz/sonsözü soyar: ilk '{'…son '}'.
func stripJSONFence(raw string) string {
	i, j := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if i < 0 || j <= i {
		return ""
	}
	return raw[i : j+1]
}

// matchLiveName — tam eş > harfe duyarsız eş > TEK benzersiz ÖNEK eşi
// (extractServiceEntity ile aynı kural: ≥3 karakter, sınır '-', '_', '.' —
// "check" "checkout-service"yi alamaz, "payment" → "payment-service" yalnız
// tek aday varsa). Yoksa "". Alt-dize eşi YOK (inceleme #1: "e" bir servisi
// yakalıyordu; env'de "p" → prod yanlış kapsam).
func matchLiveName(v string, live []string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	for _, n := range live {
		if n == v {
			return n
		}
	}
	lv := strings.ToLower(v)
	for _, n := range live {
		if strings.ToLower(n) == lv {
			return n
		}
	}
	if len(lv) < 3 {
		return ""
	}
	found := ""
	for _, n := range live {
		ls := strings.ToLower(n)
		if !strings.HasPrefix(ls, lv) {
			continue
		}
		if len(ls) > len(lv) && ls[len(lv)] != '-' && ls[len(lv)] != '_' && ls[len(lv)] != '.' {
			continue
		}
		if found != "" {
			return "" // belirsiz
		}
		found = n
	}
	return found
}

// parseIntentJSON — SAF: model çıktısı → rota + pencere (0 = belirtilmedi).
// ok=false ⇒ none. Eşleşmeyen env → none. v0.10.429 (D1): eşleşmeyen /
// belirsiz servis adı artık none DEĞİL — canlı katalogdan yakın adlar
// (nearNames) bulunursa guidedAskService rotası ("hangisini kastettin?");
// hiç yakın ad yoksa none (uydurma ad). Servis gerektiren niyet servissiz
// ve bağlamsızsa da sorar (adayları bundle doldurur).
func parseIntentJSON(raw string, services, envs, teams []string, ctxService string) (guidedRoute, int64, bool) {
	var in intentJSON
	if err := json.Unmarshal([]byte(stripJSONFence(raw)), &in); err != nil {
		return guidedRoute{}, 0, false
	}
	intent, ok := intentAllowed[strings.ToLower(strings.TrimSpace(in.Intent))]
	if !ok {
		return guidedRoute{}, 0, false
	}
	route := guidedRoute{Intent: intent}
	if strings.TrimSpace(in.Service) != "" {
		if route.Service = matchLiveName(in.Service, services); route.Service == "" {
			if opts := nearNames(in.Service, services, guidedServiceAskMax); len(opts) > 0 {
				return guidedRoute{Intent: guidedAskService, AskIntent: intent, ServiceOptions: opts}, 0, true
			}
			return guidedRoute{}, 0, false // katalogda yakın ad bile yok: uydurulmuş
		}
	}
	if route.Service == "" && intentNeedsService[intent] {
		if ctxService == "" {
			return guidedRoute{Intent: guidedAskService, AskIntent: intent}, 0, true
		}
		route.Service = ctxService
	}
	if intent == guidedTeamServices {
		team := matchLiveTeam(in.Team, teams)
		if team == "" {
			return guidedRoute{}, 0, false // takım kataloğunda yok: uydurma
		}
		route.Team = team
	}
	if strings.TrimSpace(in.Env) != "" {
		if route.Env = matchLiveName(in.Env, envs); route.Env == "" {
			return guidedRoute{}, 0, false
		}
	}
	if intent == guidedTraceSearch { // v0.10.436 (D2b) — parça boş olamaz
		st := strings.TrimSpace(in.SearchText)
		if st == "" || len(st) > 256 {
			return guidedRoute{}, 0, false
		}
		route.SearchText, route.SearchSQL = st, sqlLikeRe.MatchString(st)
	}
	if intent == guidedLogField { // v0.10.433 (D5) — alan adı şekil kapısı, değer boş olamaz
		f, v := strings.TrimSpace(in.LogField), strings.TrimSpace(in.LogValue)
		if !logFieldNameOK(f) || v == "" || len(v) > 256 {
			return guidedRoute{}, 0, false
		}
		route.LogField, route.LogValue, route.LogContains = f, v, true
	}
	switch intent {
	case guidedTraceByID:
		id := strings.TrimSpace(in.TraceID)
		if !intentHex32.MatchString(id) {
			return guidedRoute{}, 0, false
		}
		route.TraceID = strings.ToLower(id)
	case guidedSpanByID:
		id := strings.TrimSpace(in.SpanID)
		if !intentHex16.MatchString(id) {
			return guidedRoute{}, 0, false
		}
		route.SpanID = strings.ToLower(id)
	}
	var rangeS int64
	if in.RangeS > 0 && in.RangeS < 1e9 {
		rangeS = snapRangeS(int64(in.RangeS))
	}
	return route, rangeS, true
}

func intentSummary(route guidedRoute, rangeS int64, matched bool) string {
	if !matched {
		return "niyet: none — kılavuz şekline oturmadı"
	}
	parts := []string{"niyet: " + string(route.Intent)}
	if route.Service != "" {
		parts = append(parts, "servis: "+route.Service)
	}
	if route.Env != "" {
		parts = append(parts, "env: "+route.Env)
	}
	if rangeS > 0 {
		parts = append(parts, fmt.Sprintf("pencere: %ds", rangeS))
	}
	if route.TraceID != "" {
		parts = append(parts, "trace: "+route.TraceID)
	}
	if route.SpanID != "" {
		parts = append(parts, "span: "+route.SpanID)
	}
	if route.LogField != "" {
		parts = append(parts, fmt.Sprintf("alan: %s=%q", route.LogField, route.LogValue))
	}
	return strings.Join(parts, " · ")
}

// intentNoneSuggestions — "şöyle sorabilirsin" çipleri: ekranda servis varsa
// servis kapsamlı, yoksa küresel (guidedSuggestions'ın mevcut listeleri).
func intentNoneSuggestions(ctxService string) []string {
	if ctxService != "" {
		return guidedSuggestions(guidedRoute{Intent: guidedServiceHealth, Service: ctxService})
	}
	return guidedSuggestions(guidedRoute{Intent: guidedProblems})
}

// intentClassifyTimeout — istemci zaman aşımının çeyreği, [5 s, 25 s]:
// soğuk yerel model (AI sekmesi kopyası: "büyük modeli soğuk yükleyen yerel
// LLM") 25 s'yi aşabilir; aşınca basamak düşer ve on_no_loop'ta kaçınmak
// istediği serbest döngü koşar — operatör süreyi büyüttüyse burası da büyür (#14).
func intentClassifyTimeout(client time.Duration) time.Duration {
	t := client / 4
	if t > 25*time.Second {
		t = 25 * time.Second
	}
	if t < 5*time.Second {
		t = 5 * time.Second
	}
	return t
}

// intentStepArgs — adım çipinin args'ı küçük JSON (öteki çiplerle aynı biçim;
// 1500 karakterlik düz soru `intent_classify(…)` olarak basılıyordu, #11).
func intentStepArgs(q string) string {
	r := []rune(q)
	if len(r) > 120 {
		q = string(r[:120]) + "…"
	}
	b, _ := json.Marshal(map[string]string{"q": q})
	return string(b)
}

// systemPromptIntentText — test köprüsü (prompt metni copilot paketinde).
func systemPromptIntentText() string { return copilot.SystemPromptIntentClassify() }

const intentNoneAnswerTR = "Bu soruyu telemetriyle eşleyemedim — kılavuz soru şekillerinden hiçbirine oturmadı. Şu biçimlerde sorabilirsin:"

// intentGeneralNoteTR — v0.10.194: genel cevabın ÜSTÜNE sunucunun koyduğu not.
// Operatör direktifi "ama söylesin": cevabın telemetriden gelmediği modele
// bırakılmaz, sunucu yazar. Kapanışta "şöyle de sorabilirsin" — çipler altında.
const intentGeneralNoteTR = "_Bu soru telemetriyle eşleşmedi — genel bilgiyle cevaplıyorum; aşağıdakiler senin sisteminin verisine dayanmaz._"

// intentGeneralAnswer — SAF: model çıktısı → ekrana giden metin. Boş çıktı
// (model sustu) eski deterministik reddi döndürür ki operatör hiç değilse
// çipleri görsün; general=false ⇒ exchangeId yazılmaz (oylanacak model
// metni yok, #3 ile aynı gerekçe).
func intentGeneralAnswer(raw string) (text string, general bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return intentNoneAnswerTR, false
	}
	return intentGeneralNoteTR + "\n\n" + raw, true
}

// copilotChatIntent — kademe 3.5 (bkz. dosya başlığı). handled=false ⇒
// sonraki basamak (serbest döngü) sürer.
func (s *Server) copilotChatIntent(ctx context.Context, emit func(string, any), msgs []copilot.ChatMessage, ctxService, ctxOperation, explain string, ctxRangeS int64, ctxEnv string, anchorTo time.Time) (handled, ok bool) {
	mode := s.copilot.IntentClassifyMode()
	if mode == copilot.IntentOff || !s.copilot.Active() {
		return false, false
	}
	question := strings.TrimSpace(lastUserText(msgs))
	if question == "" {
		return false, false
	}
	// Katalog okunamadıysa (CH anlık hatası) basamak ATLANIR: parseIntentJSON
	// adlandırılmış her servisi reddeder ve on_no_loop'ta operatör bir CH
	// hıçkırığı yüzünden "eşleyemedim" alırdı (inceleme #4); deterministik
	// router gibi zarifçe bir sonraki basamağa düşer.
	svcNames := s.guidedServiceNames(ctx)
	if len(svcNames) == 0 {
		return false, false
	}
	// Sınıflandırıcıya kırpık KOPYA; anlatıma (runGuidedRoute) tam metin (#13).
	q := question
	if r := []rune(q); len(r) > 1500 {
		q = string(r[:1500])
	}
	const tool = "intent_classify"
	i := emitStepChipOrigin(emit, tool, intentStepArgs(q), "intent")
	t0 := time.Now()
	// Sınıflandırma çağrısı EXCHANGE'SİZ: aynı exchange_id altında ikinci
	// ai_calls satırı geri bildirim/KB adayı JOIN'lerini ikiye katlıyordu
	// (inceleme #2); satır yine yazılır (surface chat-intent), oylanamaz.
	m := copilot.MetaFromContext(ctx)
	m.ExchangeID = ""
	cctx, cancel := context.WithTimeout(copilot.WithMeta(ctx, m), intentClassifyTimeout(s.copilot.ClientTimeout()))
	raw, err := s.copilotExplainJSONSurface(cctx, "chat-intent", copilot.SystemPromptIntentClassify(), q, intentClassifySchema())
	cancel()
	if err != nil {
		emitStepEvidence(emit, i, tool, "", err) // panelde görünür; cevap kesilmez
		return false, false
	}
	route, rangeS, matched := parseIntentJSON(raw, svcNames, s.guidedEnvNames(ctx), s.guidedTeamNames(ctx), ctxService)
	if i > 0 {
		// Özet + modelin HAM JSON'u: yanlış-ama-geçerli niyette operatör
		// modelin ne dediğini görür (#10); süre 161 panel sözleşmesi.
		text := intentSummary(route, rangeS, matched) + "\n" + strings.TrimSpace(raw)
		preview, trunc := clipStepPreview(text)
		emit("step-result", map[string]any{
			"i": i, "tool": tool, "ok": true, "preview": preview, "truncated": trunc,
			"bytes": len(text), "durationMs": time.Since(t0).Milliseconds(),
		})
	}
	if !matched {
		// Router boşluk raporu (chstore.RouterGaps) serbest döngüye düşen
		// soruları surface='chat' ile sayar; on_no_loop'ta o satır hiç
		// yazılmaz — none sonucu ayrı yüzeyle kaydedilir ki rapor kör
		// kalmasın (#6). Exchange'siz: KB adayı JOIN'ine girmez.
		s.copilot.RecordUsage(copilot.WithMeta(ctx, copilot.CallMeta{Surface: "chat-intent-none", UserID: m.UserID, UserEmail: m.UserEmail}), t0,
			0, 0, "ok", "", question, strings.TrimSpace(raw))
		if mode != copilot.IntentOnNoLoop {
			return false, false
		}
		// v0.10.194 — Operator-reported ("türkiyenin başkenti neresi" → çipli
		// red): "eşleştiremese de cevap versin, LLM'e sorup ama söylesin".
		// none artık son söz değil: tool'suz TEK anlatım çağrısı (chat-general
		// yüzeyi, varsayılan profil) genel bilgiyle cevaplar; not sunucudan,
		// çipler yine altında. Model hata verirse eski deterministik red gider —
		// cevap hiçbir zaman kesilmez. Akış (delta) AÇIK: RAG'ın "önce reddi gör,
		// sonra gerçek cevap" çift-cevap riski burada yok, altında basamak yok.
		// Şeffaflık paneli iki LLM çağrısını da görür (inceleme #5); soru
		// sınıflandırıcıyla aynı 1500 rune'luk kırpık kopya (#7).
		const gtool = "general_answer"
		gi := emitStepChipOrigin(emit, gtool, intentStepArgs(q), "intent")
		gt0 := time.Now()
		emit("delta", map[string]string{"text": intentGeneralNoteTR + "\n\n"})
		graw, gerr := s.copilotStreamSurface(ctx, "chat-general", copilot.SystemPromptGeneralChat(), q,
			func(d string) { emit("delta", map[string]string{"text": d}) })
		text, general := intentGeneralAnswer(graw)
		// Akış ORTASINDA kopan çağrı (yerel model, kesik stream) metni TAŞIR
		// (stream.go: "mid-stream hatası token sayılarını taşır"); operatörün
		// ekranda gördüğü kısmi cevap redde çevrilmez — yalnız hiç metin
		// yoksa eski red (inceleme #1).
		if gerr != nil && strings.TrimSpace(graw) == "" {
			text, general = intentNoneAnswerTR, false
		}
		if gerr != nil {
			emitStepEvidence(emit, gi, gtool, "", gerr)
		} else if gi > 0 {
			preview, trunc := clipStepPreview(strings.TrimSpace(graw))
			emit("step-result", map[string]any{
				"i": gi, "tool": gtool, "ok": true, "preview": preview, "truncated": trunc,
				"bytes": len(graw), "durationMs": time.Since(gt0).Milliseconds(),
			})
		}
		ans := map[string]any{"text": text, "suggestions": intentNoneSuggestions(ctxService)}
		if general {
			// Model metni oylanabilir (exchangeId = sohbetin kendi alışverişi;
			// sınıflandırma çağrısı exchange'siz kalmaya devam eder, #2).
			ans["exchangeId"] = copilot.MetaFromContext(ctx).ExchangeID
			ans["links"] = s.answerRequestIDLinks(ctx, graw, ctxService)
		} else {
			// exchangeId YOK: deterministik sunucu metni oylanamaz (#3).
		}
		emit("answer", ans)
		return true, true
	}
	if route.Env == "" && ctxEnv != "" {
		route.Env = ctxEnv
	}
	if rangeS == 0 {
		switch {
		case route.Intent == guidedShiftSummary:
			rangeS = 12 * 3600 // guided ile aynı varsayılan (vardiya)
		case ctxRangeS > 0:
			rangeS = snapRangeS(ctxRangeS)
		default:
			rangeS = 3600
		}
	}
	return s.runGuidedRoute(ctx, emit, route, rangeS, question, msgs, explain, ctxService, ctxOperation, "", anchorTo)
}

// matchLiveTeam — v0.10.429 (D1): takım adı/kodu canlı katalogla; katlanmış
// tam eş (NormTeamName — 2 harfli kodlar geçerli) ya da jeton eşi
// (extractTeamEntity ile aynı tolerans). matchLiveName'in ≥3 önek tabanı
// burada YOK: "SY" gerçek bir koddur.
func matchLiveTeam(v string, teams []string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	fv := chstore.NormTeamName(v)
	for _, t := range teams {
		if chstore.NormTeamName(t) == fv {
			return t
		}
	}
	return extractTeamEntity(v, teams)
}
