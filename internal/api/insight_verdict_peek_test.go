package api

// v0.9.1207 (Faz 6.3) — insight kartı HAZIR verdict'i tüketir, ÜRETMEZ.
// Pinler: (a) önbellekte duran rootcause-explain gövdesinden verdict'in
// ham JSON'u çıkar, (b) miss = nil (LLM'e giden hiçbir yol yok — A2
// kararı: kart otomatik LLM ateşlemez), (c) verdict'siz gövde (prose-
// only eski cevap) nil, (d) anahtar hipotez SÜRÜMÜNÜ taşır — bayat
// hipotezin verdict'i asla dönmez.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
)

func peekTestServer() *Server {
	c, _ := cache.NewNoop()
	return &Server{cache: c, l1: newL1Cache(8), stats: newCacheStats()}
}

func TestPeekRCAVerdictFromCache(t *testing.T) {
	s := peekTestServer()
	body := []byte(`{"prose":"özet","verdict":{"verdict":"probable_cause","title":"DB havuzu"},"exchangeId":"x1"}`)
	s.l1.set("rootcause-explain:problem:p1:42", body, time.Minute)

	raw := s.peekRCAVerdict(context.Background(), "problem", "p1", 42)
	if raw == nil || !strings.Contains(string(raw), "probable_cause") {
		t.Fatalf("önbellekteki verdict çıkarılamadı: %s", raw)
	}

	// (d) Farklı hipotez sürümü = farklı anahtar = nil.
	if got := s.peekRCAVerdict(context.Background(), "problem", "p1", 43); got != nil {
		t.Fatalf("bayat sürüm anahtarından verdict döndü: %s", got)
	}
	// (b) Hiç olmayan ankor = nil.
	if got := s.peekRCAVerdict(context.Background(), "problem", "yok", 1); got != nil {
		t.Fatalf("miss nil dönmeli: %s", got)
	}
	// (c) Verdict'siz gövde = nil.
	s.l1.set("rootcause-explain:problem:p2:1", []byte(`{"prose":"yalnız anlatı","exchangeId":"x2"}`), time.Minute)
	if got := s.peekRCAVerdict(context.Background(), "problem", "p2", 1); got != nil {
		t.Fatalf("verdict'siz gövdeden nil beklenirdi: %s", got)
	}
}
