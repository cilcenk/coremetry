package provider

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// toolcall_text.go — v0.10.545 (CoSRE agent Faz 2, Adım 0 riski): METİN-GÖMÜLÜ
// tool çağrısı geri düşüşü.
//
// Gemma 4 (ve bazı Hermes/pythonic şablonlu modeller) function calling'i özel
// token'lı kendi biçimiyle üretir; OpenAI-uyumlu sunucuda parser yoksa çağrı
// `tool_calls` yerine `content` içinde METİN olarak kalır ve tur "tool
// çağırmadı" gibi görünür. Bu dosya yalnız `tool_calls` BOŞKEN devreye girer:
// yapılandırılmış çağrı geldiğinde sıfır maliyet.
//
// Tanınan biçimler (scripts/dev/gemma4_toolcall_smoke.py ham çıktısıyla
// genişletilir — gerçek Gemma 4 token'ları DOĞRULANMADI):
//   1. Hermes: <tool_call>{"name":…,"arguments":{…}}</tool_call>
//   2. Özel token: <|tool_call|>…<|/tool_call|>, <start_function_call>…<end_function_call>
//   3. Çit: ```json {"name":…,"arguments":…} ``` / ```tool_code name(k=v) ```
//   4. Çıplak JSON nesnesi {"name":"…","arguments"|"parameters":{…}} (ya da dizisi)
//   5. Pythonic: name(key="value", n=1, obj={"a":1})
// Kenar durumlar: düşünme bloğu önce soyulur (StripThinking); kesik JSON →
// çağrı yok (ok=false), çağıran finish_reason=length'i ayrıca raporlar; aynı
// içerikte birden çok çağrı sırayla; çağrı + düz metin karışıksa metin `rest`;
// Unicode json.Unmarshal ile korunur; arguments dize ya da nesne kabul.

var (
	textCallDelims = []*regexp.Regexp{
		regexp.MustCompile(`(?s)<tool_call>\s*(.*?)\s*</tool_call>`),
		regexp.MustCompile(`(?s)<\|tool_call\|>\s*(.*?)\s*<\|/tool_call\|>`),
		regexp.MustCompile(`(?s)<\|tool_call>\s*(.*?)\s*<tool_call\|>`),
		regexp.MustCompile(`(?s)<start_function_call>\s*(.*?)\s*<end_function_call>`),
		regexp.MustCompile("(?s)```(?:json|tool_call|tool_code|python)?\\s*\\n?(.*?)```"),
	}
	gemmaThoughtRe = regexp.MustCompile(`(?s)<\|channel>thought.*?<channel\|>`)
	textCallIdent  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,80}$`)
	pythonicCall   = regexp.MustCompile(`(?s)^\s*(?:call:\s*)?([A-Za-z_][A-Za-z0-9_.-]{0,80})\s*\((.*)\)\s*$`)
)

// ParseTextToolCalls — content içindeki çağrıları ayrıştırır. known boş
// değilse yalnız o adlar kabul edilir (model uydurmasına karşı). ok=false:
// hiçbir çağrı çözülemedi (çağıran metni cevap sayar).
func ParseTextToolCalls(content string, known []string) (calls []ToolCall, rest string, ok bool) {
	// Gemma kanal token'lı düşünce bloğu (<|channel>thought … <channel|>) —
	// StripThinking yalnız <think> tanır; iki biçim de soyulur.
	body := StripThinking(gemmaThoughtRe.ReplaceAllString(content, ""))
	if strings.TrimSpace(body) == "" {
		return nil, content, false
	}
	allowed := map[string]bool{}
	for _, k := range known {
		allowed[k] = true
	}
	accept := func(name string) bool {
		if !textCallIdent.MatchString(name) {
			return false
		}
		return len(allowed) == 0 || allowed[name]
	}
	consumed := body
	seq := 0
	add := func(name string, args json.RawMessage) {
		seq++
		calls = append(calls, ToolCall{ID: fmt.Sprintf("call_text_%d", seq), Name: name, Input: args, Raw: nil})
	}
	// 1-3: sınırlayıcılı segmentler
	for _, re := range textCallDelims {
		for _, m := range re.FindAllStringSubmatch(consumed, -1) {
			inner := strings.TrimSpace(m[1])
			if n := parseCallSegment(inner, accept, add); n > 0 {
				consumed = strings.Replace(consumed, m[0], "", 1)
			}
		}
	}
	// 4-5: sınırlayıcısız çıplak JSON / pythonic (yalnız hiç çağrı yoksa —
	// düz metnin içindeki `{"name":…}` benzeri örnekleri yanlış yakalamamak için
	// tüm metin tek bir çağrı olmalı ya da satır başında durmalı).
	if len(calls) == 0 {
		lines := strings.Split(strings.TrimSpace(consumed), "\n")
		var kept []string
		for _, l := range lines {
			t := strings.TrimSpace(l)
			if (strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") || pythonicCall.MatchString(t)) && parseCallSegment(t, accept, add) > 0 {
				continue
			}
			kept = append(kept, l)
		}
		if len(calls) > 0 {
			consumed = strings.Join(kept, "\n")
		}
	}
	if len(calls) == 0 {
		return nil, content, false
	}
	return calls, strings.TrimSpace(consumed), true
}

// parseCallSegment — bir segmentten 0..n çağrı; JSON nesnesi/dizisi ya da pythonic.
func parseCallSegment(seg string, accept func(string) bool, add func(string, json.RawMessage)) int {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return 0
	}
	if strings.HasPrefix(seg, "[") {
		var arr []json.RawMessage
		if json.Unmarshal([]byte(seg), &arr) != nil {
			return 0
		}
		n := 0
		for _, el := range arr {
			n += parseCallSegment(string(el), accept, add)
		}
		return n
	}
	if strings.HasPrefix(seg, "{") {
		var obj struct {
			Name       string          `json:"name"`
			Arguments  json.RawMessage `json:"arguments"`
			Parameters json.RawMessage `json:"parameters"`
			Function   *struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		}
		if json.Unmarshal([]byte(seg), &obj) != nil { // kesik/bozuk JSON → çağrı yok
			return 0
		}
		name, args := obj.Name, obj.Arguments
		if obj.Function != nil && obj.Function.Name != "" {
			name, args = obj.Function.Name, obj.Function.Arguments
		}
		if len(args) == 0 {
			args = obj.Parameters
		}
		if name == "" || !accept(name) {
			return 0
		}
		add(name, normalizeArgs(args))
		return 1
	}
	if m := pythonicCall.FindStringSubmatch(seg); m != nil && accept(m[1]) {
		if args, ok := pythonicArgs(m[2]); ok {
			add(m[1], args)
			return 1
		}
	}
	return 0
}

// normalizeArgs — arguments dize (JSON metni) ya da nesne; boş → {}.
func normalizeArgs(raw json.RawMessage) json.RawMessage {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return json.RawMessage("{}")
	}
	if strings.HasPrefix(s, "\"") {
		var inner string
		if json.Unmarshal(raw, &inner) == nil {
			inner = strings.TrimSpace(inner)
			if inner == "" {
				return json.RawMessage("{}")
			}
			if json.Valid([]byte(inner)) {
				return json.RawMessage(inner)
			}
			return json.RawMessage("{}")
		}
	}
	if json.Valid(raw) {
		return raw
	}
	return json.RawMessage("{}")
}

// pythonicArgs — `k="v", n=1, o={"a":1}, l=[1,2]` → JSON nesnesi. Üst düzey
// virgül ayrımı tırnak/parantez derinliğine göre; değerler JSON olarak
// çözülür, çözülemeyen çıplak değer dize kabul edilir.
func pythonicArgs(s string) (json.RawMessage, bool) {
	out := map[string]json.RawMessage{}
	for _, part := range splitTopLevel(s) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.Index(part, "=")
		if eq <= 0 {
			return nil, false
		}
		k := strings.TrimSpace(part[:eq])
		v := strings.TrimSpace(part[eq+1:])
		if !textCallIdent.MatchString(k) {
			return nil, false
		}
		if strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") && len(v) >= 2 {
			v = `"` + strings.ReplaceAll(v[1:len(v)-1], `"`, `\"`) + `"`
		}
		switch v {
		case "True":
			v = "true"
		case "False":
			v = "false"
		case "None":
			v = "null"
		}
		if !json.Valid([]byte(v)) {
			b, _ := json.Marshal(v)
			v = string(b)
		}
		out[k] = json.RawMessage(v)
	}
	b, err := json.Marshal(out)
	return b, err == nil
}

func splitTopLevel(s string) []string {
	var parts []string
	depth := 0
	inStr := byte(0)
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr != 0:
			if c == '\\' {
				i++
			} else if c == inStr {
				inStr = 0
			}
		case c == '"' || c == '\'':
			inStr = c
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
		case c == ',' && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}
