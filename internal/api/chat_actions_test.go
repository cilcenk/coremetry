package api

import (
	"os"
	"strings"
	"testing"
)

// v0.10.542 — action bloğu yalnız aynı sayfaya giden, sorgulu, kök-göreli
// linkten; yayın link bloğunun hemen ardından (tool sonucundan, model
// metninden değil).
func TestActionForLink(t *testing.T) {
	l := guidedAnswerLink{Label: "Traces", Href: "/traces?service=api&hasError=1"}
	act, ok := actionForLink(l, "/traces")
	if !ok || act["kind"] != "apply" || act["href"] != l.Href || act["label"] != "Filtreyi bu sayfada uygula" {
		t.Fatalf("aynı sayfa: %v %v", act, ok)
	}
	if _, ok := actionForLink(l, "/logs"); ok {
		t.Fatal("başka sayfa → aksiyon yok (link çipi yeter)")
	}
	if _, ok := actionForLink(l, ""); ok {
		t.Fatal("sayfa bağlamı yok → aksiyon yok")
	}
	for _, bad := range []string{"https://x/traces?a=1", "//x/traces?a=1", "/traces"} {
		if _, ok := actionForLink(guidedAnswerLink{Href: bad}, "/traces"); ok {
			t.Errorf("%q aksiyon üretmemeli", bad)
		}
	}
	if act, _ := actionForLink(guidedAnswerLink{Href: "/service?name=api&op=x"}, "/service"); act["label"] != "Bu sayfada uygula" {
		t.Fatalf("genel etiket: %v", act)
	}
	src, _ := os.ReadFile("copilot_chat.go")
	if strings.Count(string(src), `emit("block", blockSeq.Next(blocks.TypeAction, act))`) != 2 {
		t.Fatal("aksiyon bloğu iki link yayın noktasında da (deep_link + arg köprüsü) yayımlanmalı")
	}
	if strings.Contains(string(src), "actionForLink(guidedAnswerLink{") {
		t.Fatal("aksiyon model metninden kurulmamalı")
	}
}
