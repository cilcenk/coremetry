package api

// copilot_intent_test.go — v0.10.172 sözleşmesi (copilot_intent.go başlığı):
// model slot UYDURAMAZ; none → false; basamak sırası kaynakta pinli.

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseIntentJSON(t *testing.T) {
	services := []string{"payment-service", "api-gateway", "ledger-service", "ledger-writer", "checkout-service"}
	envs := []string{"prod", "uat"}
	cases := []struct {
		name    string
		raw     string
		ctxSvc  string
		wantOK  bool
		intent  guidedIntent
		service string
		env     string
		rangeS  int64
		trace   string
	}{
		{name: "düz JSON, tam servis", raw: `{"intent":"log_errors","service":"payment-service","env":"prod","rangeS":3600}`, wantOK: true, intent: guidedLogErrors, service: "payment-service", env: "prod", rangeS: 3600},
		{name: "çitli + önsöz", raw: "Tabii:\n```json\n{\"intent\":\"slow_traces\",\"service\":\"\",\"env\":\"\",\"rangeS\":0}\n```", wantOK: true, intent: guidedSlowTraces},
		{name: "harfe duyarsız + benzersiz alt-dize", raw: `{"intent":"problems","service":"Payment"}`, wantOK: true, intent: guidedProblems, service: "payment-service"},
		{name: "belirsiz önek (ledger ×2) → none", raw: `{"intent":"problems","service":"ledger"}`, wantOK: false},
		{name: "önek sınırı: 'check' checkout-service'i alamaz → none", raw: `{"intent":"problems","service":"check"}`, wantOK: false},
		{name: "2 karakter önek → none", raw: `{"intent":"problems","service":"ap"}`, wantOK: false},
		{name: "'api' → api-gateway (sınır '-')", raw: `{"intent":"problems","service":"api"}`, wantOK: true, intent: guidedProblems, service: "api-gateway"},
		{name: "env tek harf → none (prod'a düşmez)", raw: `{"intent":"problems","env":"p"}`, wantOK: false},
		{name: "pod_health servissiz = filo geneli (izinli)", raw: `{"intent":"pod_health"}`, wantOK: true, intent: guidedPodHealth},
		{name: "uydurulmuş servis → none (yanlış kapsam yerine sus)", raw: `{"intent":"log_errors","service":"checkout-svc"}`, wantOK: false},
		{name: "servis gerektiren şekil, servissiz, bağlamsız → none", raw: `{"intent":"service_health"}`, wantOK: false},
		{name: "servis gerektiren şekil, bağlam servisi devralır", raw: `{"intent":"root_cause"}`, ctxSvc: "api-gateway", wantOK: true, intent: guidedRootCause, service: "api-gateway"},
		{name: "bilinmeyen niyet → none", raw: `{"intent":"family_health","service":"api-gateway"}`, wantOK: false},
		{name: "none → none", raw: `{"intent":"none"}`, wantOK: false},
		{name: "bozuk JSON → none", raw: `intent: problems`, wantOK: false},
		{name: "uydurulmuş env → none", raw: `{"intent":"problems","env":"staging"}`, wantOK: false},
		{name: "trace_by_id geçersiz kimlik → none", raw: `{"intent":"trace_by_id","traceId":"abc"}`, wantOK: false},
		{name: "trace_by_id 32-hex, büyük harf küçültülür", raw: `{"intent":"trace_by_id","traceId":"ABCDEF0123456789ABCDEF0123456789"}`, wantOK: true, intent: guidedTraceByID, trace: "abcdef0123456789abcdef0123456789"},
		{name: "pencere basamağa oturur, 0 = belirtilmedi", raw: `{"intent":"problems","rangeS":0}`, wantOK: true, intent: guidedProblems, rangeS: 0},
		{name: "saçma pencere (1e12) yok sayılır", raw: `{"intent":"problems","rangeS":1000000000000}`, wantOK: true, intent: guidedProblems, rangeS: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			route, rangeS, ok := parseIntentJSON(tc.raw, services, envs, tc.ctxSvc)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v, want %v (route=%+v)", ok, tc.wantOK, route)
			}
			if !ok {
				return
			}
			if route.Intent != tc.intent || route.Service != tc.service || route.Env != tc.env || route.TraceID != tc.trace {
				t.Fatalf("route=%+v, want intent=%s service=%q env=%q trace=%q", route, tc.intent, tc.service, tc.env, tc.trace)
			}
			if rangeS != tc.rangeS {
				t.Fatalf("rangeS=%d, want %d", rangeS, tc.rangeS)
			}
		})
	}
	// 3600 basamağa AYNEN oturur (snapRangeS)
	if _, r, _ := parseIntentJSON(`{"intent":"problems","rangeS":3600}`, services, envs, ""); r != 3600 {
		t.Fatalf("rangeS 3600 → %d", r)
	}
}

func TestIntentClassifySchemaEnum(t *testing.T) {
	sch := intentClassifySchema()
	props := sch["properties"].(map[string]any)
	enum := props["intent"].(map[string]any)["enum"].([]string)
	if enum[len(enum)-1] != "none" {
		t.Fatalf("enum'un sonu 'none' olmalı: %v", enum)
	}
	if len(enum) != len(intentAllowed)+1 {
		t.Fatalf("enum %d, beyaz liste %d + none", len(enum), len(intentAllowed))
	}
	for name := range intentAllowed {
		if !strings.Contains(systemPromptIntentText(), "- "+name) && !strings.Contains(systemPromptIntentText(), "/ "+name) {
			t.Errorf("beyaz listedeki %q sistem talimatında anlatılmamış", name)
		}
	}
}

// TestIntentStageOrder — kaynak kapısı: sınıflandırıcı RAG'dan SONRA, serbest
// döngüden ÖNCE ([[feedback-tested-but-unreachable]] sınıfı: saf çekirdek
// yeşilken kablosuz kalmasın).
func TestIntentStageOrder(t *testing.T) {
	b, err := os.ReadFile("copilot_chat.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	rag, intent, loop := strings.Index(src, "s.ragChatAnswer("), strings.Index(src, "s.copilotChatIntent("), strings.Index(src, "toolsForRole(")
	if rag < 0 || intent < 0 || loop < 0 || !(rag < intent && intent < loop) {
		t.Fatalf("basamak sırası bozuk: rag=%d intent=%d loop=%d", rag, intent, loop)
	}
}

func TestIntentClassifyTimeout(t *testing.T) {
	for client, want := range map[time.Duration]time.Duration{180 * time.Second: 25 * time.Second, 60 * time.Second: 15 * time.Second, 8 * time.Second: 5 * time.Second} {
		if got := intentClassifyTimeout(client); got != want {
			t.Errorf("client %v → %v, want %v", client, got, want)
		}
	}
	if a := intentStepArgs(strings.Repeat("s", 300)); len([]rune(a)) > 140 || !strings.HasPrefix(a, `{"q":"`) {
		t.Fatalf("args kırpılmadı/JSON değil: %q", a)
	}
}

func TestIntentNoneSuggestions(t *testing.T) {
	if got := intentNoneSuggestions(""); len(got) == 0 {
		t.Fatal("küresel çip listesi boş")
	}
	if got := intentNoneSuggestions("api-gateway"); len(got) == 0 || !strings.Contains(got[0], "api-gateway") {
		t.Fatalf("servis kapsamlı çipler beklendi: %v", got)
	}
}
