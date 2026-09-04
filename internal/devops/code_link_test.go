package devops

import (
	"strings"
	"testing"
)

// code_link_test.go — v0.10.353: arama tavanı ayarı + DevOps dosya+satır linki.

func TestSearchLimitAndClamp(t *testing.T) {
	for in, want := range map[int]int{0: DefaultCodeSearchLimit, -3: DefaultCodeSearchLimit, 4: 4, 99: MaxCodeSearchLimit} {
		if got := (Settings{CodeSearchLimit: in}).searchLimit(); got != want {
			t.Errorf("searchLimit(%d)=%d want %d", in, got, want)
		}
	}
	for in, want := range map[int]int{0: 0, -1: 0, 5: 5, 99: MaxCodeSearchLimit} {
		if got := ClampCodeSearchLimit(in); got != want {
			t.Errorf("clamp(%d)=%d want %d", in, got, want)
		}
	}
	if DefaultCodeSearchLimit <= 2 {
		t.Fatal("operatör tavanın yükselmesini istedi: varsayılan eski 2'nin üstünde olmalı")
	}
}

func TestFileURL(t *testing.T) {
	cfg := Settings{BaseURL: "https://devops.example/tfs", Collection: "Coll", Project: "Proj"}
	u := FileURL(cfg, "", "my-repo", "refs/heads/release", "/src/A.java", 102)
	for _, w := range []string{"https://devops.example/tfs/Coll/Proj/_git/my-repo?", "path=%2Fsrc%2FA.java", "version=GBrelease", "line=102", "lineEnd=102", "lineStartColumn=1", "lineEndColumn=1", "_a=contents"} {
		if !strings.Contains(u, w) {
			t.Fatalf("%q yok: %s", w, u)
		}
	}
	// proje geçersizse cfg.Project; verilen proje öncelikli; satırsız; öneksiz yol
	if u2 := FileURL(cfg, "Other", "r", "", "x.java", 0); !strings.Contains(u2, "/Other/_git/r?") || strings.Contains(u2, "line=") || !strings.Contains(u2, "path=%2Fx.java") || strings.Contains(u2, "version=") {
		t.Fatalf("proje/satırsız: %s", u2)
	}
	if FileURL(cfg, "", "", "b", "x", 1) != "" || FileURL(Settings{}, "", "r", "b", "x", 1) != "" || FileURL(cfg, "", "r", "b", "", 1) != "" {
		t.Fatal("depo/base/yol boşsa link yok")
	}
}

func TestStampWindowLinks(t *testing.T) {
	cfg := Settings{BaseURL: "https://devops.example", Project: "Proj"}
	ws := []CodeWindow{
		{Path: "/src/A.java", Line: 10}, // zincir deposu
		{Path: "framework-core:/lib/B.java", Line: 5, Repo: "framework-core", Project: "PLATFORM", Branch: "main"}, // arama isabeti
	}
	stampWindowLinks(cfg, "svc-repo", "release", ws)
	if ws[0].Repo != "svc-repo" || ws[0].Branch != "release" || ws[0].Project != "Proj" || !strings.Contains(ws[0].WebURL, "/Proj/_git/svc-repo?") || !strings.Contains(ws[0].WebURL, "line=10") {
		t.Fatalf("zincir penceresi: %+v", ws[0])
	}
	if !strings.Contains(ws[1].WebURL, "/PLATFORM/_git/framework-core?") || !strings.Contains(ws[1].WebURL, "path=%2Flib%2FB.java") || strings.Contains(ws[1].WebURL, "framework-core%3A") || ws[1].Path != "framework-core:/lib/B.java" {
		t.Fatalf("arama penceresi: depo öneki URL'den düşer, Path'te kalır: %+v", ws[1])
	}
}
