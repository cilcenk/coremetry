package devops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
	t.Run("tam isabette kısmi notu YOK", func(t *testing.T) {
		ok := cc
		ok.Outcome = CodeOK
		ok.Reason = "depo adı sunucudan düzeltildi: core → core-service"
		if s := ok.LogSummary(); strings.Contains(s, "kısmi") {
			t.Fatalf("tertemiz isabet kısmi gösterildi: %q", s)
		}
	})
	t.Run("kısmi isabette kayıp notu EKLİ", func(t *testing.T) {
		part := cc
		part.Outcome = CodePartial
		part.Reason = "ağaçta eşleşen dosya yok: Other.java"
		s := part.LogSummary()
		if !strings.Contains(s, want) {
			t.Fatalf("kısmi isabette özet düştü: %q", s)
		}
		if !strings.Contains(s, "(kısmi: ağaçta eşleşen dosya yok: Other.java)") {
			t.Fatalf("kayıp notu yok: %q", s)
		}
	})
}

// TestFormatCodeMissNote — v0.9.1243 REGRESYON PİNİ.
//
// Semptom: "Kodu da incele" işaretliyken kod GELMEDİĞİNDE ai_calls'ın
// maskeli kopyası hiçbir iz taşımıyordu; satır, kodun hiç istenmediği
// bir çağrıdan ayırt edilemiyordu ve /ai analizi ıska yarısını sessizce
// eksik sayıyordu.
func TestFormatCodeMissNote(t *testing.T) {
	long := strings.Repeat("ş", 200) // çok baytlı: rune tavanı bayt sayamaz
	cases := []struct {
		name  string
		class CodeOutcome
		reas  string
		want  string
	}{
		{"sınıf varsa sınıf", CodeTreeMiss, "ağaçta eşleşen dosya yok: A.java", "\n\n[kod alınamadı: tree-miss]"},
		{"sınıf varsa sınıf (deadline)", CodeDeadline, "", "\n\n[kod alınamadı: deadline]"},
		{"sınıf yoksa gerekçe", "", "bağlam taşması — kod bloğu prompt'a sığmadı",
			"\n\n[kod alınamadı: bağlam taşması — kod bloğu prompt'a sığmadı]"},
		{"ikisi de yoksa sessiz kalınmaz", "", "  ", "\n\n[kod alınamadı: sebep bilinmiyor]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatCodeMissNote(tc.class, tc.reas); got != tc.want {
				t.Fatalf("got=%q want=%q", got, tc.want)
			}
		})
	}
	t.Run("uzun gerekçe rune tavanında kesilir", func(t *testing.T) {
		got := FormatCodeMissNote("", long)
		if !utf8.ValidString(got) {
			t.Fatal("kesme UTF-8 dizisini böldü")
		}
		// Tavan + "…" + sarmalayıcı; 200 karakterlik gerekçe AYNEN geçemez.
		if strings.Contains(got, long) {
			t.Fatalf("gerekçe kesilmedi: %q", got)
		}
		if n := utf8.RuneCountInString(got); n > codeNoteRuneCap+30 {
			t.Fatalf("işaret %d rune — tavan tutmuyor", n)
		}
		if !strings.HasSuffix(got, "…]") {
			t.Fatalf("kesme işareti yok: %q", got)
		}
	})
	t.Run("dolu bağlam ıska işareti üretmez", func(t *testing.T) {
		full := CodeContext{Repo: "r", Windows: []CodeWindow{{Path: "/a.java", FromLine: 1, ToLine: 2}}}
		if s := full.LogMissSummary(); s != "" {
			t.Fatalf("kod geldiği hâlde ıska işareti: %q", s)
		}
	})
	t.Run("boş bağlam sınıfını yazar", func(t *testing.T) {
		empty := CodeContext{Reason: "servis için depo çözülemedi", Outcome: CodeRepoUnresolved}
		if s := empty.LogMissSummary(); s != "\n\n[kod alınamadı: repo-unresolved]" {
			t.Fatalf("got=%q", s)
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
	// seen — görülen istek YOLLARI (v0.9.1240). Proje adının gerçekten
	// URL'e girdiğini ölçmek için: hint'i okuyup isteğe koymayan bir
	// uygulama, yalnız Reason'a bakan bir testten geçerdi.
	seen []string
	// slowItemAfter / itemDelay — N'inci dosya isteğinden İTİBAREN
	// uyu (v0.9.1237 süre tavanı testi). Uyku ctx-duyarlı: aksi hâlde
	// httptest.Server.Close() askıdaki handler'ı beklerdi.
	slowItemAfter int
	itemDelay     time.Duration
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

// sawPathContaining — bu parçayı taşıyan bir istek geldi mi?
func (f *fakeTFS) sawPathContaining(frag string) bool {
	for _, p := range f.seen {
		if strings.Contains(p, frag) {
			return true
		}
	}
	return false
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
		f.seen = append(f.seen, p)
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
			if f.itemDelay > 0 && f.hits["item"] >= f.slowItemAfter {
				select {
				case <-time.After(f.itemDelay):
				case <-r.Context().Done():
					return
				}
			}
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
	cc := svc.FetchCode(context.Background(), "core-service", ProjectHint{}, frames)

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
	_ = svc.FetchCode(context.Background(), "core-service", ProjectHint{}, frames)
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
			cc := svc.FetchCode(context.Background(), tt.repo, ProjectHint{}, tt.frames)
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
	cc := svc.FetchCode(context.Background(), "repo", ProjectHint{}, frames)
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
	cc := svc.FetchCode(context.Background(), "core-service", ProjectHint{},
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
	cc := svc.FetchCode(context.Background(), "core-service", ProjectHint{},
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

	cc := svc.FetchCode(context.Background(), "cashmanagement-cashflow", ProjectHint{}, frames)

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

// --- v0.9.1240: katalog pini + proje türetimi --------------------

// TestFetchCodePinnedServiceDerivesProject — SAHADAKİ VAKA.
//
// Operatör depoyu pinliyor (konvansiyon tutmuyor) ama Ayarlar'da Project
// boş. v0.9.1183 türetimi (bsa- → BSA) pinli yolda HİÇ koşmadığı için
// kod bağlamı "Project boş ve servis adı bilinen bir önekle başlamıyor"
// çıkmazına düşüyordu — üstelik servis adı TAM DA bsa- ile başlıyordu.
// Pin, kod bağlamını açmak yerine kapatıyordu.
//
// Zincirin tamamı burada: ResolveRepo (pin depoyu, önek projeyi verir) →
// FetchCode (türetilen proje isteğe girer) → v0.9.1236 kaçış kapısı
// (pinin harf yazımı sunucudan düzeltilir) → pencere. Kaçış kapısı pinli
// yolda zaten kuruluydu ama proje çıkmazı ondan ÖNCE dönüyordu, yani
// pratikte erişilemezdi.
func TestFetchCodePinnedServiceDerivesProject(t *testing.T) {
	f := newFakeTFS(t)
	const canonical = "CashManagement.CashFlow"
	f.repos = []string{canonical, "identity"}
	const path = "/src/main/java/com/example/cash/CashFlowService.java"
	f.tree = []string{path}
	f.files[path] = javaFile("com.example.cash", "CashFlowService", 200, 88)

	cfg := f.settings()
	cfg.Project = "" // operatör doldurmadı
	svc := New()
	svc.Configure(cfg)

	// Pin: doğru depo, elle yazıldığı için YANLIŞ harf yazımında.
	res := ResolveRepo("bsa-cashmanagement-cashflow-prod", "CashManagement.cashflow", ResolveConfig{})
	if res.Source != RepoSourcePin || res.Project.Value != "BSA" {
		t.Fatalf("çözüm=%+v — pin depoyu, önek projeyi vermeliydi", res)
	}

	frames := stackparse.ParseJava(
		"\tat com.example.cash.CashFlowService.post(CashFlowService.java:88)\n")
	cc := svc.FetchCode(context.Background(), res.Repo, res.Project, frames)

	if cc.Empty() {
		t.Fatalf("kod gelmedi: %s", cc.Reason)
	}
	if strings.Contains(cc.Reason, "Project boş") {
		t.Fatalf("proje çıkmazı hâlâ tetikleniyor: %q", cc.Reason)
	}
	// Türetilen proje İSTEĞE girmeli: hint'i okuyup URL'de kullanmayan
	// bir uygulama yalnız Reason'a bakan bir testten geçerdi.
	if !f.sawPathContaining("/BSA/_apis/git/repositories") {
		t.Fatalf("türetilen proje istek yoluna girmedi: %v", f.seen)
	}
	if cc.Repo != canonical {
		t.Errorf("Repo=%q, sunucunun kanonik adı (%q) beklenirdi — kaçış kapısı "+
			"pinli yolda da çalışmalı", cc.Repo, canonical)
	}
}

// TestFetchCodePinProjectBeatsPrefix — pinin KENDİ projesi türetimi ezer
// ve isteğe O girer (v0.9.1240).
func TestFetchCodePinProjectBeatsPrefix(t *testing.T) {
	f := newFakeTFS(t)
	const path = "/src/main/java/com/example/a/A.java"
	f.tree = []string{path}
	f.files[path] = javaFile("com.example.a", "A", 40, 12)

	cfg := f.settings()
	cfg.Project = ""
	svc := New()
	svc.Configure(cfg)

	res := ResolveRepo("bsa-core-prod",
		f.srv.URL+"/DefaultCollection/OTHER/_git/core-service", ResolveConfig{})
	if res.Project.Value != "OTHER" {
		t.Fatalf("Project=%+v, pinin projesi (OTHER) beklenirdi", res.Project)
	}

	cc := svc.FetchCode(context.Background(), res.Repo, res.Project,
		stackparse.ParseJava("\tat com.example.a.A.b(A.java:12)\n"))
	if cc.Empty() {
		t.Fatalf("kod gelmedi: %s", cc.Reason)
	}
	if f.sawPathContaining("/BSA/_apis/git/repositories") {
		t.Fatalf("önekten türetilen BSA kullanılmış — pinin projesi kazanmalıydı: %v", f.seen)
	}
	if !f.sawPathContaining("/OTHER/_apis/git/repositories") {
		t.Fatalf("pinin projesi istek yoluna girmedi: %v", f.seen)
	}
}

// TestProjectDeadEndNamesAllThree — proje gerçekten çözülemediğinde
// Reason ÜÇ kaynağın da durumunu söyler (v0.9.1240).
//
// Eski cümle tek bir kaynağı suçluyordu ("servis adı bilinen bir önekle
// başlamıyor") ve pinli yolda bu çoğunlukla YANLIŞTI — önek tutuyor,
// türetim hiç koşmuyordu. Operatör düzeltilecek şeyi arayıp bulamıyordu.
func TestProjectDeadEndNamesAllThree(t *testing.T) {
	f := newFakeTFS(t)
	cfg := f.settings()
	cfg.Project = ""
	svc := New()
	svc.Configure(cfg)

	res := ResolveRepo("standalone-service-prod", "pushconfirm-legacy", ResolveConfig{})
	cc := svc.FetchCode(context.Background(), res.Repo, res.Project,
		stackparse.ParseJava("\tat com.example.a.A.b(A.java:12)\n"))

	if !cc.Empty() {
		t.Fatalf("proje yokken kod gelmemeliydi: %+v", cc.Windows)
	}
	for _, want := range []string{
		"Project boş", // (1) Ayarlar
		"katalog pini yalnız depo adı taşıyor", // (2) pin bileşeni
		"standalone-service-prod",              // (3) önek türetimi: hangi ad
		"bsa-",                                 // (3) hangi öneklerle denendi
	} {
		if !strings.Contains(cc.Reason, want) {
			t.Errorf("Reason=%q, %q içermeliydi", cc.Reason, want)
		}
	}
	// Sunucuya tek istek bile çıkmamalı: proje yoksa URL kurulamaz.
	if len(f.seen) != 0 {
		t.Errorf("proje çıkmazında istek çıktı: %v", f.seen)
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

	cc := svc.FetchCode(context.Background(), "core-service", ProjectHint{}, frames)
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

	_ = svc.FetchCode(context.Background(), "billing-gateway", ProjectHint{}, frames)
	first := f.hits["list"]
	if first != 1 {
		t.Fatalf("ilk çağrıda liste %d kez çekildi, 1 beklenirdi", first)
	}
	_ = svc.FetchCode(context.Background(), "another-missing", ProjectHint{}, frames)
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
	cc := svc.FetchCode(context.Background(), "cashflow", ProjectHint{},
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
	cc := svc.FetchCode(context.Background(), "payments-core", ProjectHint{},
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
	cc := svc.FetchCode(context.Background(), "sup3rsecret-missing", ProjectHint{},
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
	cc := svc.FetchCode(context.Background(), "core-service", ProjectHint{},
		stackparse.ParseJava("\tat com.example.a.A.b(A.java:12)\n"))
	if cc.Branch != "Release" {
		t.Fatalf("Branch=%q, sunucunun kanonik yazımı (Release) beklenirdi — reason=%q",
			cc.Branch, cc.Reason)
	}
	if cc.Empty() {
		t.Fatalf("kod çekilemedi: %s", cc.Reason)
	}
}

// --- döngü disiplini (v0.9.1237) ---
//
// Üç denetim bulgusu tek döngüde birleşti: (1) tavan ADAYLARI değil
// PENCERELERİ saymalı — 3 ıska avı bitiriyor, 4. frame isabet
// edecekken hiç denenmiyordu; (2) birebir tekrar frame'i ikinci kez
// çekilmemeli, aynı dosyanın başka satırı eldeki içerikten kesilmeli;
// (3) toplam bir süre tavanı olmalı ve tavan dolduğunda elde ne varsa
// dönmeli (fail-open). Çekirdek saf: ağ enjekte ediliyor.

// huntFrame — testte frame kurmanın kısa yolu.
func huntFrame(file string, line int) stackparse.Frame {
	return stackparse.Frame{
		Class:  "com.example." + strings.TrimSuffix(file, ".java"),
		Method: "m", File: file, Line: line, IsApp: true,
	}
}

func TestHuntWindowsLoopDiscipline(t *testing.T) {
	// tree — sahte depo ağacı: yalnız bu dosyalar bulunur.
	type want struct {
		windows  int
		fetches  int
		dupes    int
		misses   int
		patience bool
		timedOut bool
		note     []string
	}
	tests := []struct {
		name string
		tree []string
		trg  []stackparse.Frame
		lim  huntLimits
		// cancelOn — kaçıncı fetch'te ctx iptal edilir (0 = hiç).
		cancelOn int
		// cancelFails — o fetch içeriği DÖNMEZ, hata döner.
		cancelFails bool
		want        want
	}{
		{
			// (a) ASIL BULGU: üç ıska avı bitirmez.
			name: "üç ıskadan sonra dördüncü frame isabet eder",
			tree: []string{"D.java"},
			trg: []stackparse.Frame{
				huntFrame("A.java", 10), huntFrame("B.java", 20),
				huntFrame("C.java", 30), huntFrame("D.java", 40),
			},
			want: want{windows: 1, fetches: 1, misses: 3,
				note: []string{"eşleşmeyen: A.java, B.java, C.java"}},
		},
		{
			// (b) Tavan ÇIKTIDA: 3 pencere kesilince durulur.
			name: "pencere tavanı fazlasını denemez",
			tree: []string{"A.java", "B.java", "C.java", "D.java", "E.java"},
			trg: []stackparse.Frame{
				huntFrame("A.java", 10), huntFrame("B.java", 20),
				huntFrame("C.java", 30), huntFrame("D.java", 40),
				huntFrame("E.java", 50),
			},
			want: want{windows: 3, fetches: 3},
		},
		{
			// (c) Iska bütçe harcamaz ama SABIR harcar.
			name: "deneme tavanı patolojik stack'i durdurur",
			tree: nil,
			trg: []stackparse.Frame{
				huntFrame("A.java", 1), huntFrame("B.java", 2),
				huntFrame("C.java", 3), huntFrame("D.java", 4),
				huntFrame("E.java", 5), huntFrame("F.java", 6),
				huntFrame("G.java", 7), huntFrame("H.java", 8),
			},
			// Pencere yok → note boş (o hâli çağıran anlatıyor).
			want: want{windows: 0, misses: 6, patience: true},
		},
		{
			name: "deneme tavanı kısmi sonuçta rapor edilir",
			tree: []string{"C.java", "F.java", "Z.java"},
			trg: []stackparse.Frame{
				huntFrame("A.java", 1), huntFrame("B.java", 2),
				huntFrame("C.java", 3), huntFrame("D.java", 4),
				huntFrame("E.java", 5), huntFrame("F.java", 6),
				huntFrame("Z.java", 7),
			},
			want: want{windows: 2, fetches: 2, misses: 4, patience: true,
				note: []string{"deneme tavanı (6) doldu — 1 frame denenmedi", "eşleşmeyen:"}},
		},
		{
			// (d) Dedup: birebir tekrar hiç denenmez, aynı DOSYANIN
			// başka satırı ikinci istek doğurmaz.
			name: "tekrar frame atlanır, aynı dosya yeniden çekilmez",
			tree: []string{"A.java", "B.java"},
			trg: []stackparse.Frame{
				huntFrame("A.java", 10), huntFrame("A.java", 10),
				huntFrame("A.java", 90), huntFrame("B.java", 20),
			},
			want: want{windows: 3, fetches: 2, dupes: 1},
		},
		{
			// (e) Süre tavanı döngü ORTASINDA: elde olan döner.
			name: "süre tavanı kısmi pencereyle döner",
			tree: []string{"A.java", "B.java", "C.java"},
			trg: []stackparse.Frame{
				huntFrame("A.java", 10), huntFrame("B.java", 20),
				huntFrame("C.java", 30),
			},
			cancelOn: 1,
			want: want{windows: 1, fetches: 1, timedOut: true,
				note: []string{"süre tavanı", "3 adaydan 1 pencere kesildi"}},
		},
		{
			// Süre tavanı fetch'in İÇİNDE dolarsa bu bir ıska DEĞİLDİR:
			// dosya orada, biz bekleyemedik.
			name: "istek sırasında dolan tavan ıska sayılmaz",
			tree: []string{"A.java", "B.java", "C.java"},
			trg: []stackparse.Frame{
				huntFrame("A.java", 10), huntFrame("B.java", 20),
				huntFrame("C.java", 30),
			},
			cancelOn: 2, cancelFails: true,
			want: want{windows: 1, fetches: 2, misses: 0, timedOut: true,
				note: []string{"süre tavanı", "3 adaydan 1 pencere kesildi"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lim := tt.lim
			if lim.windows == 0 {
				lim = huntLimits{windows: codeWindowLimit, lookups: codeLookupLimit, radius: codeWindowRadius}
			}
			inTree := map[string]bool{}
			for _, f := range tt.tree {
				inTree[f] = true
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			fetches := 0
			find := func(f stackparse.Frame) string {
				if inTree[f.File] {
					return "/src/main/java/com/example/" + f.File
				}
				return ""
			}
			fetch := func(c context.Context, p string) (string, error) {
				fetches++
				if tt.cancelOn == fetches {
					cancel()
					if tt.cancelFails {
						return "", context.Canceled
					}
				}
				name := p[strings.LastIndex(p, "/")+1:]
				return javaFile("com.example", strings.TrimSuffix(name, ".java"), 200, 10), nil
			}

			h := huntWindows(ctx, tt.trg, lim, find, fetch)

			if len(h.windows) != tt.want.windows {
				t.Errorf("pencere=%d, istenen %d", len(h.windows), tt.want.windows)
			}
			if fetches != tt.want.fetches {
				t.Errorf("fetch=%d, istenen %d (aynı dosya yeniden mi çekildi?)", fetches, tt.want.fetches)
			}
			if h.dupes != tt.want.dupes {
				t.Errorf("dupes=%d, istenen %d", h.dupes, tt.want.dupes)
			}
			if len(h.misses) != tt.want.misses {
				t.Errorf("ıska=%d, istenen %d (%v)", len(h.misses), tt.want.misses, h.misses)
			}
			if h.patience != tt.want.patience {
				t.Errorf("patience=%v, istenen %v", h.patience, tt.want.patience)
			}
			if h.timedOut != tt.want.timedOut {
				t.Errorf("timedOut=%v, istenen %v", h.timedOut, tt.want.timedOut)
			}
			note := h.note(len(h.windows), len(tt.trg), cutoffLabel(true, codeFetchDeadline))
			if len(tt.want.note) == 0 && note != "" {
				t.Errorf("not boş olmalıydı: %q", note)
			}
			for _, sub := range tt.want.note {
				if !strings.Contains(note, sub) {
					t.Errorf("notta %q yok: %q", sub, note)
				}
			}
			// Kesilen her pencere gerçekten dolu olmalı.
			for _, w := range h.windows {
				if w.Path == "" || w.Content == "" || w.Frame == "" {
					t.Errorf("yarım pencere: %+v", w)
				}
			}
		})
	}
}

// TestDeadlineHitDistinguishesCallerCancel — tavan BİZDE mi doldu?
// Çağıran vazgeçtiğinde "DevOps 25 sn'de yanıt vermedi" demek yanlış
// suçlama olurdu; ayrım Reason'ın dürüstlüğünü taşıyor.
func TestDeadlineHitDistinguishesCallerCancel(t *testing.T) {
	live := context.Background()

	past, cancel := context.WithDeadline(live, time.Now().Add(-time.Second))
	defer cancel()
	if !deadlineHit(live, past) {
		t.Error("kendi tavanımız dolduğunda true beklenirdi")
	}

	gone, cancelGone := context.WithCancel(live)
	cancelGone()
	child, cancelChild := context.WithTimeout(gone, time.Hour)
	defer cancelChild()
	if deadlineHit(gone, child) {
		t.Error("çağıran vazgeçtiğinde DevOps suçlanmamalı")
	}

	ok, cancelOK := context.WithTimeout(live, time.Hour)
	defer cancelOK()
	if deadlineHit(live, ok) {
		t.Error("her şey canlıyken false beklenirdi")
	}
	if !strings.Contains(deadlineReason(codeFetchDeadline), "25s") {
		t.Errorf("Reason süreyi söylemiyor: %q", deadlineReason(codeFetchDeadline))
	}
}

// TestFetchCodeWalksPastMissesToLaterFrame — uçtan uca: ilk üç
// uygulama frame'i ağaçta yok, dördüncü var. v0.9.1237 öncesi
// AppFrames(frames, 3) bu dördüncüyü hiç görmüyor, cevap "ağaçta
// eşleşen dosya yok" oluyordu.
func TestFetchCodeWalksPastMissesToLaterFrame(t *testing.T) {
	f := newFakeTFS(t)
	const path = "/src/main/java/com/example/card/CardRepository.java"
	f.tree = []string{"/README.md", path}
	f.files[path] = javaFile("com.example.card", "CardRepository", 200, 88)

	svc := New()
	svc.Configure(f.settings())
	stack := "" +
		"jakarta.ejb.EJBException: host response error\n" +
		"\tat com.example.card.Wrapper.a(Wrapper.java:11)\n" +
		"\tat com.example.card.Wrapper.b(Filter.java:22)\n" +
		"\tat com.example.card.Wrapper.c(Chain.java:33)\n" +
		"\tat com.example.card.CardRepository.find(CardRepository.java:88)\n"

	cc := svc.FetchCode(context.Background(), "core-service", ProjectHint{}, stackparse.ParseJava(stack))
	if len(cc.Windows) != 1 {
		t.Fatalf("pencere=%d, istenen 1 — ıskalar avı bitirmiş olabilir (reason=%q)",
			len(cc.Windows), cc.Reason)
	}
	if cc.Windows[0].Path != path {
		t.Fatalf("Path=%q, istenen %q", cc.Windows[0].Path, path)
	}
	// Kısmi ıska GÖRÜNÜR: kod geldi diye eksik susturulmaz.
	for _, sub := range []string{"eşleşmeyen:", "Wrapper.java", "Filter.java", "Chain.java"} {
		if !strings.Contains(cc.Reason, sub) {
			t.Errorf("Reason'da %q yok: %q", sub, cc.Reason)
		}
	}
}

// TestFetchCodeDedupsSameFileFrames — uçtan uca: birebir tekrar frame
// ikinci kez ÇEKİLMEZ, aynı dosyanın başka satırı eldeki içerikten
// kesilir. Özyineleme/wrapper kalıbında hem istek hem kod bütçesi
// boşa gidiyordu.
func TestFetchCodeDedupsSameFileFrames(t *testing.T) {
	f := newFakeTFS(t)
	const path = "/src/main/java/com/example/card/Recursive.java"
	f.tree = []string{path}
	f.files[path] = javaFile("com.example.card", "Recursive", 200, 12)

	svc := New()
	svc.Configure(f.settings())
	stack := "" +
		"\tat com.example.card.Recursive.walk(Recursive.java:12)\n" +
		"\tat com.example.card.Recursive.walk(Recursive.java:12)\n" +
		"\tat com.example.card.Recursive.enter(Recursive.java:120)\n"

	cc := svc.FetchCode(context.Background(), "core-service", ProjectHint{}, stackparse.ParseJava(stack))
	if len(cc.Windows) != 2 {
		t.Fatalf("pencere=%d, istenen 2 (tekrar atlanmalı, 120. satır kalmalı): %q",
			len(cc.Windows), cc.Reason)
	}
	if f.hits["item"] != 1 {
		t.Fatalf("dosya %d kez çekildi, 1 beklenirdi — içerik yeniden kullanılmıyor", f.hits["item"])
	}
	if cc.Windows[0].Line == cc.Windows[1].Line {
		t.Fatalf("aynı pencerenin kopyası bütçeyi yemiş: %d / %d",
			cc.Windows[0].Line, cc.Windows[1].Line)
	}
	if cc.Reason != "" {
		t.Fatalf("tam isabette Reason dolu: %q", cc.Reason)
	}
}

// TestFetchCodeTotalDeadlineReturnsPartial — uçtan uca: ikinci dosya
// isteği asılı kalınca TOPLAM tavan devreye girer ve elde olan pencere
// DÖNER. v0.9.1237 öncesi tek sınır istek başına 20 sn'ydi; ardışık N
// isteğin tavanı yoktu, explain baytsız dakikalarca bekleyebiliyordu.
//
// Tavan testte kısaltılır (Service.codeDeadline); ölçülen şey süre
// değil, tavanın GERÇEKTEN takılması ve kısmi sonucun raporlanması.
func TestFetchCodeTotalDeadlineReturnsPartial(t *testing.T) {
	f := newFakeTFS(t)
	const a = "/src/main/java/com/example/a/A.java"
	const b = "/src/main/java/com/example/a/B.java"
	f.tree = []string{a, b}
	f.files[a] = javaFile("com.example.a", "A", 200, 12)
	f.files[b] = javaFile("com.example.a", "B", 200, 20)
	f.slowItemAfter, f.itemDelay = 2, 5*time.Second // 2. dosya asılır

	svc := New()
	svc.Configure(f.settings())
	svc.codeDeadline = 300 * time.Millisecond

	start := time.Now()
	cc := svc.FetchCode(context.Background(), "core-service", ProjectHint{}, stackparse.ParseJava(
		"\tat com.example.a.A.x(A.java:12)\n"+
			"\tat com.example.a.B.y(B.java:20)\n"))
	el := time.Since(start)

	if len(cc.Windows) != 1 {
		t.Fatalf("pencere=%d, istenen 1 (kısmi sonuç dönmeliydi): %q", len(cc.Windows), cc.Reason)
	}
	if !strings.Contains(cc.Reason, "süre tavanı") || !strings.Contains(cc.Reason, "1 pencere kesildi") {
		t.Errorf("kesinti Reason'da yok: %q", cc.Reason)
	}
	if el > 3*time.Second {
		t.Errorf("tavan tutmadı: %s beklendi ~300ms", el)
	}
}

// TestFetchCodeDeadlineBlamesNobodyWhenCallerCancels — çağıran
// vazgeçtiyse (tarayıcı kapandı, üst ctx düştü) Reason "DevOps yanıt
// vermedi" ya da "süre tavanı" DEMEZ; yanlış suçlama operatörü
// olmayan bir sunucu arızasının peşine düşürürdü. İki hâl de gerekli:
// hiç pencere gelmeyen dal Reason'ı, kısmi dal ise notu üretiyor.
func TestFetchCodeDeadlineBlamesNobodyWhenCallerCancels(t *testing.T) {
	const a = "/src/main/java/com/example/a/A.java"
	const b = "/src/main/java/com/example/a/B.java"

	run := func(t *testing.T, slowAfter int, stack string) CodeContext {
		t.Helper()
		f := newFakeTFS(t)
		f.tree = []string{a, b}
		f.files[a] = javaFile("com.example.a", "A", 200, 12)
		f.files[b] = javaFile("com.example.a", "B", 200, 20)
		f.slowItemAfter, f.itemDelay = slowAfter, 5*time.Second

		svc := New()
		svc.Configure(f.settings())
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(150 * time.Millisecond); cancel() }()
		defer cancel()
		return svc.FetchCode(ctx, "core-service", ProjectHint{}, stackparse.ParseJava(stack))
	}

	t.Run("hiç pencere yokken", func(t *testing.T) {
		cc := run(t, 1, "\tat com.example.a.A.x(A.java:12)\n")
		if strings.Contains(cc.Reason, "yanıt vermedi") || strings.Contains(cc.Reason, "süre tavanı") {
			t.Fatalf("çağıran vazgeçtiğinde DevOps suçlanmış: %q", cc.Reason)
		}
		if !strings.Contains(cc.Reason, "iptal") {
			t.Fatalf("iptal sebebi yazılmamış: %q", cc.Reason)
		}
	})

	t.Run("kısmi pencere gelmişken", func(t *testing.T) {
		cc := run(t, 2, "\tat com.example.a.A.x(A.java:12)\n"+
			"\tat com.example.a.B.y(B.java:20)\n")
		if len(cc.Windows) != 1 {
			t.Fatalf("pencere=%d, istenen 1 (kısmi sonuç): %q", len(cc.Windows), cc.Reason)
		}
		if strings.Contains(cc.Reason, "süre tavanı") {
			t.Fatalf("iptal 'süre tavanı' diye raporlanmış: %q", cc.Reason)
		}
		if !strings.Contains(cc.Reason, "istek iptal edildi") {
			t.Fatalf("kesinti notu yok: %q", cc.Reason)
		}
	})
}
