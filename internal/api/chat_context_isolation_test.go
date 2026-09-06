package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
)

// v0.10.487 (Astra bulgusu #1) — sohbet bağlamı KONUŞMALAR ARASI SIZMAZ:
// yeni konuşmanın ilk turu köprüyü ("_") okumaz; id'li konuşma köprüyü
// devralınca siler; id'li konuşma köprüye yazmaz. Anahtar sözleşmesi:
// yazan — id'siz tur (köprü, 10 dk) ve id'li tur (kendi anahtarı, 24 s);
// okuyan — aynı kullanıcı; başka konuşma yalnız köprü üzerinden ve yalnız
// id'siz takip turlarında.

type ctxMemCache struct {
	fakeCache
	mu sync.Mutex
	m  map[string][]byte
}

func (c *ctxMemCache) Get(_ context.Context, k string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	return v, ok, nil
}
func (c *ctxMemCache) Set(_ context.Context, k string, v []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[k] = append([]byte(nil), v...)
	return nil
}
func (c *ctxMemCache) Del(_ context.Context, k string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, k)
	return nil
}

func TestChatContextIsolation(t *testing.T) {
	mc := &ctxMemCache{m: map[string][]byte{}}
	s := &Server{cache: mc}
	ctx := auth.ContextWithClaims(context.Background(), &auth.Claims{UserID: "u1"})

	// Konuşma A, ilk tur (id yok): servis çözüldü → köprüye yazılır.
	a1 := s.loadChatContext(ctx, "", true)
	a1.ctx.Service, a1.dirty = "checkout-service", true
	s.flushChatContext(ctx, a1)
	if _, ok := mc.m[chatContextKey("u1", "")]; !ok {
		t.Fatal("id'siz tur köprüye yazmalı")
	}
	// Konuşma A, ikinci tur (id geldi): köprüden devralır, kendi anahtarına yazar, köprüyü siler.
	a2 := s.loadChatContext(ctx, "conv-A", false)
	if a2.ctx.Service != "checkout-service" || !a2.bridged {
		t.Fatalf("köprüden devir: %+v", a2)
	}
	s.flushChatContext(ctx, a2)
	if _, ok := mc.m[chatContextKey("u1", "")]; ok {
		t.Fatal("köprü devralındıktan sonra silinmeli")
	}
	if _, ok := mc.m[chatContextKey("u1", "conv-A")]; !ok {
		t.Fatal("id'li konuşma kendi anahtarına yazmalı")
	}
	// Konuşma B (yeni thread, ilk tur, id yok): A'nın bağlamını GÖRMEZ.
	b1 := s.loadChatContext(ctx, "", true)
	if !b1.ctx.Empty() {
		t.Fatalf("yeni konuşmanın ilk turu eski bağlamı devraldı: %+v", b1.ctx)
	}
	// Konuşma B id'li: A'nın anahtarını okumaz.
	b2 := s.loadChatContext(ctx, "conv-B", false)
	if !b2.ctx.Empty() {
		t.Fatalf("başka konuşmanın anahtarı okunmamalı: %+v", b2.ctx)
	}
	// A id'li ikinci flush köprüye YAZMAZ (köprü boş kalır).
	a3 := s.loadChatContext(ctx, "conv-A", false)
	a3.ctx.Namespace, a3.dirty = "shop", true
	s.flushChatContext(ctx, a3)
	if _, ok := mc.m[chatContextKey("u1", "")]; ok {
		t.Fatal("id'li konuşma köprüye yazmamalı")
	}
	// Köprü yalnız id'siz TAKİP turunda okunur (ilk turda değil).
	c1 := s.loadChatContext(ctx, "", true)
	c1.ctx.Service, c1.dirty = "payment-service", true
	s.flushChatContext(ctx, c1)
	if c2 := s.loadChatContext(ctx, "", false); c2.ctx.Service != "payment-service" {
		t.Fatalf("id'siz takip turu köprüyü okumalı: %+v", c2.ctx)
	}
	if c3 := s.loadChatContext(ctx, "", true); !c3.ctx.Empty() {
		t.Fatalf("ilk tur köprüyü okumamalı: %+v", c3.ctx)
	}
	// Farklı kullanıcı hiçbir şey görmez.
	other := auth.ContextWithClaims(context.Background(), &auth.Claims{UserID: "u2"})
	if o := s.loadChatContext(other, "conv-A", false); !o.ctx.Empty() {
		t.Fatalf("başka kullanıcı: %+v", o.ctx)
	}
}
