package api

// copilot_chat_profile_test.go — v0.10.183: sohbet gövdesindeki context.profile
// bütün alışverişe uygulanır (copilot.WithProfile) — kaynak kapısı.

import (
	"os"
	"strings"
	"testing"
)

func TestChatContextProfileWiring(t *testing.T) {
	b, err := os.ReadFile("copilot_chat.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "Profile string `json:\"profile,omitempty\"`") {
		t.Fatal("ChatRequest.Context.Profile alanı yok")
	}
	if !strings.Contains(src, "ctx = copilot.WithProfile(ctx, p)") {
		t.Fatal("context.profile ctx'e uygulanmıyor (WithProfile)")
	}
	if strings.Index(src, "ctx = copilot.WithProfile(ctx, p)") < strings.Index(src, "copilot.WithMeta(dctx, copilot.CallMeta{") {
		t.Fatal("WithProfile, CallMeta'dan ÖNCE uygulanmış — meta ctx'i ezer")
	}
}
