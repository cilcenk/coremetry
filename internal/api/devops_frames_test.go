package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/devops"
	"github.com/cilcenk/coremetry/internal/stackparse"
)

// devops_frames_test.go — v0.10.581 (stack frame → VCS derin linki).
//
// Kilitlediği sözleşmeler:
//   - cache anahtarı TÜM girdileri taşır, uzunluğu DEĞİL (v0.5.187)
//   - yapılandırılmamış = 200 + boş liste (hata DEĞİL)
//   - lineIndex ham satır indeksiyle BİREBİR — CRLF, boş satır,
//     "Caused by:" bloğu dahil
//   - kütüphane frame'i LİSTEDE ama linksiz
//   - revisionWarning link varken DAİMA dolu
//   - 64 KB üstü stack 400
//   - yanıtta KOD GÖVDESİ yok (alan seti pinli)

// ─── cache anahtarı (v0.5.187 sınıfı) ────────────────────────────

// AYNI UZUNLUKTA farklı stack'ler farklı anahtara düşmeli. Tarihsel
// bug tam buydu: uzunluğa çöken bir özet, iki farklı girdiyi aynı
// cache satırına servis ediyordu.
func TestDevopsFramesKeyDistinctSameLengthStacks(t *testing.T) {
	a := devopsFramesKey("svc", "repo", "cfg", "at com.a.A.x(A.java:1)")
	b := devopsFramesKey("svc", "repo", "cfg", "at com.b.B.y(B.java:1)")
	if len(a) == 0 || a == b {
		t.Fatalf("v0.5.187 sınıfı: aynı uzunlukta iki stack aynı anahtara düştü (%q)", a)
	}
}

// Anahtar KARARLI: aynı girdi iki çağrıda aynı dizeyi vermeli.
func TestDevopsFramesKeyStable(t *testing.T) {
	in := []string{"odeme", "core", "cfg1", "at com.a.A.x(A.java:1)"}
	if devopsFramesKey(in[0], in[1], in[2], in[3]) != devopsFramesKey(in[0], in[1], in[2], in[3]) {
		t.Fatal("anahtar kararsız")
	}
}

// Permütasyon AYNILIK DEĞİL AYRIŞMA üretmeli: burada girdiler bir
// KÜME değil, sıralı alanlar. service ile repo yer değiştirdiğinde
// aynı anahtara düşmek, iki farklı servisin cevabını karıştırırdı.
func TestDevopsFramesKeyFieldsAreNotInterchangeable(t *testing.T) {
	a := devopsFramesKey("alfa", "beta", "cfg", "st")
	b := devopsFramesKey("beta", "alfa", "cfg", "st")
	if a == b {
		t.Fatalf("service ve repo yer değiştirince anahtar aynı kaldı: %q", a)
	}
}

// Her alan anahtarı DEĞİŞTİRMELİ — birini unutmak, ayar değişince
// bayat cevap servis etmek demek.
func TestDevopsFramesKeyEveryInputMatters(t *testing.T) {
	base := devopsFramesKey("svc", "repo", "cfg", "st")
	cases := map[string]string{
		"service": devopsFramesKey("svc2", "repo", "cfg", "st"),
		"repo":    devopsFramesKey("svc", "repo2", "cfg", "st"),
		"cfg":     devopsFramesKey("svc", "repo", "cfg2", "st"),
		"stack":   devopsFramesKey("svc", "repo", "cfg", "st2"),
	}
	for field, got := range cases {
		if got == base {
			t.Errorf("%s değişti ama anahtar aynı kaldı", field)
		}
	}
}

// Ayar parmak izi: BranchOrder anahtarı değiştirir (branşın kendisi
// bir ÇIKTI, girdi olarak taşınamıyor — onu belirleyen ayar taşınır).
// Dilim sınırları da ayrışmalı: {"a","b"} ile {"ab"} aynı özete
// düşerse iki farklı konvansiyon tek cache satırını paylaşırdı.
func TestDevopsSettingsDigest(t *testing.T) {
	base := devops.Snapshot{
		BaseURL: "https://devops.local/tfs", Collection: "DefaultCollection",
		Project: "SHOP", RepoPrefixes: []string{"shop-"},
		BranchOrder: []string{"release", "master"}, AppPrefixes: []string{"com.banka."},
	}
	d := devopsSettingsDigest(base)

	alt := base
	alt.BranchOrder = []string{"master", "release"}
	if devopsSettingsDigest(alt) == d {
		t.Error("BranchOrder sırası değişti, özet aynı kaldı")
	}
	alt = base
	alt.RepoPrefixes = []string{"bs", "a-"}
	if devopsSettingsDigest(alt) == d {
		t.Error("dilim sınırı kaydı, özet aynı kaldı — parça ayracı çalışmıyor")
	}
	alt = base
	alt.AppPrefixes = []string{"com.baska."}
	if devopsSettingsDigest(alt) == d {
		t.Error("AppPrefixes değişti, özet aynı kaldı")
	}
	alt = base
	alt.Project = "OTHER"
	if devopsSettingsDigest(alt) == d {
		t.Error("Project değişti, özet aynı kaldı")
	}
	if devopsSettingsDigest(base) != d {
		t.Error("özet kararsız")
	}
}

// ─── lineIndex ↔ ham satır ───────────────────────────────────────

// "Caused by:" bloğu + boş satır + CRLF + "... N more" kuyruğu. Ham
// satır indeksi tam tutmalı: frontend süslemeyi bununla yapacak ve
// bir satır kayması yanlış satırı vurgulardı.
func TestParseStackFramesLineIndex(t *testing.T) {
	lines := []string{
		"java.lang.RuntimeException: patladı",         // 0
		"\tat com.example.a.Alpha.run(Alpha.java:10)", // 1
		"", // 2
		"Caused by: java.lang.IllegalStateException: iç",                   // 3
		"\tat com.example.b.Beta.call(Beta.java:20)",                       // 4
		"\tat java.base/java.util.Optional.orElseThrow(Optional.java:403)", // 5
		"\t... 12 more", // 6
	}
	stack := strings.Join(lines, "\r\n")

	frames, idx := parseStackFrames(stack)
	if len(frames) != 3 || len(idx) != 3 {
		t.Fatalf("frames=%d idx=%d, istenen 3/3", len(frames), len(idx))
	}
	want := []int{1, 4, 5}
	for i, w := range want {
		if idx[i] != w {
			t.Errorf("frame %d (%s) lineIndex=%d, istenen %d", i, frames[i].Class, idx[i], w)
		}
		// Kapının kendisi: verilen indeksteki HAM satır gerçekten o
		// frame'i taşıyor mu? Sabit bir dizi beklemek, ayrıştırıcı
		// değişince sessizce yalan söylerdi.
		raw := strings.Split(stack, "\n")[idx[i]]
		if !strings.Contains(raw, frames[i].Class) {
			t.Errorf("lineIndex %d yanlış satırı gösteriyor: %q", idx[i], raw)
		}
	}
	// Segment damgası TAM METİN çağrısından gelmeli (satır satır
	// koşan çağrı "Caused by:" sayacını göremez).
	if frames[0].Segment != 0 || frames[1].Segment != 1 || frames[2].Segment != 1 {
		t.Errorf("segment damgaları=%d/%d/%d, istenen 0/1/1",
			frames[0].Segment, frames[1].Segment, frames[2].Segment)
	}
}

// Frame'siz metin: boş dilimler, panik yok.
func TestParseStackFramesNoFrames(t *testing.T) {
	frames, idx := parseStackFrames("yalnızca bir mesaj\nikinci satır")
	if len(frames) != 0 || len(idx) != 0 {
		t.Fatalf("frames=%d idx=%d, istenen 0/0", len(frames), len(idx))
	}
}

// ─── yanıt gövdesi ───────────────────────────────────────────────

const twoFrameStack = "java.lang.RuntimeException: patladı\n" +
	"\tat com.example.card.CardService.charge(CardService.java:246)\n" +
	"\tat java.util.Optional.orElseThrow(Optional.java:403)\n"

func linkedResolve(frames []stackparse.Frame) devops.FrameLinks {
	out := devops.FrameLinks{
		Configured: true, Repo: "core-service", Project: "SHOP",
		Branch: "release", RepoSource: devops.RepoSourceConvention,
		Links: make([]devops.FrameLink, len(frames)),
	}
	for i, f := range frames {
		if f.IsApp {
			out.Links[i] = devops.FrameLink{App: true, Tier: 0,
				URL: "https://devops.local/tfs/SHOP/_git/core-service?line=246"}
			continue
		}
		out.Links[i] = devops.FrameLink{App: false, Tier: 1, Reason: devops.FrameReasonLibrary}
	}
	return out
}

// KÜTÜPHANE FRAME'İ LİSTEDE. Frontend onu soluk çizecek; listeden
// düşürmek, operatörün stack'inde bir satırın kaybolması demekti.
func TestStackFramesPayloadKeepsLibraryFrames(t *testing.T) {
	got := stackFramesPayload(twoFrameStack, linkedResolve)
	if len(got.Frames) != 2 {
		t.Fatalf("frames=%d, istenen 2 (kütüphane frame'i de listede)", len(got.Frames))
	}
	lib := got.Frames[1]
	if lib.Class != "java.util.Optional" {
		t.Fatalf("ikinci frame=%q", lib.Class)
	}
	if lib.URL != "" {
		t.Errorf("kütüphane frame'ine link verildi: %q", lib.URL)
	}
	if lib.IsApp || lib.Reason != devops.FrameReasonLibrary {
		t.Errorf("kütüphane frame'i=%+v", lib)
	}
	app := got.Frames[0]
	if app.URL == "" || app.Reason != "" || !app.IsApp || app.Tier != 0 {
		t.Errorf("uygulama frame'i=%+v", app)
	}
	if app.Line != 246 || app.Method != "charge" || app.File != "CardService.java" {
		t.Errorf("künye eksik: %+v", app)
	}
}

// REVİZYON UYARISI KURAL, İSTİSNA DEĞİL: link üretilen HER yanıtta
// dolu ve branşı ADIYLA söylüyor. Sessiz kalmak, kaymış bir satır
// numarasını kanıt diye göstermek olurdu.
func TestStackFramesPayloadRevisionWarningAlwaysPresentWithLink(t *testing.T) {
	got := stackFramesPayload(twoFrameStack, linkedResolve)
	if got.RevisionWarning == "" {
		t.Fatal("link var ama revisionWarning boş")
	}
	if !strings.Contains(got.RevisionWarning, "release") {
		t.Errorf("uyarı branşı söylemiyor: %q", got.RevisionWarning)
	}
}

// Link YOKSA uyarı da yok — uyarılacak bir şey yok.
func TestStackFramesPayloadNoWarningWithoutLink(t *testing.T) {
	got := stackFramesPayload(twoFrameStack, func(frames []stackparse.Frame) devops.FrameLinks {
		return devops.FrameLinks{
			Configured: true, Repo: "core-service", Branch: "release",
			Links: make([]devops.FrameLink, len(frames)),
		}
	})
	if got.RevisionWarning != "" {
		t.Fatalf("linksiz yanıtta uyarı: %q", got.RevisionWarning)
	}
	if len(got.Frames) != 2 {
		t.Fatalf("frames=%d, istenen 2", len(got.Frames))
	}
}

// Branş adı okunamadıysa (deponun varsayılanı kullanıldı) uyarı yine
// dolu ve ne olduğunu SÖYLÜYOR — "" yazmak yerine.
func TestRevisionWarningText(t *testing.T) {
	if w := revisionWarningText("master"); !strings.Contains(w, "master") {
		t.Errorf("branş adı uyarıda yok: %q", w)
	}
	w := revisionWarningText("  ")
	if w == "" || strings.Contains(w, "  branş") {
		t.Errorf("boş branşta uyarı=%q", w)
	}
	if !strings.Contains(w, "varsayılan") {
		t.Errorf("boş branşta ne kullanıldığı söylenmiyor: %q", w)
	}
}

// YAPILANDIRILMAMIŞ: frames BOŞ ve geri kalan alanlar boş. Frame
// künyesini yine göndermek, frontend'i "link gelecek mi" belirsizliğine
// sokardı.
func TestStackFramesPayloadUnconfiguredIsEmpty(t *testing.T) {
	got := stackFramesPayload(twoFrameStack, func([]stackparse.Frame) devops.FrameLinks {
		return devops.FrameLinks{}
	})
	if got.Configured {
		t.Fatal("Configured=true")
	}
	if got.Frames == nil || len(got.Frames) != 0 {
		t.Fatalf("frames=%v, istenen boş DİZİ (null değil)", got.Frames)
	}
	if got.Repo != "" || got.Branch != "" || got.RevisionWarning != "" {
		t.Fatalf("yapılandırılmamışken alanlar doldu: %+v", got)
	}
}

// Hizasız çözüm (sözleşme ihlali) sessiz bir yalan üretmemeli: eksik
// kalan frame linksiz ve kendi IsApp'iyle döner, listeden DÜŞMEZ.
func TestStackFramesPayloadShortLinkSliceIsSafe(t *testing.T) {
	got := stackFramesPayload(twoFrameStack, func([]stackparse.Frame) devops.FrameLinks {
		return devops.FrameLinks{
			Configured: true,
			Links:      []devops.FrameLink{{App: true, Tier: 0, URL: "https://x/1"}},
		}
	})
	if len(got.Frames) != 2 {
		t.Fatalf("frames=%d, istenen 2 — hizasızlıkta frame düşürüldü", len(got.Frames))
	}
	if got.Frames[1].URL != "" || got.Frames[1].Reason != "" {
		t.Fatalf("hizasız frame uydurma veri aldı: %+v", got.Frames[1])
	}
	if got.Frames[1].IsApp {
		t.Errorf("hizasız frame'in IsApp'i kendi künyesinden gelmeli: %+v", got.Frames[1])
	}
}

// ─── ALAN SETİ PİNİ: yanıtta KOD GÖVDESİ YOK ─────────────────────

// Bu uç yalnız künye + URL döndürür. copilot_code.go'nun codePayload'ı
// da Content'i bilinçle kopyalamıyor ("kaynak modele gider, tarayıcıya
// değil"). Yeni bir alan eklemek isteyen bu testi güncellemek zorunda
// kalsın — snippet sessizce sızmasın.
func TestStackFramesResponseFieldSetPinned(t *testing.T) {
	got := stackFramesPayload(twoFrameStack, linkedResolve)
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantTop := []string{"branch", "configured", "frames", "project", "repo", "repoSource", "revisionWarning"}
	if keys := jsonKeys(top); !equalStrings(keys, wantTop) {
		t.Fatalf("üst alan seti=%v, istenen %v", keys, wantTop)
	}
	var frames []map[string]json.RawMessage
	if err := json.Unmarshal(top["frames"], &frames); err != nil {
		t.Fatalf("frames unmarshal: %v", err)
	}
	wantFrame := []string{"class", "file", "isApp", "line", "lineIndex", "method", "reason", "tier", "url"}
	for i, f := range frames {
		if keys := jsonKeys(f); !equalStrings(keys, wantFrame) {
			t.Fatalf("frame %d alan seti=%v, istenen %v", i, keys, wantFrame)
		}
	}
	// Karşı kanıt: kod gövdesi taşıyabilecek adlar HİÇBİR yerde
	// geçmemeli (alan seti pini bunu zaten kapatıyor; bu satır
	// gövdenin kendisinde de kaçak olmadığını söyler).
	low := strings.ToLower(string(b))
	for _, banned := range []string{"\"content\"", "\"snippet\"", "\"windows\"", "\"code\"", "\"lines\""} {
		if strings.Contains(low, banned) {
			t.Fatalf("yanıtta kod gövdesi alanı: %s", banned)
		}
	}
}

// jsonKeys — serileşmiş nesnenin alan adları, sıralı. equalStrings
// devops_handlers_test.go'da zaten var; paket-içi tek yazım.
func jsonKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ─── handler ─────────────────────────────────────────────────────

func postFrames(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/devops/stack-frames", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.postDevopsStackFrames(w, req)
	return w
}

// DevOps yapılandırılmamışsa 200 + boş liste. HATA DEĞİL: operatör
// kuralı "eşleme tanımlı değilse frame'ler düz metin kalsın".
func TestPostDevopsStackFramesUnconfiguredIs200(t *testing.T) {
	w := postFrames(t, &Server{}, `{"service":"odeme","stack":"\tat com.a.A.x(A.java:1)"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("kod=%d, istenen 200 — yapılandırılmamış bir bağlantı hata değil", w.Code)
	}
	var got devopsStackFramesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("gövde: %v (%s)", err, w.Body.String())
	}
	if got.Configured {
		t.Error("configured=true, oysa s.devops nil")
	}
	if len(got.Frames) != 0 {
		t.Errorf("frames=%d, istenen 0", len(got.Frames))
	}
	if !strings.Contains(w.Body.String(), `"frames":[]`) {
		t.Errorf("frames null olarak serileşti (frontend .map()'liyor): %s", w.Body.String())
	}
}

// 64 KB üstü stack 400. Gövde tavanı ayrı bir kapı; ikisi de kapalı.
func TestPostDevopsStackFramesRejectsOversizeStack(t *testing.T) {
	big := strings.Repeat("a", devopsStackMaxBytes+1)
	w := postFrames(t, &Server{}, `{"service":"odeme","stack":"`+big+`"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("kod=%d, istenen 400", w.Code)
	}
	// Tam tavan GEÇMELİ (aksi hâlde sınır bir eksik yazılmış olurdu).
	// Bu boyutta frame yok, yani ağa çıkılmaz.
	ok := strings.Repeat("a", devopsStackMaxBytes)
	if w := postFrames(t, &Server{}, `{"service":"odeme","stack":"`+ok+`"}`); w.Code != http.StatusOK {
		t.Fatalf("tam tavanda kod=%d, istenen 200", w.Code)
	}
}

// Gövde tavanı: JSON kaçışlarına yer bırakır ama sonsuz değil.
func TestPostDevopsStackFramesRejectsOversizeBody(t *testing.T) {
	huge := strings.Repeat("a", devopsStackMaxBody+1024)
	w := postFrames(t, &Server{}, `{"service":"odeme","stack":"`+huge+`"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("kod=%d, istenen 400", w.Code)
	}
}

func TestPostDevopsStackFramesRejectsEmptyStack(t *testing.T) {
	for _, body := range []string{
		`{"service":"odeme","stack":""}`,
		`{"service":"odeme","stack":"   \n  "}`,
		`{"service":"odeme"}`,
	} {
		if w := postFrames(t, &Server{}, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s → kod=%d, istenen 400", body, w.Code)
		}
	}
}

func TestPostDevopsStackFramesRejectsBadJSON(t *testing.T) {
	if w := postFrames(t, &Server{}, `{"service":`); w.Code != http.StatusBadRequest {
		t.Fatalf("kod=%d, istenen 400", w.Code)
	}
}

// KAYIT KAPISI (skill S1): kayıt satırı unutulursa route 404 DEĞİL,
// SPA catch-all yüzünden 200 + boş ekran döner ve hiçbir gate bunu
// görmez. Defter üzerinden kayıt olduğu için burada ölçülüyor.
func TestDevopsFramesRouteRegistered(t *testing.T) {
	mux := (&Server{}).buildMux()
	req := httptest.NewRequest(http.MethodPost, "/api/devops/stack-frames", strings.NewReader("{}"))
	h, pattern := mux.Handler(req)
	if h == nil || pattern != "POST /api/devops/stack-frames" {
		t.Fatalf("rota kayıtlı değil (pattern=%q) — SPA catch-all'a düşer, 200 + boş ekran", pattern)
	}
	// GET aynı yola düşmemeli: metot öneki olmayan bir kayıt Go 1.22
	// mux'ta kazara tüm metotları açardı.
	get := httptest.NewRequest(http.MethodGet, "/api/devops/stack-frames", nil)
	if _, p := mux.Handler(get); p == "POST /api/devops/stack-frames" {
		t.Error("GET de POST handler'ına düştü — metot öneki kaybolmuş")
	}
}

// v0.10.581 — AJANLAR ARASI SÖZLEŞME: lineIndex ↔ gönderilen baytlar.
//
// Frontend `formatStack(raw)` gönderiyor (CRLF→LF + sondaki boşluk kırpma)
// ve AYNI dizeyi çiziyor; backend onu `strings.Split(stack, "\n")` ile
// bölüyor. Araya bir `TrimSpace` ya da boş-satır atlama girerse indeksler
// kayar ve YANLIŞ SATIR linklenir — tip hatası vermez, test olmadan
// hiçbir kapı görmez. Baştaki boş satır bu kaymanın en keskin vakası.
func TestFrameLineIndexCountsLeadingBlankLines(t *testing.T) {
	stack := "\n" + // 0 — boş satır: bir TrimSpace bunu yutar ve her şey kayar
		"java.lang.IllegalStateException: boom\n" + // 1
		"\tat com.example.app.Handler.handle(Handler.java:42)\n" + // 2
		"\tat java.base/java.lang.Thread.run(Thread.java:840)" // 3

	_, idx := parseStackFrames(stack)
	if len(idx) < 2 {
		t.Fatalf("iki frame bekleniyordu, geldi %d: %v", len(idx), idx)
	}
	if idx[0] != 2 {
		t.Errorf("uygulama frame'i 2. satırda: geldi %d (baştaki boş satır yutulmuş olabilir)", idx[0])
	}
	if idx[1] != 3 {
		t.Errorf("kütüphane frame'i 3. satırda: geldi %d", idx[1])
	}
}
