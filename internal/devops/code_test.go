package devops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/stackparse"
)

// code_test.go — v0.9.830.
//
// Sahte TFS httptest ile: gerçek bir müşteri sunucusu/koleksiyonu bu
// depoya girmez. Sunucu, gerçek Azure DevOps Server / TFS uçlarının
// ŞEKLİNİ taklit eder (refs / items?recursionLevel=Full / items
// includeContent).

// --- saf yardımcılar ---

func TestBestPathForFrame(t *testing.T) {
	tree := []string{
		"/README.md",
		"/src/main/java/com/example/card/CardDetailBusiness.java",
		"/legacy/src/com/other/card/CardDetailBusiness.java",
		"/src/main/java/com/example/card/MyCardDetailBusiness.java",
		"/src/test/java/com/example/card/CardDetailBusinessTest.java",
		"/modules/billing/src/main/java/com/example/billing/CardService.java",
		"/generated/a/b/c/d/e/CardService.java",
	}
	tests := []struct {
		name  string
		frame stackparse.Frame
		want  string
	}{
		{
			name:  "paket yolu en çok eşleşen kazanır",
			frame: stackparse.Frame{Class: "com.example.card.CardDetailBusiness", File: "CardDetailBusiness.java", Line: 246},
			want:  "/src/main/java/com/example/card/CardDetailBusiness.java",
		},
		{
			name:  "derin yol da olsa paket eşleşmesi kazanır",
			frame: stackparse.Frame{Class: "com.example.billing.CardService", File: "CardService.java", Line: 10},
			want:  "/modules/billing/src/main/java/com/example/billing/CardService.java",
		},
		{
			name:  "farklı paket → yalnız o pakete uyan aday",
			frame: stackparse.Frame{Class: "com.other.card.CardDetailBusiness", File: "CardDetailBusiness.java", Line: 5},
			want:  "/legacy/src/com/other/card/CardDetailBusiness.java",
		},
		{
			name:  "sonek eşleşmesi TAM dosya adı üstünden — MyX.java yakalanmaz",
			frame: stackparse.Frame{Class: "com.example.card.MyCardDetailBusiness", File: "MyCardDetailBusiness.java", Line: 1},
			want:  "/src/main/java/com/example/card/MyCardDetailBusiness.java",
		},
		{
			name:  "ağaçta yok",
			frame: stackparse.Frame{Class: "com.example.x.Missing", File: "Missing.java", Line: 1},
			want:  "",
		},
		{
			name:  "dosya adı boş → eşleşme yok",
			frame: stackparse.Frame{Class: "com.example.x.Y", Line: 1},
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BestPathForFrame(tree, tt.frame); got != tt.want {
				t.Fatalf("BestPathForFrame=%q, istenen %q", got, tt.want)
			}
		})
	}
}

func TestBestPathForFrameDeterministicTie(t *testing.T) {
	// Paket ipucu YOK (paketsiz sınıf): iki aday da 0 puan alır.
	// Sonuç yine de deterministik olmalı — aynı exception iki tıkta
	// iki farklı dosya göstermemeli.
	tree := []string{"/bbb/Runner.java", "/a/Runner.java", "/aa/Runner.java"}
	f := stackparse.Frame{Class: "Runner", File: "Runner.java", Line: 1}
	first := BestPathForFrame(tree, f)
	for i := 0; i < 20; i++ {
		if got := BestPathForFrame(tree, f); got != first {
			t.Fatalf("deterministik değil: %q vs %q", got, first)
		}
	}
	if first != "/a/Runner.java" {
		t.Fatalf("eşitlikte kısa yol beklenirdi, %q geldi", first)
	}
}

func TestWindowAround(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&sb, "satir%d\n", i)
	}
	content := sb.String()

	tests := []struct {
		name       string
		line       int
		radius     int
		from, to   int
		firstLabel string
	}{
		{name: "ortada", line: 50, radius: 5, from: 45, to: 55, firstLabel: "45| satir45"},
		{name: "dosya başı kırpılır", line: 2, radius: 10, from: 1, to: 12, firstLabel: "1| satir1"},
		{name: "dosya sonu kırpılır", line: 98, radius: 10, from: 88, to: 100, firstLabel: "88| satir88"},
		{name: "satır dosyanın dışında → sınıra kırpılır", line: 500, radius: 3, from: 100, to: 100, firstLabel: "100| satir100"},
		{name: "satır 0 → 1 sayılır", line: 0, radius: 2, from: 1, to: 3, firstLabel: "1| satir1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := WindowAround(content, tt.line, tt.radius)
			if w.FromLine != tt.from || w.ToLine != tt.to {
				t.Fatalf("aralık=%d-%d, istenen %d-%d", w.FromLine, w.ToLine, tt.from, tt.to)
			}
			if first := strings.SplitN(w.Content, "\n", 2)[0]; first != tt.firstLabel {
				t.Fatalf("ilk satır=%q, istenen %q", first, tt.firstLabel)
			}
			if n := strings.Count(w.Content, "\n") + 1; n != tt.to-tt.from+1 {
				t.Fatalf("satır sayısı=%d, istenen %d", n, tt.to-tt.from+1)
			}
		})
	}

	if got := WindowAround("", 5, 10); got.Content != "" {
		t.Error("boş içerik boş pencere vermeli")
	}
	if got := WindowAround("   \n  \n", 1, 10); got.Content != "" {
		t.Error("yalnız boşluk boş pencere vermeli")
	}
	// CRLF: satır sonları normalize edilmeli, \r içerik satırına
	// karışmamalı.
	w := WindowAround("bir\r\niki\r\nüç\r\n", 2, 1)
	if !strings.Contains(w.Content, "2| iki") || strings.Contains(w.Content, "\r") {
		t.Errorf("CRLF normalize edilmedi: %q", w.Content)
	}
}

func TestWindowAroundRuneSafe(t *testing.T) {
	// Çok baytlı karakterler pencerede bozulmamalı (v0.9.414 dersi).
	content := "// açıklama satırı\nif (kart == null) { throw new İstisna(); }\n// üçüncü\n"
	w := WindowAround(content, 2, 1)
	if !strings.Contains(w.Content, "İstisna") || strings.Contains(w.Content, "�") {
		t.Fatalf("çok baytlı içerik bozuldu: %q", w.Content)
	}
}

func TestClampCodeWindows(t *testing.T) {
	mk := func(from, to int) CodeWindow {
		var sb strings.Builder
		for i := from; i <= to; i++ {
			fmt.Fprintf(&sb, "%d| kod\n", i)
		}
		return CodeWindow{FromLine: from, ToLine: to, Path: "/x.java",
			Content: strings.TrimRight(sb.String(), "\n")}
	}

	t.Run("bütçe yetiyorsa dokunmaz", func(t *testing.T) {
		ws, trimmed := ClampCodeWindows([]CodeWindow{mk(1, 3), mk(10, 12)}, 10000)
		if trimmed || len(ws) != 2 {
			t.Fatalf("trimmed=%v len=%d", trimmed, len(ws))
		}
	})

	t.Run("son pencere satır sınırında kısalır", func(t *testing.T) {
		first := mk(1, 3)
		ws, trimmed := ClampCodeWindows([]CodeWindow{first, mk(10, 40)},
			len([]rune(first.Content))+20)
		if !trimmed {
			t.Fatal("trimmed=false — kısalma bildirilmedi")
		}
		if len(ws) != 2 {
			t.Fatalf("pencere sayısı=%d, istenen 2", len(ws))
		}
		// Kesim satır sınırında: hiçbir satır yarım kalmamalı.
		for _, ln := range strings.Split(ws[1].Content, "\n") {
			if ln != "" && !strings.HasSuffix(ln, "| kod") {
				t.Fatalf("yarım satır kesildi: %q", ln)
			}
		}
		// ToLine gerçeği söylemeli: korunan SON satır.
		last := strings.Split(ws[1].Content, "\n")
		wantTo := 0
		fmt.Sscanf(last[len(last)-1], "%d|", &wantTo)
		if ws[1].ToLine != wantTo {
			t.Fatalf("ToLine=%d, korunan son satır %d — aralık yalan söylüyor", ws[1].ToLine, wantTo)
		}
	})

	t.Run("bütçe tek satırı bile almıyorsa pencere düşer", func(t *testing.T) {
		ws, trimmed := ClampCodeWindows([]CodeWindow{mk(1, 3), mk(10, 40)},
			len([]rune(mk(1, 3).Content))+2)
		if !trimmed || len(ws) != 1 {
			t.Fatalf("trimmed=%v len=%d, istenen true/1", trimmed, len(ws))
		}
	})

	t.Run("sıfır bütçe", func(t *testing.T) {
		ws, trimmed := ClampCodeWindows([]CodeWindow{mk(1, 3)}, 0)
		if len(ws) != 0 || !trimmed {
			t.Fatalf("len=%d trimmed=%v", len(ws), trimmed)
		}
	})

	t.Run("pencere yok", func(t *testing.T) {
		ws, trimmed := ClampCodeWindows(nil, 100)
		if len(ws) != 0 || trimmed {
			t.Fatalf("len=%d trimmed=%v", len(ws), trimmed)
		}
	})
}

func TestMaskCodeInPrompt(t *testing.T) {
	cc := CodeContext{
		Repo: "core-service", Branch: "release",
		Windows: []CodeWindow{{
			Path:  "/src/main/java/com/example/card/CardDetailBusiness.java",
			Frame: "com.example.card.CardDetailBusiness.handleHostResponseError(CardDetailBusiness.java:246)",
			Line:  246, FromLine: 216, ToLine: 276,
			Content: "246| if (hostResponse == null) { throw new HostException(SECRET_MARKER); }",
		}},
	}
	block := cc.PromptBlock()
	full := "Exception GRUBU: {...}" + block + "\n\nson söz"

	masked := MaskCodeInPrompt(full, block, cc.LogSummary())

	// PİN: kod satırı maskeli logda GÖRÜNMEZ.
	if strings.Contains(masked, "SECRET_MARKER") || strings.Contains(masked, "hostResponse == null") {
		t.Fatalf("kod gövdesi maskeli loga sızdı:\n%s", masked)
	}
	if strings.Contains(masked, "```java") {
		t.Fatal("kod bloğu çiti maskeli logda kaldı")
	}
	// Özet DURUR: hangi dosyanın gittiği görülebilmeli.
	want := "[kod: core-service/src/main/java/com/example/card/CardDetailBusiness.java:216-276 · 61 satır]"
	if !strings.Contains(masked, want) {
		t.Fatalf("özet yok:\n%s\nistenen %q", masked, want)
	}
	// Prompt'un GERİ KALANI dokunulmadan durmalı.
	if !strings.Contains(masked, "Exception GRUBU") || !strings.Contains(masked, "son söz") {
		t.Fatalf("kod dışı bağlam bozuldu:\n%s", masked)
	}
	// Gerçek prompt değişmedi (saf fonksiyon).
	if !strings.Contains(full, "SECRET_MARKER") {
		t.Fatal("MaskCodeInPrompt girdisini değiştirdi — sağlayıcıya giden prompt bozulur")
	}

	t.Run("blok bulunamazsa prompt aynen döner", func(t *testing.T) {
		if got := MaskCodeInPrompt("abc", "yok", "özet"); got != "abc" {
			t.Fatalf("got=%q", got)
		}
	})
	t.Run("boş bağlam blok/özet üretmez", func(t *testing.T) {
		var empty CodeContext
		if empty.PromptBlock() != "" || empty.LogSummary() != "" {
			t.Fatal("boş CodeContext metin üretti")
		}
	})
}

// --- uçtan uca: sahte TFS ---

// fakeTFS — gerçek TFS/Azure DevOps Server uç ŞEKİLLERİNİ taklit eden
// test sunucusu. Adlar jenerik.
type fakeTFS struct {
	srv      *httptest.Server
	tree     []string
	files    map[string]string
	branches []string
	// repos — sunucudaki KANONİK depo adları (v0.9.1236). Boşsa her ad
	// kabul edilir; mevcut testler bu yüzden dokunulmadan geçiyor.
	// Doluysa eşleşme BAYT BAYT: gerçek Azure DevOps ada göre çözümde
	// harf farkını çoğu zaman tolere eder, ama testin işi toleransa
	// yaslanmak değil kaçış kapısının gerçekten açıldığını görmek.
	repos []string
	// hits — uç başına istek sayısı (cache pini için).
	hits map[string]int
}

// repoSegment — istek yolundaki depo adı. Liste ucunda "".
func repoSegment(path string) string {
	const marker = "/_apis/git/repositories"
	i := strings.Index(path, marker)
	if i < 0 {
		return ""
	}
	rest := strings.TrimPrefix(path[i+len(marker):], "/")
	if j := strings.Index(rest, "/"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// knownRepo — sunucu bu adı tanıyor mu?
func (f *fakeTFS) knownRepo(name string) bool {
	if len(f.repos) == 0 {
		return true
	}
	for _, r := range f.repos {
		if r == name {
			return true
		}
	}
	return false
}

func newFakeTFS(t *testing.T) *fakeTFS {
	t.Helper()
	f := &fakeTFS{
		branches: []string{"refs/heads/master", "refs/heads/release", "refs/heads/feature/x"},
		files:    map[string]string{},
		hits:     map[string]int{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, q := r.URL.Path, r.URL.Query()
		// v0.9.1236 — liste DENEMESİ en tepede sayılır, kimlik ve
		// api-version muhafızlarından ÖNCE. Sayacı aşağıya, başarı
		// dalına koymak testi kör ederdi: "kaçış kapısı hiç açılmadı"
		// ile "açıldı ama sunucu 401 verdi" aynı sıfırı gösterirdi —
		// oysa testin ölçtüğü şey tam olarak İSTEĞİN ÇIKIP ÇIKMADIĞI.
		if strings.HasSuffix(p, "/_apis/git/repositories") {
			f.hits["list"]++
		}
		// PAT sözleşmesi: Basic auth, kullanıcı adı boş, PAT şifrede.
		if u, pw, ok := r.BasicAuth(); !ok || u != "" || pw == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if q.Get("api-version") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// v0.9.1236 — depo LİSTESİ ucu ve bilinmeyen depo 404'ü.
		if seg := repoSegment(p); seg == "" && strings.HasSuffix(p, "/_apis/git/repositories") {
			var out struct {
				Value []struct {
					Name string `json:"name"`
				} `json:"value"`
			}
			for _, r := range f.repos {
				out.Value = append(out.Value, struct {
					Name string `json:"name"`
				}{r})
			}
			_ = json.NewEncoder(w).Encode(out)
			return
		} else if seg != "" && !f.knownRepo(seg) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"TF401019: repository does not exist"}`))
			return
		}
		switch {
		case strings.HasSuffix(p, "/refs"):
			f.hits["refs"]++
			var out struct {
				Value []struct {
					Name string `json:"name"`
				} `json:"value"`
			}
			for _, b := range f.branches {
				out.Value = append(out.Value, struct {
					Name string `json:"name"`
				}{b})
			}
			_ = json.NewEncoder(w).Encode(out)
		case strings.HasSuffix(p, "/items") && q.Get("recursionLevel") == "Full":
			f.hits["tree"]++
			if q.Get("versionDescriptor.version") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			type item struct {
				Path          string `json:"path"`
				GitObjectType string `json:"gitObjectType"`
				IsFolder      bool   `json:"isFolder"`
			}
			out := struct {
				Value []item `json:"value"`
			}{}
			out.Value = append(out.Value, item{Path: "/src", GitObjectType: "tree", IsFolder: true})
			for _, pth := range f.tree {
				out.Value = append(out.Value, item{Path: pth, GitObjectType: "blob"})
			}
			_ = json.NewEncoder(w).Encode(out)
		case strings.HasSuffix(p, "/items"):
			f.hits["item"]++
			body, ok := f.files[q.Get("path")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"path": q.Get("path"), "content": body,
			})
		default:
			// Depo metadata (varsayılan branş) dahil her şey.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "core-service", "defaultBranch": "refs/heads/master",
			})
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTFS) settings() Settings {
	return Settings{
		BaseURL: f.srv.URL, Collection: "DefaultCollection", Project: "Payments",
		PAT: "test-pat", Flavor: FlavorServer,
	}
}

// javaFile — N satırlık jenerik kaynak; `errLine` satırında hata.
func javaFile(pkg, class string, lines, errLine int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "package %s;\n", pkg)
	for i := 2; i <= lines; i++ {
		if i == errLine {
			fmt.Fprintf(&sb, "    if (hostResponse == null) { throw new HostException(\"host response error\"); } // %s\n", class)
			continue
		}
		fmt.Fprintf(&sb, "    // dolgu satırı %d\n", i)
	}
	return sb.String()
}

func TestFetchCodeEndToEnd(t *testing.T) {
	f := newFakeTFS(t)
	const path = "/src/main/java/com/example/card/CardDetailBusiness.java"
	f.tree = []string{
		"/README.md",
		path,
		"/legacy/src/com/other/card/CardDetailBusiness.java",
		"/src/main/java/com/example/card/CardRepository.java",
	}
	f.files[path] = javaFile("com.example.card", "CardDetailBusiness", 400, 246)
	f.files["/src/main/java/com/example/card/CardRepository.java"] =
		javaFile("com.example.card", "CardRepository", 200, 88)

	svc := New()
	svc.Configure(f.settings())

	stack := "" +
		"jakarta.ejb.EJBException: host response error\n" +
		"\tat deployment.APPWEB.war//com.example.card.CardDetailBusiness.handleHostResponseError(CardDetailBusiness.java:246)\n" +
		"\tat org.springframework.web.servlet.DispatcherServlet.doService(DispatcherServlet.java:1010)\n" +
		"\tat deployment.APPWEB.war//com.example.card.CardRepository.find(CardRepository.java:88)\n" +
		"\tat java.base/java.util.Optional.orElseThrow(Optional.java:403)\n"

	frames := stackparse.ParseJava(stack)
	cc := svc.FetchCode(context.Background(), "core-service", "", frames)

	if cc.Reason != "" && cc.Empty() {
		t.Fatalf("kod çekilemedi: %s", cc.Reason)
	}
	// (a) branş: release > master
	if cc.Branch != "release" {
		t.Errorf("Branch=%q, istenen release", cc.Branch)
	}
	// (b) eşleşme: paket yoluna uyan dosya, legacy kopya DEĞİL
	if len(cc.Windows) != 2 {
		t.Fatalf("pencere sayısı=%d, istenen 2 (iki uygulama frame'i): %+v", len(cc.Windows), cc.Windows)
	}
	if cc.Windows[0].Path != path {
		t.Errorf("Path=%q, istenen %q (legacy kopya seçilmiş olabilir)", cc.Windows[0].Path, path)
	}
	// (c) pencere: ±30 satır, hata satırı İÇİNDE
	w := cc.Windows[0]
	if w.FromLine != 216 || w.ToLine != 276 {
		t.Errorf("aralık=%d-%d, istenen 216-276", w.FromLine, w.ToLine)
	}
	if !strings.Contains(w.Content, "246| ") || !strings.Contains(w.Content, "HostException") {
		t.Errorf("hata satırı pencerede yok:\n%s", w.Content)
	}
	// Çerçeve frame'leri hiç denenmemeli.
	for _, win := range cc.Windows {
		if strings.Contains(win.Path, "Dispatcher") || strings.Contains(win.Path, "Optional") {
			t.Errorf("çerçeve frame'i için kod çekilmiş: %s", win.Path)
		}
	}
	// (d) prompt bloğu
	block := cc.PromptBlock()
	if !strings.Contains(block, "KOD BAĞLAMI") || !strings.Contains(block, path) ||
		!strings.Contains(block, "HostException") {
		t.Fatalf("prompt bloğu eksik:\n%s", block)
	}
	// (e) maskeli log pini: kod gövdesi ai_calls kaydına GİRMEZ
	full := "Exception GRUBU: {...}" + block
	masked := MaskCodeInPrompt(full, block, cc.LogSummary())
	if strings.Contains(masked, "HostException") || strings.Contains(masked, "dolgu satırı") {
		t.Fatalf("kod maskeli loga sızdı:\n%s", masked)
	}
	if !strings.Contains(masked, "[kod: core-service"+path) {
		t.Fatalf("maskeli logda kaynak özeti yok:\n%s", masked)
	}
	// (f) bütçe: toplam kod ~4000 rune'u aşmaz
	total := 0
	for _, win := range cc.Windows {
		total += len([]rune(win.Content))
	}
	if total > codeBudgetRunes {
		t.Errorf("kod bütçesi aşıldı: %d > %d", total, codeBudgetRunes)
	}

	// (g) ağaç cache'i: ikinci çağrı yeni listeleme yapmaz
	treeHits := f.hits["tree"]
	_ = svc.FetchCode(context.Background(), "core-service", "", frames)
	if f.hits["tree"] != treeHits {
		t.Errorf("ağaç yeniden listelendi (%d → %d) — 10 dk cache tutmuyor",
			treeHits, f.hits["tree"])
	}
}

func TestFetchCodeFailOpen(t *testing.T) {
	f := newFakeTFS(t)
	f.tree = []string{"/src/main/java/com/example/other/Thing.java"}
	f.files["/src/main/java/com/example/other/Thing.java"] = javaFile("com.example.other", "Thing", 50, 10)

	frames := stackparse.ParseJava(
		"\tat com.example.card.CardDetailBusiness.handle(CardDetailBusiness.java:246)\n")

	tests := []struct {
		name       string
		mutate     func(s *Settings)
		repo       string
		frames     []stackparse.Frame
		wantReason string
	}{
		{
			name: "bağlantı yapılandırılmamış", repo: "core-service", frames: frames,
			mutate: func(s *Settings) { s.BaseURL = "" }, wantReason: "yapılandırılmamış",
		},
		{
			name: "proje boş", repo: "core-service", frames: frames,
			mutate: func(s *Settings) { s.Project = "" }, wantReason: "Project boş",
		},
		{
			name: "depo çözülemedi", repo: "", frames: frames,
			wantReason: "depo çözülemedi",
		},
		{
			name: "uygulama frame'i yok", repo: "core-service",
			frames:     stackparse.ParseJava("\tat java.base/java.util.Optional.orElseThrow(Optional.java:403)\n"),
			wantReason: "uygulama frame'i yok",
		},
		{
			name: "ağaçta eşleşme yok", repo: "core-service", frames: frames,
			wantReason: "eşleşen dosya yok",
		},
		{
			name: "PAT yanlış → sunucu 401", repo: "core-service", frames: frames,
			mutate: func(s *Settings) { s.PAT = "" }, wantReason: "http 401",
		},
		{
			name: "sunucu erişilemiyor", repo: "core-service", frames: frames,
			mutate: func(s *Settings) { s.BaseURL = "http://127.0.0.1:1" }, wantReason: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := f.settings()
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}
			svc := New()
			svc.Configure(cfg)
			cc := svc.FetchCode(context.Background(), tt.repo, "", tt.frames)
			if !cc.Empty() {
				t.Fatalf("kod dönmemeliydi: %+v", cc.Windows)
			}
			if cc.Reason == "" {
				t.Fatal("Reason boş — fail-open yolunda NEDEN söylenmeli")
			}
			if tt.wantReason != "" && !strings.Contains(cc.Reason, tt.wantReason) {
				t.Fatalf("Reason=%q, %q içermeliydi", cc.Reason, tt.wantReason)
			}
		})
	}
}

// TestFetchCodeSanitizesPATInReason — fail-open mesajı sır sızdırmaz
// (v0.9.829 sözleşmesi kod yolunda da geçerli).
func TestFetchCodeSanitizesPATInReason(t *testing.T) {
	svc := New()
	svc.Configure(Settings{
		BaseURL: "https://user:sup3rsecret@127.0.0.1:1/tfs", Collection: "C", Project: "P",
		PAT: "sup3rsecret", Flavor: FlavorServer,
	})
	frames := stackparse.ParseJava("\tat com.example.a.A.b(A.java:1)\n")
	cc := svc.FetchCode(context.Background(), "repo", "", frames)
	if strings.Contains(cc.Reason, "sup3rsecret") {
		t.Fatalf("PAT fail-open mesajına sızdı: %s", cc.Reason)
	}
}

// TestFetchCodeFallsBackToDefaultBranch — konvansiyondaki branşların
// hiçbiri yoksa deponun varsayılan branşı kullanılır.
func TestFetchCodeFallsBackToDefaultBranch(t *testing.T) {
	f := newFakeTFS(t)
	f.branches = []string{"refs/heads/develop", "refs/heads/main"}
	const path = "/src/main/java/com/example/a/A.java"
	f.tree = []string{path}
	f.files[path] = javaFile("com.example.a", "A", 40, 12)

	svc := New()
	svc.Configure(f.settings())
	cc := svc.FetchCode(context.Background(), "core-service", "",
		stackparse.ParseJava("\tat com.example.a.A.b(A.java:12)\n"))
	if cc.Branch != "master" {
		t.Fatalf("Branch=%q, deponun varsayılanı (master) beklenirdi — reason=%q", cc.Branch, cc.Reason)
	}
	if cc.Empty() {
		t.Fatalf("kod çekilemedi: %s", cc.Reason)
	}
}

// TestFetchCodeRespectsBranchOrderSetting — ayardaki sıra uygulanır.
func TestFetchCodeRespectsBranchOrderSetting(t *testing.T) {
	f := newFakeTFS(t)
	f.branches = []string{"refs/heads/master", "refs/heads/release"}
	const path = "/src/main/java/com/example/a/A.java"
	f.tree = []string{path}
	f.files[path] = javaFile("com.example.a", "A", 40, 12)

	cfg := f.settings()
	cfg.BranchOrder = []string{"master", "release"}
	svc := New()
	svc.Configure(cfg)
	cc := svc.FetchCode(context.Background(), "core-service", "",
		stackparse.ParseJava("\tat com.example.a.A.b(A.java:12)\n"))
	if cc.Branch != "master" {
		t.Fatalf("Branch=%q, ayardaki sıra (master önce) uygulanmadı", cc.Branch)
	}
}

// --- v0.9.1236: depo çözümü sunucu listesine karşı ---------------

// TestFetchCodeRecoversRepoNameFromServerList — SAHADAKİ VAKA.
//
// Konvansiyon küçük harf üretir ("cashmanagement-cashflow"), gerçek
// depo "CashManagement.CashFlow" yazımındadır. Eskiden bu opak bir
// "depo bulunamadı" ile biterdi ve tek çare servis başına elle pindi.
func TestFetchCodeRecoversRepoNameFromServerList(t *testing.T) {
	f := newFakeTFS(t)
	const canonical = "CashManagement.CashFlow"
	f.repos = []string{"Payments.Core", canonical, "identity"}
	const path = "/src/main/java/com/example/cash/CashFlowService.java"
	f.tree = []string{path}
	f.files[path] = javaFile("com.example.cash", "CashFlowService", 200, 88)

	svc := New()
	svc.Configure(f.settings())
	frames := stackparse.ParseJava(
		"\tat com.example.cash.CashFlowService.post(CashFlowService.java:88)\n")

	cc := svc.FetchCode(context.Background(), "cashmanagement-cashflow", "", frames)

	if cc.Empty() {
		t.Fatalf("kaçış kapısı açılmadı: %s", cc.Reason)
	}
	if cc.Repo != canonical {
		t.Fatalf("Repo=%q, sunucunun kanonik adı (%q) beklenirdi", cc.Repo, canonical)
	}
	// Düzeltme İZİ: sessiz düzeltme, operatörün yanlış adı hiç
	// öğrenmemesi demek olurdu.
	if !strings.Contains(cc.Reason, "düzeltildi") ||
		!strings.Contains(cc.Reason, "cashmanagement-cashflow") ||
		!strings.Contains(cc.Reason, canonical) {
		t.Fatalf("düzeltme izi Reason'da yok: %q", cc.Reason)
	}
	if f.hits["list"] != 1 {
		t.Fatalf("liste %d kez çekildi, 1 beklenirdi", f.hits["list"])
	}
}

// TestFetchCodeRepoListOnlyOnFailure — MUTLU YOL bir istek bile
// fazladan görmemeli; liste yalnız 404'ten SONRA çekilir.
func TestFetchCodeRepoListOnlyOnFailure(t *testing.T) {
	f := newFakeTFS(t)
	f.repos = []string{"core-service"}
	const path = "/src/main/java/com/example/a/A.java"
	f.tree = []string{path}
	f.files[path] = javaFile("com.example.a", "A", 40, 12)

	svc := New()
	svc.Configure(f.settings())
	frames := stackparse.ParseJava("\tat com.example.a.A.b(A.java:12)\n")

	cc := svc.FetchCode(context.Background(), "core-service", "", frames)
	if cc.Empty() {
		t.Fatalf("mutlu yol kırıldı: %s", cc.Reason)
	}
	if cc.Reason != "" {
		t.Fatalf("mutlu yolda Reason dolu: %q", cc.Reason)
	}
	if f.hits["list"] != 0 {
		t.Fatalf("mutlu yolda depo listesi çekildi (%d kez)", f.hits["list"])
	}
}

// TestFetchCodeRepoListCached — ikinci başarısız çözüm yeni bir
// listeleme yapmaz (10 dk cache, ağaç cache'iyle aynı disiplin).
func TestFetchCodeRepoListCached(t *testing.T) {
	f := newFakeTFS(t)
	f.repos = []string{"Payments.Core"}
	svc := New()
	svc.Configure(f.settings())
	frames := stackparse.ParseJava("\tat com.example.a.A.b(A.java:12)\n")

	_ = svc.FetchCode(context.Background(), "billing-gateway", "", frames)
	first := f.hits["list"]
	if first != 1 {
		t.Fatalf("ilk çağrıda liste %d kez çekildi, 1 beklenirdi", first)
	}
	_ = svc.FetchCode(context.Background(), "another-missing", "", frames)
	if f.hits["list"] != first {
		t.Fatalf("liste yeniden çekildi (%d → %d) — cache tutmuyor", first, f.hits["list"])
	}
}

// TestFetchCodeNoMatchListsNearestNames — eşleşme yoksa bugünkü hata
// KORUNUR, üstüne 2-3 yakın ad eklenir. Tam liste DÖKÜLMEZ.
func TestFetchCodeNoMatchListsNearestNames(t *testing.T) {
	f := newFakeTFS(t)
	f.repos = []string{
		"cashflow-api", "cashflow-worker", "cashflow-batch", "cashflow-ui",
		"payments-core", "identity",
	}
	svc := New()
	svc.Configure(f.settings())
	cc := svc.FetchCode(context.Background(), "cashflow", "",
		stackparse.ParseJava("\tat com.example.a.A.b(A.java:12)\n"))

	if !cc.Empty() {
		t.Fatal("eşleşmemeliydi")
	}
	if !strings.Contains(cc.Reason, "en yakın adlar") {
		t.Fatalf("yakın adlar Reason'da yok: %q", cc.Reason)
	}
	if strings.Contains(cc.Reason, "identity") || strings.Contains(cc.Reason, "payments-core") {
		t.Fatalf("alakasız adlar dökülmüş: %q", cc.Reason)
	}
	if n := strings.Count(cc.Reason, "cashflow-"); n > repoNearMax {
		t.Fatalf("%d aday listelenmiş, tavan %d: %q", n, repoNearMax, cc.Reason)
	}
}

// TestFetchCodeEscapeHatchSkipsOnAuthError — PAT arızasında liste hiç
// denenmez: sebep ad değil kimlik, ve ikinci istek aynı duvara çarpardı.
func TestFetchCodeEscapeHatchSkipsOnAuthError(t *testing.T) {
	f := newFakeTFS(t)
	f.repos = []string{"Payments.Core"}
	cfg := f.settings()
	cfg.PAT = "" // sahte sunucu 401 döner
	svc := New()
	svc.Configure(cfg)
	cc := svc.FetchCode(context.Background(), "payments-core", "",
		stackparse.ParseJava("\tat com.example.a.A.b(A.java:12)\n"))

	if !strings.Contains(cc.Reason, "http 401") {
		t.Fatalf("Reason=%q, asıl teşhis (401) korunmalıydı", cc.Reason)
	}
	if f.hits["list"] != 0 {
		t.Fatalf("kimlik hatasında liste çekildi (%d kez)", f.hits["list"])
	}
}

// TestFetchCodeEscapeHatchSanitizesPAT — kaçış kapısının ürettiği
// mesajlar da sır sızdırmaz (v0.9.829 sözleşmesi bu yolda da geçerli).
func TestFetchCodeEscapeHatchSanitizesPAT(t *testing.T) {
	f := newFakeTFS(t)
	f.repos = []string{"sup3rsecret-named-repo"}
	cfg := f.settings()
	cfg.PAT = "sup3rsecret"
	svc := New()
	svc.Configure(cfg)
	cc := svc.FetchCode(context.Background(), "sup3rsecret-missing", "",
		stackparse.ParseJava("\tat com.example.a.A.b(A.java:12)\n"))
	if strings.Contains(cc.Reason, "sup3rsecret") {
		t.Fatalf("PAT kaçış kapısı mesajına sızdı: %s", cc.Reason)
	}
}

// TestFetchCodePicksCaseDifferentBranch — 'Release' yazımlı depo artık
// varsayılan branşa DÜŞMEZ. Eski davranışta kod pencereleri yanlış
// branştan (Master) kesiliyor, operatöre yanlış satırlar kanıt diye
// gösteriliyordu.
func TestFetchCodePicksCaseDifferentBranch(t *testing.T) {
	f := newFakeTFS(t)
	f.branches = []string{"refs/heads/Master", "refs/heads/Release"}
	const path = "/src/main/java/com/example/a/A.java"
	f.tree = []string{path}
	f.files[path] = javaFile("com.example.a", "A", 40, 12)

	svc := New()
	svc.Configure(f.settings())
	cc := svc.FetchCode(context.Background(), "core-service", "",
		stackparse.ParseJava("\tat com.example.a.A.b(A.java:12)\n"))
	if cc.Branch != "Release" {
		t.Fatalf("Branch=%q, sunucunun kanonik yazımı (Release) beklenirdi — reason=%q",
			cc.Branch, cc.Reason)
	}
	if cc.Empty() {
		t.Fatalf("kod çekilemedi: %s", cc.Reason)
	}
}
