package copilot

import (
	"context"
	"errors"
	"net"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	aiprov "github.com/cilcenk/coremetry/internal/ai/provider"
)

// v0.10.409 — prompt sürümü: prompts.go'da LİTERAL taşıyan her `const`
// kayıtta (AD ile — dosya metnini grep'lemek yerine haritanın anahtarı;
// inceleme bulgusu: eski pin yorumdaki adı da "kayıtlı" sayıyordu). Saf
// bileşimler (systemTrace = systemTraceBody + AnswerInTurkish) parçalarıyla
// kapsanır; kuyruk literali olan bileşikler (systemChatRoundCap) kayıtta.
func TestPromptVersionRegistryCoversEveryPrompt(t *testing.T) {
	b, err := os.ReadFile("prompts.go")
	if err != nil {
		t.Fatal(err)
	}
	exempt := map[string]bool{"fenceLit": true, "CodeFrameMarker": true, "answerInTurkishLine": true}
	re := regexp.MustCompile("(?m)^const ([A-Za-z0-9_]+) = (.*)$")
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		name, rhs := m[1], m[2]
		seen[name] = true
		if exempt[name] {
			continue
		}
		if !strings.Contains(rhs, "`") && !strings.Contains(rhs, "\"") {
			continue // saf bileşim
		}
		if _, ok := promptVersionRegistry[name]; !ok {
			t.Errorf("prompts.go sabiti %s promptVersionRegistry'de yok — sürüm değişimi görünmez", name)
		}
	}
	for name := range promptVersionRegistry {
		if !seen[name] {
			t.Errorf("kayıttaki %s prompts.go'da const değil", name)
		}
	}
	if len(promptVersionRegistry) < 30 {
		t.Fatalf("registry %d — beklenenden küçük", len(promptVersionRegistry))
	}
	v := PromptVersion()
	if len(v) != 16 || v != PromptVersion() {
		t.Fatalf("sürüm 16 hex ve kararlı olmalı: %q", v)
	}
}

type fakeNetTimeout struct{}

func (fakeNetTimeout) Error() string   { return "dial tcp: i/o timeout" }
func (fakeNetTimeout) Timeout() bool   { return true }
func (fakeNetTimeout) Temporary() bool { return true }

// v0.10.409 — hata sınıfları: DOKUZ sınıfın her biri en az bir vakayla;
// typed yol + metin yolu (chat.go yalnız metin görür).
func TestClassifyAIError(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"deadline":       {context.DeadlineExceeded, ErrClassTimeout},
		"net timeout":    {fakeNetTimeout{}, ErrClassTimeout},
		"cancelled":      {context.Canceled, ErrClassCancelled},
		"cancel text":    {errors.New("openai: Post \"…\": context canceled"), ErrClassCancelled},
		"unavailable":    {&net.OpError{Op: "dial", Err: errors.New("connection refused")}, ErrClassUnavailable},
		"unavail text":   {errors.New("dial tcp 10.0.0.1:8000: connect: connection refused"), ErrClassUnavailable},
		"quota http":     {&aiprov.HTTPError{Provider: "openai", Status: 429, Body: "rate limit"}, ErrClassQuota},
		"5xx":            {&aiprov.HTTPError{Provider: "openai", Status: 502, Body: "bad gateway"}, ErrClassHTTP5xx},
		"4xx":            {&aiprov.HTTPError{Provider: "openai", Status: 400, Body: "bad request"}, ErrClassHTTP4xx},
		"overflow":       {&aiprov.HTTPError{Provider: "openai", Status: 400, Body: "This model's maximum context length is 8192 tokens"}, ErrClassContextOverflow},
		"refusal":        {errors.New("anthropic: model isteği reddetti (stop_reason=refusal)"), ErrClassRefusal},
		"text quota":     {errors.New("openai 429: too many requests"), ErrClassQuota},
		"text 4xx":       {errors.New("openai-compat 400: invalid request"), ErrClassHTTP4xx},
		"text 5xx late":  {errors.New("chat turn 5: openai-compat 502: bad gateway"), ErrClassHTTP5xx},
		"text ms no hit": {errors.New("took 450ms then failed"), ErrClassOther},
		"other":          {errors.New("weird"), ErrClassOther},
		"nil":            {nil, ""},
	}
	hit := map[string]bool{}
	for name, c := range cases {
		got := ClassifyAIError(c.err)
		if got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
		hit[got] = true
	}
	for _, cls := range []string{ErrClassTimeout, ErrClassQuota, ErrClassRefusal, ErrClassContextOverflow, ErrClassHTTP4xx, ErrClassHTTP5xx, ErrClassUnavailable, ErrClassCancelled, ErrClassOther} {
		if !hit[cls] {
			t.Errorf("sınıf %q hiçbir vakayla vurulmadı", cls)
		}
	}
}

// v0.10.409 — TTFT ve düşüş bayrağı bağlam üzerinden.
func TestStreamStats(t *testing.T) {
	st := &streamStats{started: time.Now().Add(-200 * time.Millisecond)}
	ctx := withStreamStats(context.Background(), st)
	if streamStatsFromContext(ctx) != st || streamStatsFromContext(context.Background()) != nil {
		t.Fatal("bağlam taşıması")
	}
	if st.ttftMs() != 0 {
		t.Fatal("ilk token gelmeden TTFT 0")
	}
	st.markFirst()
	if got := st.ttftMs(); got < 200 || got > 10_000 {
		t.Fatalf("ttft = %d", got)
	}
	first := st.ttftMs()
	time.Sleep(5 * time.Millisecond)
	st.markFirst()
	if st.ttftMs() != first {
		t.Fatal("ilk damga değişmemeli")
	}
	if st.fallbackSet() {
		t.Fatal("başta düşüş yok")
	}
	markStreamFallback(ctx)
	if !st.fallbackSet() {
		t.Fatal("fallback işaretlenmeli")
	}
	markStreamFallback(context.Background()) // bağlamsız çağrı panik yapmaz
}

// v0.10.409 — her BUFFERED yol düşüş sayar: iki sağlayıcının önbellekli
// "akış desteklenmiyor" erken dönüşü, iki gerçek düşüş ve GitHub'ın
// akışsız dalı (inceleme bulgusu: yalnız ilk düşüş sayılıyordu, ikinci
// çağrıdan itibaren ttft=0 + fallback=0 "sağlıklı akış" gibi görünüyordu).
func TestStreamTextMarksEveryBufferedPath(t *testing.T) {
	b, err := os.ReadFile("stream.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if n := strings.Count(src, "markStreamFallback(ctx)"); n < 5 {
		t.Fatalf("stream.go'da %d markStreamFallback çağrısı var, en az 5 olmalı (2 önbellekli + 2 düşüş + GitHub)", n)
	}
	for _, guard := range []string{
		"streamKnownUnsupported(ProviderOpenAI, base, model) {",
		"streamKnownUnsupported(ProviderAnthropic, \"\", model) {",
		"case ProviderGitHub:",
	} {
		i := strings.Index(src, guard)
		if i < 0 {
			t.Fatalf("kapı bulunamadı: %s", guard)
		}
		if !strings.Contains(src[i:i+400], "markStreamFallback(ctx)") {
			t.Errorf("%s bloğu düşüşü işaretlemiyor", guard)
		}
	}
}
