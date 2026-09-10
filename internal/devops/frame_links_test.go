package devops

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/stackparse"
)

// frame_links_test.go — v0.10.581 (stack frame → VCS derin linki).
//
// Kilitlediği sözleşmeler:
//   - sayaç karışmazlığı (FetchCode'a girilmez; resolve_dryrun'ın
//     v0.9.1242 kararının aynısı, karşı kanıtıyla birlikte)
//   - sınıflandırma: kütüphane / satırsız / aday, AppPrefixes ezmesi
//   - hizalama: Links her zaman frames uzunluğunda
//   - süre tavanı TEK kaynaktan (ekranda yazan sayı = uygulanan)
//   - tavan cümlesindeki sayı ile FrameLinkCandidateLimit'in tutması

func frame(class, method, file string, line int) stackparse.Frame {
	return stackparse.Frame{
		Class: class, Method: method, File: file, Line: line,
		IsApp: stackparse.IsAppClass(class),
	}
}

// TAVAN CÜMLESİ ↔ TAVAN SABİTİ. FrameReasonOverLimit const olduğu için
// sayıyı strconv ile üretemiyor; sayı elde yazılı. Tavanı değiştirip
// cümleyi unutmak, operatöre yanlış bir sayı gösterirdi.
func TestFrameReasonOverLimitMatchesLimit(t *testing.T) {
	want := strconv.Itoa(FrameLinkCandidateLimit)
	if !strings.Contains(FrameReasonOverLimit, " "+want+" ") {
		t.Fatalf("FrameReasonOverLimit=%q içinde tavan (%s) geçmiyor — sabit değişti, cümle kalmış",
			FrameReasonOverLimit, want)
	}
}

func TestClassifyFrames(t *testing.T) {
	prefixes := []string{"com.banka."}
	cases := []struct {
		name     string
		f        stackparse.Frame
		wantApp  bool
		wantTier int
		wantWhy  string
	}{
		{"uygulama öneki tutuyor", frame("com.banka.odeme.Kart", "cek", "Kart.java", 42),
			true, 0, FrameReasonOverLimit},
		{"uygulama ama önek dışı", frame("com.baska.Servis", "calis", "Servis.java", 7),
			true, 1, FrameReasonOverLimit},
		{"JDK frame'i", frame("java.util.Optional", "orElseThrow", "Optional.java", 403),
			false, 1, FrameReasonLibrary},
		{"Spring frame'i", frame("org.springframework.web.Filter", "doFilter", "Filter.java", 12),
			false, 1, FrameReasonLibrary},
		// Operatörün açık öneki sabit çerçeve listesini EZER
		// (RankFrames sözleşmesi, v0.10.112). Buradaki sınıf hem
		// frameworkPrefixes'e hem AppPrefixes'e uyuyor.
		{"önek çerçeveyi ezer", frame("com.banka.org.apache.Yama", "uygula", "Yama.java", 3),
			true, 0, FrameReasonOverLimit},
		{"satır numarası yok", frame("com.banka.odeme.Kart", "cek", "Kart.java", 0),
			true, 0, FrameReasonNoLine},
		{"kaynak bilinmiyor", frame("com.banka.odeme.Kart", "cek", "", 0),
			true, 0, FrameReasonNoLine},
		// Kütüphane + satırsız: gerekçe KÜTÜPHANE olmalı. Tersi doğru
		// ama yanıltıcı — satırı olsa da link üretmezdik.
		{"kütüphane hem de satırsız", frame("java.lang.Thread", "run", "", 0),
			false, 1, FrameReasonLibrary},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyFrames([]stackparse.Frame{tc.f}, prefixes)
			if len(got) != 1 {
				t.Fatalf("uzunluk=%d, istenen 1", len(got))
			}
			if got[0].App != tc.wantApp || got[0].Tier != tc.wantTier || got[0].Reason != tc.wantWhy {
				t.Fatalf("got %+v, istenen App=%v Tier=%d Reason=%q",
					got[0], tc.wantApp, tc.wantTier, tc.wantWhy)
			}
			if got[0].URL != "" {
				t.Errorf("ağ öncesi URL üretildi: %q", got[0].URL)
			}
		})
	}
}

// failEligible YALNIZ aday olabilecek frame'lere yazar: kütüphane ya
// da satırsız bir frame'in çözülememe sebebi zincir DEĞİL, ve o
// gerekçeyi ezmek operatörü olmayan bir arızanın peşine düşürürdü.
func TestFailEligibleKeepsStructuralReasons(t *testing.T) {
	links := []FrameLink{
		{Reason: FrameReasonLibrary},
		{Reason: FrameReasonNoLine},
		{Reason: FrameReasonOverLimit},
	}
	failEligible(links, "depo ağacı boş döndü")
	if links[0].Reason != FrameReasonLibrary || links[1].Reason != FrameReasonNoLine {
		t.Fatalf("yapısal gerekçeler ezildi: %+v", links)
	}
	if links[2].Reason != "depo ağacı boş döndü" {
		t.Fatalf("aday gerekçesi=%q, istenen zincir hatası", links[2].Reason)
	}
}

// Boş gerekçe ekrana boş bir hücre olarak düşmemeli.
func TestFailEligibleEmptyReasonFallsBack(t *testing.T) {
	links := []FrameLink{{Reason: FrameReasonOverLimit}}
	failEligible(links, "   ")
	if links[0].Reason == "" || links[0].Reason == FrameReasonOverLimit {
		t.Fatalf("gerekçe=%q, istenen dolu bir yedek cümle", links[0].Reason)
	}
}

// Uzun ağ hatası tek satırda kalmalı (yığın izinin altında paragraf
// istemiyoruz) ve UTF-8 ortasından kesilmemeli.
func TestFailEligibleCapsRunes(t *testing.T) {
	links := []FrameLink{{Reason: FrameReasonOverLimit}}
	failEligible(links, strings.Repeat("ğ", 400))
	r := []rune(links[0].Reason)
	if len(r) > frameReasonRuneCap+1 { // +1 = "…"
		t.Fatalf("gerekçe %d rune, tavan %d", len(r), frameReasonRuneCap)
	}
	if !strings.HasSuffix(links[0].Reason, "…") {
		t.Fatalf("kesildi ama işaretlenmedi: %q", links[0].Reason)
	}
}

// YAPILANDIRILMAMIŞ = HATA DEĞİL. Configured=false döner, Links yine
// frames ile aynı uzunlukta ve sınıflandırılmış olur.
func TestResolveFrameLinksUnconfigured(t *testing.T) {
	frames := []stackparse.Frame{
		frame("com.banka.odeme.Kart", "cek", "Kart.java", 42),
		frame("java.util.Optional", "orElseThrow", "Optional.java", 403),
	}
	got := New().ResolveFrameLinks(context.Background(), "bsa-odeme-prod", PinRead{}, frames, "")
	if got.Configured {
		t.Fatal("Configured=true, oysa BaseURL boş")
	}
	if len(got.Links) != len(frames) {
		t.Fatalf("Links uzunluğu=%d, frames=%d — hizalama sözleşmesi kırıldı", len(got.Links), len(frames))
	}
	for i, l := range got.Links {
		if l.URL != "" {
			t.Errorf("frame %d: bağlantı yokken URL üretildi: %q", i, l.URL)
		}
	}
	if got.Repo != "" || got.Branch != "" {
		t.Errorf("yapılandırılmamışken repo/branş doldu: %+v", got)
	}
}

// nil Service de aynı dürüst cevaba çıkar (CurrentSettings nil-güvenli).
func TestResolveFrameLinksNilService(t *testing.T) {
	var svc *Service
	got := svc.ResolveFrameLinks(context.Background(), "x",
		PinRead{}, []stackparse.Frame{frame("com.x.Y", "z", "Y.java", 1)}, "")
	if got.Configured || len(got.Links) != 1 {
		t.Fatalf("nil Service: %+v", got)
	}
}

// MUTLU YOL — gerçek zincir (branş + ağaç) + gerçek link üretimi.
func TestResolveFrameLinksHappyPath(t *testing.T) {
	f := newFakeTFS(t)
	f.tree = []string{
		"/src/main/java/com/example/card/CardService.java",
		"/src/main/java/com/example/other/CardService.java",
	}
	svc := New()
	svc.Configure(f.settings())

	frames := []stackparse.Frame{
		frame("com.example.card.CardService", "charge", "CardService.java", 246),
		frame("java.util.Optional", "orElseThrow", "Optional.java", 403),
		frame("com.example.card.CardService", "noline", "CardService.java", 0),
	}
	got := svc.ResolveFrameLinks(context.Background(), "bsa-core-service-prod", PinRead{}, frames, "")

	if !got.Configured || got.Repo != "core-service" || got.Branch != "release" {
		t.Fatalf("zincir çıktısı=%+v, istenen core-service@release", got)
	}
	if got.RepoSource != RepoSourceConvention {
		t.Errorf("RepoSource=%q, istenen %q", got.RepoSource, RepoSourceConvention)
	}
	if len(got.Links) != 3 {
		t.Fatalf("Links=%d, istenen 3", len(got.Links))
	}
	// (1) uygulama frame'i: link var, gerekçe yok, PAKET AFFİNİTESİ
	// doğru dosyayı seçti (iki aynı adlı dosya var).
	u := got.Links[0].URL
	if u == "" || got.Links[0].Reason != "" {
		t.Fatalf("uygulama frame'i çözülemedi: %+v", got.Links[0])
	}
	if !strings.Contains(u, "com%2Fexample%2Fcard%2FCardService.java") {
		t.Errorf("yanlış dosya seçildi: %s", u)
	}
	if !strings.Contains(u, "line=246") || !strings.Contains(u, "version=GBrelease") {
		t.Errorf("satır/branş linke girmedi: %s", u)
	}
	// (2) kütüphane frame'i LİSTEDE ama linksiz.
	if got.Links[1].URL != "" || got.Links[1].Reason != FrameReasonLibrary || got.Links[1].App {
		t.Fatalf("kütüphane frame'i: %+v", got.Links[1])
	}
	// (3) satırsız frame de listede, kendi gerekçesiyle.
	if got.Links[2].URL != "" || got.Links[2].Reason != FrameReasonNoLine {
		t.Fatalf("satırsız frame: %+v", got.Links[2])
	}
}

// Ağaçta eşleşen yol yoksa gerekçe SPESİFİK olmalı — "link yok" değil,
// "depo ağacında eşleşen yol yok".
func TestResolveFrameLinksNoPathInTree(t *testing.T) {
	f := newFakeTFS(t)
	f.tree = []string{"/src/main/java/com/example/other/Other.java"}
	svc := New()
	svc.Configure(f.settings())

	got := svc.ResolveFrameLinks(context.Background(), "bsa-core-service-prod", PinRead{},
		[]stackparse.Frame{frame("com.example.card.CardService", "charge", "CardService.java", 246)}, "")
	if got.Links[0].URL != "" || got.Links[0].Reason != FrameReasonNoPath {
		t.Fatalf("got %+v, istenen %q", got.Links[0], FrameReasonNoPath)
	}
}

// TAVAN — 10'dan fazla aday varsa fazlası DENENMEZ ve bunu SÖYLER.
// "denenmedi" ile "yok" farklı cevaplar.
func TestResolveFrameLinksCandidateLimit(t *testing.T) {
	f := newFakeTFS(t)
	svc := New()
	svc.Configure(f.settings())

	n := FrameLinkCandidateLimit + 3
	frames := make([]stackparse.Frame, 0, n)
	for i := 0; i < n; i++ {
		// Her frame AYRI dosya: frameLinkKey ile birleşmesinler,
		// yoksa tavan hiç dolmaz ve test kör olur.
		cls := "com.example.card.Svc" + strconv.Itoa(i)
		f.tree = append(f.tree, "/src/main/java/com/example/card/Svc"+strconv.Itoa(i)+".java")
		frames = append(frames, frame(cls, "run", "Svc"+strconv.Itoa(i)+".java", 10+i))
	}
	got := svc.ResolveFrameLinks(context.Background(), "bsa-core-service-prod", PinRead{}, frames, "")

	linked, over := 0, 0
	for _, l := range got.Links {
		switch {
		case l.URL != "":
			linked++
		case l.Reason == FrameReasonOverLimit:
			over++
		}
	}
	if linked != FrameLinkCandidateLimit {
		t.Fatalf("çözülen=%d, istenen tam %d", linked, FrameLinkCandidateLimit)
	}
	if over != n-FrameLinkCandidateLimit {
		t.Fatalf("tavan gerekçeli=%d, istenen %d", over, n-FrameLinkCandidateLimit)
	}
}

// Aday YOKSA ağa hiç çıkılmaz: tamamı JDK olan bir stack'te depo
// ağacını çekmek, cevabı değiştirmeyen saniyeler demekti.
func TestResolveFrameLinksSkipsNetworkWithoutCandidates(t *testing.T) {
	f := newFakeTFS(t)
	svc := New()
	svc.Configure(f.settings())

	got := svc.ResolveFrameLinks(context.Background(), "bsa-core-service-prod", PinRead{},
		[]stackparse.Frame{frame("java.util.Optional", "orElseThrow", "Optional.java", 403)}, "")
	if !got.Configured {
		t.Fatal("Configured=false")
	}
	f.mu.Lock()
	seen := len(f.seen)
	f.mu.Unlock()
	if seen != 0 {
		t.Fatalf("aday yokken %d istek çıktı — ağ boşuna dövüldü", seen)
	}
}

// Katalog okunamadıysa fail-CLOSED: yanlış depoya link vermek, link
// vermemekten kötü (pinReadDecision, v0.9.1236).
func TestResolveFrameLinksPinAbortIsFailClosed(t *testing.T) {
	f := newFakeTFS(t)
	f.tree = []string{"/src/main/java/com/example/card/CardService.java"}
	svc := New()
	svc.Configure(f.settings())

	got := svc.ResolveFrameLinks(context.Background(), "bsa-core-service-prod",
		PinRead{Abort: "servis kataloğu okunamadı"},
		[]stackparse.Frame{frame("com.example.card.CardService", "charge", "CardService.java", 246)}, "")
	if got.Links[0].URL != "" {
		t.Fatalf("pin iptalinde link üretildi: %q", got.Links[0].URL)
	}
	if got.Links[0].Reason != "servis kataloğu okunamadı" {
		t.Fatalf("gerekçe=%q", got.Links[0].Reason)
	}
	f.mu.Lock()
	seen := len(f.seen)
	f.mu.Unlock()
	if seen != 0 {
		t.Fatalf("pin iptaline rağmen %d istek çıktı", seen)
	}
}

// SÜRE TAVANI TEK KAYNAKTAN. Ekranda yazan sayı, uygulanan tavan
// olmalı — deadlineReason'ın v0.9.1237 dersi.
func TestFrameLinkDeadline(t *testing.T) {
	if got := New().frameLinkDeadline(); got != frameLinkDeadlineCap {
		t.Fatalf("varsayılan=%s, istenen %s", got, frameLinkDeadlineCap)
	}
	// Operatör kod tavanını KISALTTIYSA bu uç ondan sabırlı olmaz.
	svc := New()
	svc.codeDeadline = 2 * time.Second
	if got := svc.frameLinkDeadline(); got != 2*time.Second {
		t.Fatalf("kısa tavanda=%s, istenen 2s", got)
	}
	// UZATTIYSA yine 10 sn: bu uç bir çekmecenin arkasında.
	svc2 := New()
	svc2.codeDeadline = time.Minute
	if got := svc2.frameLinkDeadline(); got != frameLinkDeadlineCap {
		t.Fatalf("uzun tavanda=%s, istenen %s", got, frameLinkDeadlineCap)
	}
}

// Aynı dosya+satıra düşen frame'ler (özyineleme) TEK çözüme iner ama
// HEPSİ linki alır.
func TestResolveFrameLinksDeduplicatesRecursion(t *testing.T) {
	f := newFakeTFS(t)
	f.tree = []string{"/src/main/java/com/example/card/CardService.java"}
	svc := New()
	svc.Configure(f.settings())

	rec := frame("com.example.card.CardService", "charge", "CardService.java", 246)
	got := svc.ResolveFrameLinks(context.Background(), "bsa-core-service-prod", PinRead{},
		[]stackparse.Frame{rec, rec, rec}, "")
	for i, l := range got.Links {
		if l.URL == "" {
			t.Fatalf("yinelenen frame %d linksiz: %+v", i, l)
		}
	}
	if got.Links[0].URL != got.Links[2].URL {
		t.Fatal("aynı frame iki farklı URL aldı")
	}
}

// SAYAÇ KARIŞMAZLIĞI (v0.9.1242 duruşunun aynısı). Link çözümü
// /ai kod-isabet sayaçlarını KIPIRDATMAMALI; mekanizma "FetchCode'a
// girmemek". Karşı kanıt aynı testte: gerçek FetchCode sayacı
// artırıyor, yoksa sıfır bir şey kanıtlamaz.
func TestResolveFrameLinksDoesNotTouchCodeCounters(t *testing.T) {
	f := newFakeTFS(t)
	f.tree = []string{"/src/main/java/com/example/card/CardService.java"}
	svc := New()
	svc.Configure(f.settings())
	ctx := context.Background()
	frames := []stackparse.Frame{frame("com.example.card.CardService", "charge", "CardService.java", 246)}

	svc.ResolveFrameLinks(ctx, "bsa-core-service-prod", PinRead{}, frames, "")   // mutlu yol
	svc.ResolveFrameLinks(ctx, "legacy-service", PinRead{}, frames, "")          // konvansiyon çıkmazı
	svc.ResolveFrameLinks(ctx, "x", PinRead{Abort: "okunamadı"}, frames, "")     // pin iptali
	New().ResolveFrameLinks(ctx, "bsa-core-service-prod", PinRead{}, frames, "") // yapılandırılmamış

	if st := svc.CodeObservability(); st.Attempts != 0 || st.OK != 0 ||
		st.Partial != 0 || len(st.Misses) != 0 {
		t.Fatalf("link çözümü sayaçlara karıştı: %+v", st)
	}
	svc.FetchCode(ctx, "core-service", ProjectHint{}, nil, nil, nil)
	if st := svc.CodeObservability(); st.Attempts != 1 {
		t.Fatalf("FetchCode sayacı=%d, istenen 1 — sayaç ölüyse üstteki sıfır bir şey kanıtlamaz", st.Attempts)
	}
}

// YAPISAL KAPI: ResolveFrameLinks'in GÖVDESİ FetchCode/RecordCodeOutcome
// çağırmamalı. Sayaç testi davranışı ölçüyor; bu kapı niyeti kilitliyor
// — bir gün FetchCode'a düşen bir "yedek" eklenirse sayaç testi de
// yeşil kalabilirdi (ağ yoksa deneme sayılmaz sanılabilir).
//
// Pencere FONKSİYONA hapsedilir: dosya sonuna kadar aramak komşu
// fonksiyonun kodunu kanıt sayardı.
func TestResolveFrameLinksBodyAvoidsFetchCode(t *testing.T) {
	b, err := os.ReadFile("frame_links.go")
	if err != nil {
		t.Fatalf("kaynak okunamadı: %v", err)
	}
	src := string(b)
	start := strings.Index(src, "func (s *Service) ResolveFrameLinks(")
	if start < 0 {
		t.Fatal("ResolveFrameLinks bulunamadı — kapı kör kaldı")
	}
	body := src[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	for _, banned := range []string{"FetchCode(", "RecordCodeOutcome(", ".Explain("} {
		if strings.Contains(body, banned) {
			t.Fatalf("ResolveFrameLinks gövdesinde %q — sayaç/atıf sözleşmesi kırıldı", banned)
		}
	}
	// Kapının kendisi ölçülür: yasaklı dize gövdede OLSA yakalanır mı?
	if !strings.Contains(body+"FetchCode(", "FetchCode(") {
		t.Fatal("kapı kendi aradığı deseni göremiyor")
	}
}
