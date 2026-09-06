package api

// chat_context.go — v0.10.478 (CoSRE Telemetry Agent Faz 4, F4-1; audit G9,
// Ek A {{ACTIVE_CONTEXT}}): SUNUCUDA tutulan yapılandırılmış sohbet bağlamı.
// Konuşma başına (kullanıcı + conversation id) Redis'te 24 s TTL; modelin
// hafızasına değil, bir tool çağrısının bağlı olduğu her şey buradan okunur.
//
// Doldurma: (1) her başarılı guided cevabın ardından rotadan (servis / aile /
// namespace / pencere / yalnız-hata / arama anahtarları) — set_context'i
// sunucu kendisi yapar; (2) serbest döngüde set_context / clear_context
// tool'ları (context_tools.go). Okuma: guided router'a VARSAYILAN (ekran
// bağlamı yoksa), serbest döngü prompt'una AKTİF BAĞLAM önsözü, operatöre
// çip. Kural (Ek A): açık ifade > sohbet bağlamı > ekran bağlamı.
//
// Konuşma id'si ilk turda henüz yok (kalıcılık cevaptan sonra id verir);
// o turlar kullanıcı-düzeyi "_" anahtarına yazılır ve id gelince oradan
// devralınır.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

// ChatContext — yapılandırılmış çalışma kümesi.
type ChatContext struct {
	Cluster   string `json:"cluster,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Workload  string `json:"workload,omitempty"`
	Service   string `json:"service,omitempty"`
	Pod       string `json:"pod,omitempty"`
	// RangeS — pencere (saniye); RangeExplicit: operatör/tool açıkça koydu
	// (ekran aralığını ezer). Rotadan gelen pencere explicit DEĞİLDİR.
	RangeS        int64 `json:"rangeS,omitempty"`
	RangeExplicit bool  `json:"rangeExplicit,omitempty"`
	ErrorsOnly    bool  `json:"errorsOnly,omitempty"`
	// Filters — son aramanın attribute süzgeçleri; SearchText — aranan değer.
	Filters    []chstore.FilterExpr `json:"filters,omitempty"`
	SearchText string               `json:"searchText,omitempty"`
	LastIntent string               `json:"lastIntent,omitempty"`
	UpdatedAt  int64                `json:"updatedAt,omitempty"`
}

func (c ChatContext) Empty() bool {
	return c.Cluster == "" && c.Namespace == "" && c.Workload == "" && c.Service == "" && c.Pod == "" &&
		c.RangeS == 0 && !c.ErrorsOnly && len(c.Filters) == 0 && c.SearchText == ""
}

const chatContextTTL = 24 * time.Hour

func chatContextKey(user, conv string) string {
	if conv == "" {
		conv = "_"
	}
	return "copilot:ctx:" + user + ":" + conv
}

// chatContextState — istek ömrü boyunca ctx'te taşınan durum.
type chatContextState struct {
	user, conv string
	ctx        ChatContext
	dirty      bool
}

type chatCtxKey struct{}

func ctxWithChatContext(ctx context.Context, st *chatContextState) context.Context {
	return context.WithValue(ctx, chatCtxKey{}, st)
}

func chatContextFromCtx(ctx context.Context) *chatContextState {
	st, _ := ctx.Value(chatCtxKey{}).(*chatContextState)
	return st
}

// loadChatContext — kullanıcı + konuşma için bağlam (yoksa boş); conv
// anahtarı boşsa "_" anahtarına düşer (ilk turlar).
func (s *Server) loadChatContext(ctx context.Context, conv string) *chatContextState {
	user := ""
	if claims := auth.FromContext(ctx); claims != nil {
		user = claims.UserID
	}
	st := &chatContextState{user: user, conv: strings.TrimSpace(conv)}
	if user == "" || s.cache == nil {
		return st
	}
	for _, key := range []string{chatContextKey(user, st.conv), chatContextKey(user, "")} {
		if b, ok, _ := s.cache.Get(ctx, key); ok && len(b) > 0 {
			var c ChatContext
			if json.Unmarshal(b, &c) == nil {
				st.ctx = c
				return st
			}
		}
		if st.conv == "" {
			break
		}
	}
	return st
}

// flushChatContext — kirliyse yaz (konuşma anahtarı + "_" anahtarı).
func (s *Server) flushChatContext(ctx context.Context, st *chatContextState) {
	if st == nil || !st.dirty || st.user == "" || s.cache == nil {
		return
	}
	st.ctx.UpdatedAt = time.Now().UnixNano()
	b, err := json.Marshal(st.ctx)
	if err != nil {
		return
	}
	_ = s.cache.Set(ctx, chatContextKey(st.user, st.conv), b, chatContextTTL)
	_ = s.cache.Set(ctx, chatContextKey(st.user, ""), b, chatContextTTL)
	st.dirty = false
}

// contextPatchFromRoute — SAF: cevaplanan rotadan bağlam yaması. explicitRange:
// soru açık pencere taşıdı (guidedRangeSExplicit). Boş alanlar dokunmaz.
func contextPatchFromRoute(c ChatContext, route guidedRoute, rangeS int64, explicitRange bool) (ChatContext, bool) {
	before := c
	switch {
	case route.Service != "":
		c.Service = route.Service
	case len(route.Family) == 1:
		c.Service = route.Family[0]
	}
	if route.Intent == guidedNamespaceServices && route.FindQuery != "" && !route.FindList {
		c.Namespace = route.FindQuery
	}
	if route.Intent == guidedFamilyTraces || route.Intent == guidedTraceSearch || route.Intent == guidedSlowTraces {
		c.ErrorsOnly = route.TraceErrorsOnly
	}
	if route.Intent == guidedTraceSearch && route.SearchText != "" {
		c.SearchText = route.SearchText
		c.Filters = nil
		for _, k := range route.SearchKeys {
			c.Filters = append(c.Filters, chstore.FilterExpr{Key: k, Op: "=", Values: []string{route.SearchText}})
		}
	}
	if rangeS > 0 {
		c.RangeS = rangeS
		if explicitRange {
			c.RangeExplicit = true
		}
	}
	if route.Intent != guidedNone {
		c.LastIntent = string(route.Intent)
	}
	return c, !chatContextEqual(before, c)
}

func chatContextEqual(a, b ChatContext) bool {
	a.UpdatedAt, b.UpdatedAt = 0, 0
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

// applyContextPatch — SAF: tool'un yaması (map) → bağlam; bilinmeyen alan
// hata (model alan uydurmasın).
func applyContextPatch(c ChatContext, patch map[string]any) (ChatContext, error) {
	for k, v := range patch {
		switch k {
		case "cluster", "namespace", "workload", "service", "pod", "search_text":
			s, ok := v.(string)
			if !ok {
				return c, fmt.Errorf("%s: metin olmalı", k)
			}
			s = strings.TrimSpace(s)
			switch k {
			case "cluster":
				c.Cluster = s
			case "namespace":
				c.Namespace = s
			case "workload":
				c.Workload = s
			case "service":
				c.Service = s
			case "pod":
				c.Pod = s
			case "search_text":
				c.SearchText = s
			}
		case "range_s":
			f, ok := v.(float64)
			if !ok || f < 60 || f > 30*86400 {
				return c, fmt.Errorf("range_s: 60…2592000 saniye")
			}
			c.RangeS, c.RangeExplicit = int64(f), true
		case "errors_only":
			b, ok := v.(bool)
			if !ok {
				return c, fmt.Errorf("errors_only: boolean")
			}
			c.ErrorsOnly = b
		case "filters":
			raw, _ := json.Marshal(v)
			var fs []chstore.FilterExpr
			if err := json.Unmarshal(raw, &fs); err != nil {
				return c, fmt.Errorf("filters: %v", err)
			}
			if err := chstore.ValidateFilters(fs); err != nil {
				return c, fmt.Errorf("filters: %v", err)
			}
			c.Filters = fs
		default:
			return c, fmt.Errorf("bilinmeyen bağlam alanı %q (cluster, namespace, workload, service, pod, range_s, errors_only, filters, search_text)", k)
		}
	}
	return c, nil
}

// clearContextFields — SAF: alan listesi boşsa hepsi.
func clearContextFields(c ChatContext, fields []string) (ChatContext, error) {
	if len(fields) == 0 {
		return ChatContext{}, nil
	}
	for _, f := range fields {
		switch strings.TrimSpace(f) {
		case "cluster":
			c.Cluster = ""
		case "namespace":
			c.Namespace = ""
		case "workload":
			c.Workload = ""
		case "service":
			c.Service = ""
		case "pod":
			c.Pod = ""
		case "range_s", "range":
			c.RangeS, c.RangeExplicit = 0, false
		case "errors_only":
			c.ErrorsOnly = false
		case "filters":
			c.Filters = nil
		case "search_text":
			c.SearchText = ""
		default:
			return c, fmt.Errorf("bilinmeyen bağlam alanı %q", f)
		}
	}
	return c, nil
}

// chatContextPreambleTR — serbest döngü prompt önsözü (ekran bağlamının
// ikizi; Ek A ACTIVE_CONTEXT). Boşsa boş dize.
func chatContextPreambleTR(c ChatContext) string {
	if c.Empty() {
		return ""
	}
	var b strings.Builder
	b.WriteString("AKTİF SOHBET BAĞLAMI — önceki turlarda çözülen çalışma kümesi (set_context ile güncellenir):\n")
	if c.Cluster != "" {
		fmt.Fprintf(&b, "- cluster: %s\n", c.Cluster)
	}
	if c.Namespace != "" {
		fmt.Fprintf(&b, "- namespace: %s\n", c.Namespace)
	}
	if c.Workload != "" {
		fmt.Fprintf(&b, "- workload: %s\n", c.Workload)
	}
	if c.Service != "" {
		fmt.Fprintf(&b, "- servis: %s\n", c.Service)
	}
	if c.Pod != "" {
		fmt.Fprintf(&b, "- pod: %s\n", c.Pod)
	}
	if c.RangeS > 0 {
		fmt.Fprintf(&b, "- zaman aralığı: %s (range_s=%d)\n", fmtRangeTR(c.RangeS), c.RangeS)
	}
	if c.ErrorsOnly {
		b.WriteString("- yalnız hatalı trace'ler\n")
	}
	if c.SearchText != "" {
		fmt.Fprintf(&b, "- aranan değer: %q\n", c.SearchText)
	}
	if len(c.Filters) > 0 {
		fb, _ := json.Marshal(c.Filters)
		fmt.Fprintf(&b, "- süzgeçler: %s\n", string(fb))
	}
	b.WriteString("\"onun içinde\", \"bu servisin\", \"aynı filtreyle\", \"son 1 saate genişlet\", \"sadece hatalı olanlar\" gibi ifadeler BU kümeyi değiştirir, sıfırdan başlamaz. Operatör konu değiştirirse varlık alanlarını değiştir ama pencereyi koru. Boş argümanı buradan doldur ve set_context ile değişeni yaz.\n\n")
	return b.String()
}

// chatContextChipTR — operatöre görünen özet.
func chatContextChipTR(c ChatContext) string {
	if c.Empty() {
		return ""
	}
	var parts []string
	for _, p := range []string{c.Cluster, c.Namespace, c.Workload, c.Service, c.Pod} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if c.RangeS > 0 {
		parts = append(parts, fmtRangeTR(c.RangeS))
	}
	if c.ErrorsOnly {
		parts = append(parts, "yalnız hatalı")
	}
	if c.SearchText != "" {
		parts = append(parts, "\""+c.SearchText+"\"")
	}
	return "sohbet bağlamı: " + strings.Join(parts, " · ")
}

// contextToolView — get_context çıktısı (tool JSON şekli).
func contextToolView(c ChatContext) map[string]any {
	out := map[string]any{}
	if c.Cluster != "" {
		out["cluster"] = c.Cluster
	}
	if c.Namespace != "" {
		out["namespace"] = c.Namespace
	}
	if c.Workload != "" {
		out["workload"] = c.Workload
	}
	if c.Service != "" {
		out["service"] = c.Service
	}
	if c.Pod != "" {
		out["pod"] = c.Pod
	}
	if c.RangeS > 0 {
		out["range_s"] = c.RangeS
	}
	if c.ErrorsOnly {
		out["errors_only"] = true
	}
	if len(c.Filters) > 0 {
		out["filters"] = c.Filters
	}
	if c.SearchText != "" {
		out["search_text"] = c.SearchText
	}
	if c.LastIntent != "" {
		out["last_intent"] = c.LastIntent
	}
	return out
}

// chatContextTools — mcptools.Deps kapanışları: tool handler'ları ctx'teki
// state'i okur/yazar; bağlam state'i yoksa (dış MCP çağrısı) dürüst hata.
func (s *Server) chatContextGet(ctx context.Context) (map[string]any, error) {
	st := chatContextFromCtx(ctx)
	if st == nil {
		return nil, fmt.Errorf("sohbet bağlamı yalnız in-app CoSRE sohbetinde tutulur (bu çağrıda konuşma yok)")
	}
	return contextToolView(st.ctx), nil
}

func (s *Server) chatContextSet(ctx context.Context, patch map[string]any) (map[string]any, error) {
	st := chatContextFromCtx(ctx)
	if st == nil {
		return nil, fmt.Errorf("sohbet bağlamı yalnız in-app CoSRE sohbetinde tutulur (bu çağrıda konuşma yok)")
	}
	next, err := applyContextPatch(st.ctx, patch)
	if err != nil {
		return nil, err
	}
	st.ctx, st.dirty = next, true
	return contextToolView(st.ctx), nil
}

func (s *Server) chatContextClear(ctx context.Context, fields []string) (map[string]any, error) {
	st := chatContextFromCtx(ctx)
	if st == nil {
		return nil, fmt.Errorf("sohbet bağlamı yalnız in-app CoSRE sohbetinde tutulur (bu çağrıda konuşma yok)")
	}
	next, err := clearContextFields(st.ctx, fields)
	if err != nil {
		return nil, err
	}
	st.ctx, st.dirty = next, true
	return contextToolView(st.ctx), nil
}

// noteChatContextRoute — başarılı guided cevabın ardından rotadan yama.
func (s *Server) noteChatContextRoute(ctx context.Context, route guidedRoute, rangeS int64, explicitRange bool) {
	st := chatContextFromCtx(ctx)
	if st == nil {
		return
	}
	if next, changed := contextPatchFromRoute(st.ctx, route, rangeS, explicitRange); changed {
		st.ctx, st.dirty = next, true
	}
}
