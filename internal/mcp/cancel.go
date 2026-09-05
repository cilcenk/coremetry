package mcp

// cancel.go — v0.10.427 (CoSRE denetimi M4): notifications/cancelled.
// Eskiden bildirim "unknown notification" diye loglanıp yutuluyordu;
// istemci vazgeçse de ağır ClickHouse okuması sonuna dek koşuyordu
// (tavan yalnız ToolCallBudget 20 sn).
//
// Kapsam: HTTP+SSE transport'u (oturumlu). Streamable-HTTP stateless ve
// pod-yerel: iptal bildirimi BAŞKA bir POST'la (belki başka bir pod'a)
// gelir, ilişkilendirme mümkün değil; spec "bilinmeyen/tamamlanmış id'yi
// yok sayabilir" der, öyle yapılır. Oturumsuz yolda iptal DESTEKLENMEZ.
//
// Spec (Cancellation, 2025-03-26/06-18): params.requestId + reason;
// alıcı işlemeyi DURDURMALI ve iptal edilen isteğe yanıt GÖNDERMEMELİ
// (SHOULD); gönderici geç yanıtı yok saymalı; initialize iptal edilemez.
// Burada: SSE yolunda yanıt bastırılır (mezar taşı), initialize/ping/
// *-list kayıt edilmez (iptal edilemez), oturum kapanınca kayıt düşer.
//
// Anahtar OTURUM KAPSAMLI ve kanonik (json round-trip): ham RawMessage
// karşılaştırması `1` / `1.0` / boşlukta kırılır; oturumsuz anahtar
// A istemcisinin B'nin isteğini iptal etmesine izin verirdi.
//
// Rate bütçesi iade EDİLMEZ: iptal edilen çağrı jetonunu yaktı (aksi
// hâlde iptal-döngüsü 60/dk sınırını aşar).

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"strings"
)

// canonicalID — JSON-RPC id'sinin kanonik yazımı; çözülemezse ham metin.
//
// v0.10.430 — sayılar json.Number ile okunur: `any` yolu her sayıyı
// float64'e indiriyordu ve 2^53 üstü tam sayılar (nanosaniye zaman
// damgası, 64-bit rastgele id — ikisi de geçerli JSON-RPC id) aynı
// anahtara çöküyordu: B'nin kaydı A'nınkini eziyor, A iptal edilemiyor,
// A'nın iptali B'yi öldürüyordu. Tam sayı literali AYNEN kalır; kesirli/
// üslü yazım (`7.0`, `1e2`) float üzerinden `7`/`100`'e katlanır (spec
// kesir önermez, istemciler yine de yolluyor — M4 testi 7.0 gönderir).
func canonicalID(raw json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return string(raw)
	}
	if n, ok := v.(json.Number); ok {
		s := string(n)
		if !strings.ContainsAny(s, ".eE") {
			return s
		}
		f, err := n.Float64()
		if err != nil {
			return s
		}
		b, _ := json.Marshal(f)
		return string(b)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(b)
}

// cancellable — iptal edilebilen (ClickHouse okuyan) yöntemler.
func cancellable(method string) bool {
	switch method {
	case "tools/call", "resources/read", "prompts/get":
		return true
	}
	return false
}

// track — isteği oturumun uçuş defterine yazar; dönen finish() kaydı
// düşürür ve istek İSTEMCİ tarafından iptal edildiyse true döner (yanıt
// bastırılır). İptal edilemeyen yöntemde ctx aynen, finish false.
func (sess *session) track(ctx context.Context, req *Request) (context.Context, func() bool) {
	if sess == nil || !cancellable(req.Method) || req.IsNotification() {
		return ctx, func() bool { return false }
	}
	key := canonicalID(req.ID)
	ctx, cancel := context.WithCancel(ctx)
	sess.mu.Lock()
	if sess.inflight == nil {
		sess.inflight = map[string]context.CancelFunc{}
	}
	sess.inflight[key] = cancel
	sess.mu.Unlock()
	return ctx, func() bool {
		cancel()
		sess.mu.Lock()
		defer sess.mu.Unlock()
		delete(sess.inflight, key)
		if sess.cancelled[key] {
			delete(sess.cancelled, key)
			return true
		}
		return false
	}
}

// cancelRequest — notifications/cancelled: uçuştaki isteği iptal eder ve
// mezar taşı bırakır; bilinmeyen/tamamlanmış id sessiz no-op (spec MAY).
func (sess *session) cancelRequest(key string) bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	cancel, ok := sess.inflight[key]
	if !ok {
		return false
	}
	if sess.cancelled == nil {
		sess.cancelled = map[string]bool{}
	}
	sess.cancelled[key] = true
	cancel()
	return true
}

type cancelledParams struct {
	RequestID json.RawMessage `json:"requestId"`
	Reason    string          `json:"reason"`
}

// handleCancelled — dispatchNotification'dan; sess nil ise (streamable)
// yok sayılır. Bildirimin kendi ctx'i POST'la birlikte ölmüş olabilir —
// kullanılmaz.
func (s *Server) handleCancelled(sess *session, req *Request) {
	if sess == nil {
		return
	}
	var p cancelledParams
	if err := json.Unmarshal(req.Params, &p); err != nil || len(p.RequestID) == 0 {
		return
	}
	key := canonicalID(p.RequestID)
	if sess.cancelRequest(key) {
		log.Printf("[mcp] session=%s request %s cancelled by client (reason=%q) — response suppressed", sess.id, key, p.Reason)
	}
}
