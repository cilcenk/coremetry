package mcpclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// transport_http.go — STREAMABLE HTTP taşıması.
//
// Her JSON-RPC mesajı tek POST; sunucu ya düz application/json (tek
// yanıt) ya da text/event-stream (aynı isteğin yanıtı + araya bildirim)
// döndürür. Oturum kimliği initialize yanıtının Mcp-Session-Id
// başlığından alınır ve sonraki her isteğe geri yazılır.
//
// ⚠ BAĞIMSIZ GET akışı BİLİNÇLİ olarak AÇILMIYOR (dilim ① sınırı):
// bildirimler yalnız POST yanıt akışlarına binmiş hâlleriyle görülür.
// Bu bir kayıp değil, kabul edilmiş bir gecikme — bildirim sadece
// önbellek ipucu ve Registry'nin TTL'i zaten emniyet ağı.
//
// Gövde tavanı: her yanıt io.LimitReader ile kırpılır (devops
// doGetCapped disiplini) — boyutu bilinmeyen bir dış sunucu belleği
// dolduramaz.

// httpBodyCap — tek HTTP yanıtının okunacak azami gövdesi.
const httpBodyCap = 1 << 20 // 1 MiB

type httpTransport struct {
	url   string
	token string
	cli   *http.Client

	mu      sync.Mutex
	session string

	seq   atomic.Int64
	notif chan string
}

// newHTTPTransport — cli nil ise kendi istemcisini kurar (timeout
// callTimeout'tan uzun: SSE akışı yanıtı geciktirebilir; asıl tavan
// çağrı ctx'inde).
func newHTTPTransport(cfg ServerConfig, cli *http.Client) *httpTransport {
	if cli == nil {
		tr := &http.Transport{}
		if cfg.InsecureSkipVerify {
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}
		cli = &http.Client{Timeout: 60 * time.Second, Transport: tr}
	}
	return &httpTransport{url: cfg.URL, token: cfg.Token, cli: cli, notif: make(chan string, 8)}
}

func (t *httpTransport) Notifications() <-chan string { return t.notif }
func (t *httpTransport) Close() error                 { return nil }

func (t *httpTransport) Notify(ctx context.Context, method string, params any) error {
	_, err := t.post(ctx, rpcEnvelope{JSONRPC: "2.0", Method: method, Params: params}, nil)
	return err
}

func (t *httpTransport) Call(ctx context.Context, method string, params, result any) error {
	id := t.seq.Add(1)
	env, err := t.post(ctx, rpcEnvelope{JSONRPC: "2.0", ID: &id, Method: method, Params: params}, &id)
	if err != nil {
		return err
	}
	if env == nil {
		return fmt.Errorf("mcp %s: sunucu yanıt zarfı döndürmedi", method)
	}
	if env.Error != nil {
		return env.Error
	}
	if result != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, result); err != nil {
			return fmt.Errorf("mcp %s: yanıt çözümlenemedi: %w", method, err)
		}
	}
	return nil
}

// post — zarfı gönderir; wantID doluysa o kimliğe ait yanıt zarfını
// arayıp döndürür (bildirimler yol üstünde kanala düşer).
func (t *httpTransport) post(ctx context.Context, env rpcEnvelope, wantID *int64) (*rpcEnvelope, error) {
	body, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	t.mu.Lock()
	if t.session != "" {
		req.Header.Set("Mcp-Session-Id", t.session)
	}
	t.mu.Unlock()

	resp, err := t.cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		t.mu.Lock()
		t.session = s
		t.mu.Unlock()
	}
	// Tek biçim kimlik hatası (devops doGet sözleşmesi): 401 ile 403'ü
	// ayrı ayrı açıklamak yerine operatörün yapacağı tek işi söyle.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("mcp sunucusu kimliği reddetti (http %d) — token'ı Ayarlar'dan kontrol edin", resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("mcp http %d: %s", resp.StatusCode, firstLineTrim(string(raw)))
	}
	// Bildirim POST'u (202/204, gövdesiz) — zarf beklenmez.
	if wantID == nil && (resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent) {
		return nil, nil
	}

	ct := resp.Header.Get("Content-Type")
	limited := io.LimitReader(resp.Body, httpBodyCap)
	if strings.HasPrefix(ct, "text/event-stream") {
		return t.readStream(limited, wantID)
	}
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if wantID == nil {
		return nil, nil
	}
	var out rpcEnvelope
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("mcp yanıtı çözümlenemedi: %w", err)
	}
	return &out, nil
}

// readStream — SSE gövdesinden `data:` satırlarını okur; wantID'nin
// yanıtı gelene dek bildirimleri kanala akıtır.
func (t *httpTransport) readStream(r io.Reader, wantID *int64) (*rpcEnvelope, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		var env rpcEnvelope
		if json.Unmarshal([]byte(payload), &env) != nil {
			continue // tanınmayan satır akışı düşürmez
		}
		if env.ID == nil && env.Method != "" {
			t.pushNotif(env.Method)
			continue
		}
		if wantID != nil && env.ID != nil && *env.ID == *wantID {
			return &env, nil
		}
	}
	if wantID == nil {
		return nil, nil
	}
	return nil, fmt.Errorf("mcp akışı yanıt zarfı taşımadı (id=%d)", *wantID)
}

// pushNotif — bloklamadan gönderir; dolu kanalda bildirim düşer
// (TTL emniyet ağı — dosya başlığındaki gerekçe).
func (t *httpTransport) pushNotif(method string) {
	select {
	case t.notif <- method:
	default:
	}
}

func firstLineTrim(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
