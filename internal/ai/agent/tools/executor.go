// Package tools — v0.10.536 (CoSRE v2 Faz 2.3): tool YÜRÜTME tek yol.
//
// Sohbet döngüsü (api/copilot_chat.go) bilinmeyen ad, tekrar muhafızı,
// zaman bütçesi, JSON'lama, hata sözleşmesi (mcp.ToolErrorJSON), ai.tool
// span'ı ve audit satırını döngü gövdesine elle diziyordu; explain/insight
// yolları tool çağıramıyordu. Executor bu diziyi tek yerde tutar; çağıran
// yalnız Outcome'u yayınlar (çip, kanıt, köprü) ve konuşmaya ekler.
//
// Scope — Faz 2'de KISITSIZ (Unrestricted): G13 (LDAP grup → cluster/
// namespace) kararı gelince Constrain doldurulur; tool argümanı sunucuda
// daraltılır, prompt'a yazılmaz.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/mcp"
)

// Handler — mcp.ToolHandler ile aynı imza (sohbet byName haritası).
type Handler = mcp.ToolHandler

// Scope — kullanıcı veri kapsamı; Constrain argümanı daraltır ya da
// çağrıyı reddeder (hata metni modele ToolErrorJSON ile gider).
type Scope interface {
	Constrain(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
}

// Unrestricted — bugünkü davranış: kapsam yok.
type Unrestricted struct{}

func (Unrestricted) Constrain(_ context.Context, _ string, args json.RawMessage) (json.RawMessage, error) {
	return args, nil
}

// Hooks — gözlem kancaları; nil alan = atla.
type Hooks struct {
	// Span — ai.tool span'ı: yürütmeyi sarar, bayt + ok ile kapanır.
	Span func(ctx context.Context, name string, external bool) (context.Context, func(bytes int, isErr bool))
	// Audit — yürütülen her çağrı (başarısız dâhil); yürütülmeyenler (bilinmeyen/tekrar) hariç.
	Audit func(name string, args json.RawMessage, dur time.Duration, err error, bytes int)
}

// Outcome — bir çağrının sonucu.
type Outcome struct {
	// Content — modele giden metin: JSON sonuç, ToolErrorJSON, RepeatedCallJSON ya da "unknown tool".
	Content string
	IsError bool
	// Executed — handler gerçekten koştu (bilinmeyen ad ve tekrar: false; süre yazılmaz).
	Executed bool
	// Kind — ok | error | unknown | repeated | scope
	Kind     string
	Duration time.Duration
	Err      error
}

type Executor struct {
	byName   map[string]Handler
	external map[string]bool
	seen     map[string]bool
	budget   time.Duration
	hooks    Hooks
	scope    Scope
}

// NewExecutor — byName yürütme haritası, external köken etiketi (ai.tool
// origin). Bütçe mcp.ToolCallBudget (telle aynı).
func NewExecutor(byName map[string]Handler, external map[string]bool, hooks Hooks) *Executor {
	return &Executor{byName: byName, external: external, seen: map[string]bool{}, budget: mcp.ToolCallBudget, hooks: hooks, scope: Unrestricted{}}
}

// WithScope — kapsam enjeksiyonu (G13); nil = kısıtsız.
func (x *Executor) WithScope(s Scope) *Executor {
	if s != nil {
		x.scope = s
	}
	return x
}

// WithBudget — testler ve ileride yüzey-başına bütçe.
func (x *Executor) WithBudget(d time.Duration) *Executor {
	if d > 0 {
		x.budget = d
	}
	return x
}

// Known — ad katalogda mı.
func (x *Executor) Known(name string) bool { _, ok := x.byName[name]; return ok }

// Call — sıra sözleşmesi (chat döngüsünün eski gövdesiyle birebir):
// bilinmeyen → tekrar muhafızı → kapsam → span aç → bütçeli yürütme →
// JSON'la / ToolErrorJSON → span kapat(bayt, ok) → audit.
func (x *Executor) Call(ctx context.Context, name string, args json.RawMessage) Outcome {
	h, found := x.byName[name]
	if !found {
		msg := fmt.Sprintf("unknown tool %q", name)
		return Outcome{Content: msg, IsError: true, Kind: "unknown"}
	}
	// v0.10.88 — aynı (tool, kanonik argüman) çiftinin ikinci kopyası
	// YÜRÜTÜLMEZ; model ToolErrorJSON alanlarıyla yönlendirilir.
	if MarkRepeatedCall(x.seen, name, args) {
		return Outcome{Content: RepeatedCallJSON, IsError: true, Kind: "repeated"}
	}
	cargs, serr := x.scope.Constrain(ctx, name, args)
	if serr != nil {
		return Outcome{Content: mcp.ToolErrorJSON(serr), IsError: true, Kind: "scope", Err: serr}
	}
	end := func(int, bool) {}
	if x.hooks.Span != nil {
		ctx, end = x.hooks.Span(ctx, name, x.external[name])
	}
	t0 := time.Now()
	out, herr := runTool(ctx, h, cargs, x.budget)
	dur := time.Since(t0)
	oc := Outcome{Executed: true, Duration: dur, Err: herr, Kind: "ok"}
	if herr != nil {
		// v0.9.1234 — MCP telinin gördüğü sözleşmenin AYNISI (sınıf +
		// tekrar denenebilirlik + Türkçe ipucu + kırpılmış ham metin).
		oc.Content, oc.IsError, oc.Kind = mcp.ToolErrorJSON(herr), true, "error"
	} else {
		oc.Content = out
	}
	end(len(oc.Content), oc.IsError)
	if x.hooks.Audit != nil {
		x.hooks.Audit(name, cargs, dur, herr, len(oc.Content))
	}
	return oc
}

// runTool — bütçeli yürütme + JSON'lama (v0.10.401: telle aynı bütçe).
func runTool(ctx context.Context, h Handler, args json.RawMessage, budget time.Duration) (string, error) {
	tctx, cancel := context.WithTimeout(ctx, budget)
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

// ── tekrar muhafızı (v0.10.88, chat_mcp_bridge.go'dan taşındı) ────────────

// RepeatedCallJSON — muhafızın modele verdiği sonuç; mcp.ToolErrorJSON
// alanları (error/retryable/hint): model iki hata biçimi görmesin.
const RepeatedCallJSON = `{"error":"repeated_call","retryable":false,` +
	`"hint":"Bu tool bu argümanlarla bu konuşmada zaten çağrıldı; sonucu yukarıdaki ` +
	`turda duruyor. Farklı argüman dene (aralığı genişlet, filtreyi değiştir) ya da ` +
	`eldeki veriyle cevap ver."}`

// MarkRepeatedCall — kaydeder; aynı (tool, kanonik argüman) çiftinin ikinci
// ve sonraki kopyalarında true. Anahtar kanonik: JSON anahtar sırası
// değişse de aynı çağrı; çözülemeyen argüman ham hâliyle anahtarlanır.
func MarkRepeatedCall(seen map[string]bool, name string, raw json.RawMessage) bool {
	key := RepeatCallKey(name, raw)
	if seen[key] {
		return true
	}
	seen[key] = true
	return false
}

// RepeatCallKey — muhafız anahtarı (ad + kanonik argüman); testler için dışa açık.
func RepeatCallKey(name string, raw json.RawMessage) string {
	var v any
	if len(raw) > 0 && json.Unmarshal(raw, &v) == nil {
		if b, err := json.Marshal(v); err == nil {
			return name + "\x00" + string(b)
		}
	}
	return name + "\x00" + strings.TrimSpace(string(raw))
}
