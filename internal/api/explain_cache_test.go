package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/ai/provider"
)

// explain_cache_test.go — v0.10.83, operatör ürün kararı: "explain
// cevabını kaydet, tekrar tekrar LLM'e sorgu atma; Redis 1 saat TTL."
//
// Dört sözleşme ve dördü de dürüstlük taşıyor:
//  1. anahtar PROMPT'UN TAMAMINDAN (v0.5.187 — tüm girdiler hash'e),
//  2. isabet ETİKETLENİR (cached/cachedAtMs) ve LLM HİÇ çağrılmaz,
//  3. "Yeniden sor" (?refresh=1) önbelleği ATLAR,
//  4. kurtarılmış-düşünce cevabı SAKLANMAZ.

// memCache — Get/Set çalışan minimal cache.Cache. Paketteki fakeCache'i
// GÖMÜYOR: arayüzün geri kalanını o karşılıyor ve arayüz büyüdüğünde bu
// test kendiliğinden ayak uydurur; yalnız Get/Set gölgeleniyor.
type memCache struct {
	fakeCache
	mu sync.Mutex
	m  map[string][]byte
}

func newMemCache() *memCache { return &memCache{m: map[string][]byte{}} }
func (c *memCache) Get(_ context.Context, k string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	return v, ok, nil
}
func (c *memCache) Set(_ context.Context, k string, v []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[k] = v
	return nil
}

func TestExplainCacheKeyHashesEveryInput(t *testing.T) {
	base := explainCacheKey("sys", "user", "")
	for name, k := range map[string]string{
		"system değişti":  explainCacheKey("sys2", "user", ""),
		"user değişti":    explainCacheKey("sys", "user2", ""),
		"kod bloğu girdi": explainCacheKey("sys", "user", "blok"),
		// Ayraç kayması: ("ab","c") ≠ ("a","bc") — \x00 ayraç bunun için.
		"sınır kayması": explainCacheKey("sy", "suser", ""),
	} {
		if k == base {
			t.Errorf("%s ama anahtar AYNI kaldı — iki farklı prompt aynı cevabı paylaşır", name)
		}
	}
	if explainCacheKey("sys", "user", "") != base {
		t.Error("anahtar kararsız — aynı girdi iki farklı satır üretir")
	}
}

// TestCacheHitSkipsTheLLM — ÇEKİRDEK VAAD + İSABET ETİKETİ + XID DEVRİ.
func TestCacheHitSkipsTheLLM(t *testing.T) {
	s := &Server{cache: newMemCache()}
	key := explainCacheKey("sys", "user", "")
	llmCalls := 0
	run := func(func(string)) (string, error) { llmCalls++; return "kök neden: havuz doldu", nil }

	// 1. çağrı: ıska → LLM koşar, cevap saklanır.
	w1 := httptest.NewRecorder()
	r1 := httptest.NewRequest(http.MethodPost, "/api/copilot/explain-trace/t1", nil)
	s.deliverExplain(w1, r1, "xid-ilk", nil, run, "", key)
	if llmCalls != 1 {
		t.Fatalf("ilk çağrı LLM'e gitmedi (llmCalls=%d)", llmCalls)
	}

	// 2. çağrı: isabet → LLM KOŞMAZ, etiket biner, xid SAKLANAN olur.
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodPost, "/api/copilot/explain-trace/t1", nil)
	s.deliverExplain(w2, r2, "xid-ikinci", nil, run, "", key)
	if llmCalls != 1 {
		t.Fatalf("isabette LLM yeniden çağrıldı (llmCalls=%d) — özelliğin tüm amacı buydu", llmCalls)
	}
	var body map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &body); err != nil {
		t.Fatalf("gövde çözülemedi: %v", err)
	}
	if body["cached"] != true {
		t.Error("isabet ETİKETSİZ — operatör bayat cevabı taze sanır")
	}
	if _, ok := body["cachedAtMs"].(float64); !ok {
		t.Error("cachedAtMs yok — arayüz yaş gösteremez")
	}
	if body["exchangeId"] != "xid-ilk" {
		t.Errorf("exchangeId=%v; SAKLANAN (xid-ilk) olmalı — yoksa 👍/👎 hiçliğe düşer", body["exchangeId"])
	}
	if !strings.Contains(w2.Body.String(), "havuz doldu") {
		t.Error("saklanan metin kayboldu")
	}
}

// TestRefreshBypassesTheCache — "Yeniden sor" taze istiyor.
func TestRefreshBypassesTheCache(t *testing.T) {
	s := &Server{cache: newMemCache()}
	key := explainCacheKey("sys", "user", "")
	llmCalls := 0
	run := func(func(string)) (string, error) { llmCalls++; return "cevap", nil }

	r1 := httptest.NewRequest(http.MethodPost, "/x", nil)
	s.deliverExplain(httptest.NewRecorder(), r1, "x1", nil, run, "", key)
	r2 := httptest.NewRequest(http.MethodPost, "/x?refresh=1", nil)
	s.deliverExplain(httptest.NewRecorder(), r2, "x2", nil, run, "", key)
	if llmCalls != 2 {
		t.Fatalf("refresh=1 önbelleğe takıldı (llmCalls=%d) — 'Yeniden sor' işlevsiz", llmCalls)
	}
	// Taze cevap ÜZERİNE yazıldı: sonraki isabet yeni xid'i taşımalı.
	r3 := httptest.NewRequest(http.MethodPost, "/x", nil)
	w3 := httptest.NewRecorder()
	s.deliverExplain(w3, r3, "x3", nil, run, "", key)
	var body map[string]any
	_ = json.Unmarshal(w3.Body.Bytes(), &body)
	if body["exchangeId"] != "x2" {
		t.Errorf("refresh sonrası isabet eski çağrıya bağlı (%v); x2 olmalı", body["exchangeId"])
	}
}

// TestSalvagedThinkingIsNotCached — çalışma notu bir saat çoğaltılmaz.
func TestSalvagedThinkingIsNotCached(t *testing.T) {
	s := &Server{cache: newMemCache()}
	key := explainCacheKey("sys", "user", "")
	llmCalls := 0
	run := func(func(string)) (string, error) {
		llmCalls++
		return provider.MarkSalvagedThinking("belki havuz?"), nil
	}
	r1 := httptest.NewRequest(http.MethodPost, "/x", nil)
	s.deliverExplain(httptest.NewRecorder(), r1, "x1", nil, run, "", key)
	r2 := httptest.NewRequest(http.MethodPost, "/x", nil)
	s.deliverExplain(httptest.NewRecorder(), r2, "x2", nil, run, "", key)
	if llmCalls != 2 {
		t.Fatalf("kurtarılmış düşünce SAKLANMIŞ (llmCalls=%d) — işaretin kapattığı "+
			"kusur önbellekten geri gelir", llmCalls)
	}
}

// TestEmptyKeyMeansNoCaching — anahtarsız çağrı bugünkü davranış.
func TestEmptyKeyMeansNoCaching(t *testing.T) {
	mc := newMemCache()
	s := &Server{cache: mc}
	run := func(func(string)) (string, error) { return "cevap", nil }
	r := httptest.NewRequest(http.MethodPost, "/x", nil)
	s.deliverExplain(httptest.NewRecorder(), r, "x1", nil, run, "", "")
	if len(mc.m) != 0 {
		t.Errorf("anahtarsız çağrı önbelleğe yazdı: %d satır", len(mc.m))
	}
}
