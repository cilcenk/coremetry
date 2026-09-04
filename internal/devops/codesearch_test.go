package devops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/stackparse"
)

// codesearch_test.go — v0.10.74.
//
// Operatör: "repoda bulamazsa tüm organizationı da arayabilir… metod
// stacktrace'ten search çalıştırıp ilgili repoyu bulup kontrol etse daha
// iyi olur." Teşhis doğru: depo SERVİS adından çözülüyor ama hatanın
// atıldığı sınıf çoğu zaman BAŞKA bir depoda yaşıyor.
//
// ⚠ AMA ARAMA TEK BAŞINA BELİRSİZ. Operatörün ekranında aynı sınıf YEDİ
// sonuçta birden çıktı: aynı kodun Development/INT/UAT/Prod kopyaları
// (TFVC yolları) ve bir Git deposu. "İlk sonucu al" YANLIŞ dosyayı kanıt
// diye sunardı — ve yanlış kanıt, kanıt yokluğundan kötüdür.

func hit(project, repo, path string) CodeSearchHit {
	return CodeSearchHit{Project: project, Repository: repo, Path: path}
}

func frameFor(class, method string) stackparse.Frame {
	return stackparse.Frame{Class: class, Method: method, File: "X.java", Line: 1}
}

// TestTFVCResultsAreRejected — ÇEKİM YOLU YALNIZ GIT KONUŞUYOR.
//
// `$/Proje/…` bir Git yolu DEĞİL; denemek 404 üretir ve operatöre
// "dosya yok" dedirtir.
func TestTFVCResultsAreRejected(t *testing.T) {
	// ⚠ Depo adı BOŞ olan hâl, `Repository == ""` muhafızıyla da elenir;
	// yani bu girdi tek başına YOL kontrolünü ölçmez. Mutasyon denemesinde
	// TFVC elemesini söktüm ve test YEŞİL kaldı — örtüşen muhafız
	// mutasyonu gölgeledi ([[feedback-overlapping-guards-shadow-mutations]]).
	//
	// Bu yüzden asıl vaka DEPO ADI DOLU bir TFVC sonucu: bazı uç
	// sürümleri proje adını repository alanına koyuyor ve o zaman tek
	// savunma yol önekidir.
	hits := []CodeSearchHit{
		hit("P", "", "$/P/Development/src/EmailSender.java"),
		hit("P", "P", "$/P/Prod/src/EmailSender.java"), // depo adı DOLU, yol TFVC
	}
	if got, ok := PickSearchHit(hits, "", frameFor("com.x.EmailSender", "send"), nil); ok {
		t.Errorf("TFVC sonucu kabul edildi (%+v) — Git API'sinde 404 üretir ve "+
			"operatör 'dosya yok' diye okur", got)
	}
}

// TestConventionRepoWins — OPERATÖRÜN SÖZLEŞMESİ TAHMİNDEN GÜÇLÜ.
func TestConventionRepoWins(t *testing.T) {
	hits := []CodeSearchHit{
		hit("P", "other-repo", "/src/com/x/EmailSender.java"),
		hit("P", "delivery-manager", "/src/com/x/EmailSender.java"),
	}
	got, ok := PickSearchHit(hits, "delivery-manager", frameFor("com.x.EmailSender", "send"), nil)
	if !ok || got.Repository != "delivery-manager" {
		t.Errorf("konvansiyon deposu seçilmedi: %+v", got)
	}
}

// TestPackagePathBreaksTheTie — AYNI ADLI DOSYA, FARKLI PAKET.
func TestPackagePathBreaksTheTie(t *testing.T) {
	hits := []CodeSearchHit{
		hit("P", "repo-a", "/src/com/other/pkg/EmailSender.java"),
		hit("P", "repo-b", "/src/com/x/y/EmailSender.java"),
	}
	got, ok := PickSearchHit(hits, "", frameFor("com.x.y.EmailSender", "send"), nil)
	if !ok || got.Repository != "repo-b" {
		t.Errorf("paket yolu örtüşmesi kullanılmadı: %+v", got)
	}
}

// TestPickIsDeterministic — AYNI HATA AYNI KANIT.
//
// Sıra rastgele olsaydı aynı trace iki kez farklı dosya gösterirdi ve
// operatör hangisinin doğru olduğunu bilemezdi.
func TestPickIsDeterministic(t *testing.T) {
	hits := []CodeSearchHit{
		hit("Z", "r2", "/src/B.java"),
		hit("A", "r1", "/src/A.java"),
		hit("A", "r1", "/src/B.java"),
	}
	first, ok := PickSearchHit(hits, "", frameFor("com.x.B", "m"), nil)
	if !ok {
		t.Fatal("hiçbir aday seçilmedi")
	}
	for i := 0; i < 20; i++ {
		got, _ := PickSearchHit(hits, "", frameFor("com.x.B", "m"), nil)
		if got != first {
			t.Fatalf("%d. koşumda seçim değişti: %+v vs %+v", i, got, first)
		}
	}
}

// TestSearchQueryMirrorsOperatorSearch — SORGU BİÇİMİ.
//
// Operatörün elle yaptığı arama `Sınıf.metot` idi ve en seçici olan bu:
// yalnız sınıf adı her dosyayı getirir, yalnız metot adı gürültü denizi.
func TestSearchQueryForFrame(t *testing.T) {
	for _, tc := range []struct{ class, method, want string }{
		{"com.x.y.EmailSender", "getAttachmentByCMId", "EmailSender.getAttachmentByCMId"},
		{"com.x.Outer$Inner", "run", "Inner.run"},
		{"com.x.Svc", "<init>", "Svc"},      // kurucu: metot adı işe yaramaz
		{"com.x.Svc", "lambda$do$0", "Svc"}, // lambda: aynı gerekçe
		{"", "m", ""},                       // sınıfsız frame
	} {
		if got := SearchQueryForFrame(frameFor(tc.class, tc.method)); got != tc.want {
			t.Errorf("SearchQueryForFrame(%q,%q) = %q; want %q", tc.class, tc.method, got, tc.want)
		}
	}
}

// ── AĞ YARISI: ULAŞILABİLİRLİK VE SIRA ─────────────────────────────────

func searchFrame(class, method, file string, line int) stackparse.Frame {
	return stackparse.Frame{Class: class, Method: method, File: file, Line: line}
}

// TestSearchFindsClassInAnotherRepo — OPERATÖRÜN DURUMU.
//
// Hatanın atıldığı sınıf, trace'in servisinden BAŞKA bir depoda.
// Konvansiyon onu asla bulamıyor; arama buluyor.
func TestSearchFindsClassInAnotherRepo(t *testing.T) {
	missed := []stackparse.Frame{searchFrame("com.x.mail.EmailSender", "getAttachment", "EmailSender.java", 645)}
	search := func(_ context.Context, q string) ([]CodeSearchHit, error) {
		if q != "EmailSender.getAttachment" {
			t.Errorf("sorgu = %q; Sınıf.metot bekleniyordu", q)
		}
		return []CodeSearchHit{
			{Project: "P", Repository: "", Path: "$/P/Prod/src/EmailSender.java"}, // TFVC
			{Project: "P", Repository: "delivery-manager", Path: "/src/com/x/mail/EmailSender.java", Branch: "master"},
		}, nil
	}
	var gotProject, gotRepo, gotBranch string
	fetchIn := func(_ context.Context, project, repo, branch, path string) (string, error) {
		gotProject, gotRepo, gotBranch = project, repo, branch
		return javaFile("com.x.mail", "EmailSender", 700, 645), nil
	}
	out, notes := huntSearchWindows(context.Background(), missed, "other-service", nil, 5, search, fetchIn, codeSearchLimit)

	if len(out) != 1 {
		t.Fatalf("pencere=%d, 1 bekleniyordu", len(out))
	}
	if gotRepo != "delivery-manager" || gotBranch != "master" {
		t.Errorf("çekim yanlış hedefe gitti: repo=%q branch=%q", gotRepo, gotBranch)
	}
	// v0.10.85 — isabetin PROJESİ çekime geçmeli: başka projedeki depo,
	// yürürlükteki projenin URL'iyle 404 olur ve sessizce atlanırdı.
	if gotProject != "P" {
		t.Errorf("çekim isabetin projesini taşımıyor: project=%q", gotProject)
	}
	// Operatör pencerenin BAŞKA depodan geldiğini görmeli, yoksa yolu
	// kendi deposunda arar.
	if !strings.HasPrefix(out[0].Path, "delivery-manager:") {
		t.Errorf("yol depo adını taşımıyor: %q", out[0].Path)
	}
	if len(notes) == 0 || !strings.Contains(notes[0], "BAŞKA depodan") {
		t.Errorf("künye başka-depo durumunu söylemiyor: %v", notes)
	}
}

// TestSearchFailureIsReported — SESSİZ KALMA.
//
// Uzantı kurulu değilse (404) ya da PAT kapsamı yetmiyorsa (401) arama
// düşer. Sessiz kalmak "aradık, bulamadık" demek olurdu — oysa hiç
// arayamadık.
func TestSearchFailureIsReported(t *testing.T) {
	missed := []stackparse.Frame{searchFrame("com.x.A", "m", "A.java", 1)}
	search := func(context.Context, string) ([]CodeSearchHit, error) {
		return nil, errSearchProbe
	}
	_, notes := huntSearchWindows(context.Background(), missed, "", nil, 5, search,
		func(context.Context, string, string, string, string) (string, error) { return "", nil }, codeSearchLimit)
	if len(notes) == 0 || !strings.Contains(notes[0], "kod araması başarısız") {
		t.Errorf("arama hatası künyeye yazılmadı: %v", notes)
	}
}

// TestSearchIsBounded — ARAMA PAHALI.
func TestSearchIsBounded(t *testing.T) {
	var missed []stackparse.Frame
	for i := 0; i < 6; i++ {
		missed = append(missed, searchFrame("com.x.C", "m", "C.java", 1))
	}
	calls := 0
	search := func(context.Context, string) ([]CodeSearchHit, error) {
		calls++
		return nil, nil
	}
	huntSearchWindows(context.Background(), missed, "", nil, 5, search,
		func(context.Context, string, string, string, string) (string, error) { return "", nil }, 2)
	if calls != 2 { // v0.10.353 — tavan artık parametre; testte 2
		t.Errorf("arama=%d, tavan 2 olmalıydı", calls)
	}
}

// TestSearchIsReachableAndOptIn — ⚠ ÖLÜ YOL OLMASIN, ama VARSAYILAN KAPALI.
//
// Bu gece bir kez saf çekirdek yeşil ama çağıranı pinsiz kaldı
// ([[feedback-tested-but-unreachable]]); bu kapı zinciri çiviliyor.
// Aynı anda varsayılanın KAPALI kaldığını da doğruluyor: arama yeni bir
// ağ çağrısı ve ayrı bir uzantı istiyor.
func TestSearchIsReachableAndOptIn(t *testing.T) {
	src := readDevopsSource(t, "code.go")
	if !strings.Contains(flatWSDevops(src), "if cfg.CodeSearch && len(hunt.missedFrames) > 0 {") {
		t.Error("arama FetchCode'dan çağrılmıyor — ölü yol")
	}
	if !strings.Contains(src, "huntSearchWindows(ctx, hunt.missedFrames") {
		t.Error("arama GERÇEK ıskalayan frame'lerle çağrılmıyor")
	}
	// Sıra: konvansiyon ÖNCE. Arama, kesin olanı ezmemeli.
	iHunt := strings.Index(src, "hunt := huntWindows(ctx, targets")
	iSearch := strings.Index(src, "if cfg.CodeSearch")
	if iHunt < 0 || iSearch < 0 || iSearch < iHunt {
		t.Error("arama konvansiyon avından ÖNCE koşuyor — belirsiz olan kesini eziyor")
	}
	// Varsayılan kapalı: sıfır değerli Settings'te arama yapılmamalı.
	if (Settings{}).CodeSearch {
		t.Error("kod araması VARSAYILAN AÇIK — doğrulanmamış bir uç prod yoluna girer")
	}
	// v0.10.85 — proje çıkmazı yedeği de ULAŞILABİLİR olmalı: saf çözücü
	// yeşilken çağrı yolu unutulursa operatörün duvarı geri gelir.
	if !strings.Contains(src, "searchResolveProjectRepo(ctx, targets") {
		t.Error("proje-çıkmazı yedeği FetchCode'dan çağrılmıyor — ölü yol (v0.10.85)")
	}
	// Dördüncü çare TEK yazımdan (searchOffRemedyTR) iki yüzeye de akmalı.
	for _, file := range []string{"code.go", "resolve_dryrun.go"} {
		if !strings.Contains(readDevopsSource(t, file), "searchOffRemedyTR") {
			t.Errorf("%s çıkmazda kod araması çaresini basmıyor", file)
		}
	}
}

// TestSearchResolveProjectRepo — proje çıkmazı çözücüsünün saf yarısı
// (v0.10.85, operatör-raporlu: servis başka DevOps projesi altında).
func TestSearchResolveProjectRepo(t *testing.T) {
	fr := func(class string) stackparse.Frame {
		return searchFrame(class, "run", "X.java", 10)
	}
	t.Run("projeli isabet kazanır, projesiz elenir", func(t *testing.T) {
		search := func(_ context.Context, q string) ([]CodeSearchHit, error) {
			return []CodeSearchHit{
				{Project: "", Repository: "r0", Path: "/src/com/x/A.java"},
				{Project: "PLATFORM", Repository: "transfer-core", Path: "/src/com/x/A.java", Branch: "master"},
			}, nil
		}
		prj, repo, note, ok := searchResolveProjectRepo(context.Background(),
			[]stackparse.Frame{fr("com.x.A")}, "", nil, search, codeSearchLimit)
		if !ok || prj != "PLATFORM" || repo != "transfer-core" {
			t.Fatalf("ok=%v prj=%q repo=%q", ok, prj, repo)
		}
		if !strings.Contains(note, "PLATFORM/transfer-core") {
			t.Errorf("künye kaynağı söylemiyor: %q", note)
		}
	})
	t.Run("ilk frame projesiz → ikinci frame çözer", func(t *testing.T) {
		call := 0
		search := func(_ context.Context, q string) ([]CodeSearchHit, error) {
			call++
			if call == 1 {
				return []CodeSearchHit{{Project: "", Repository: "r0", Path: "/p"}}, nil
			}
			return []CodeSearchHit{{Project: "P2", Repository: "r2", Path: "/src/com/y/B.java"}}, nil
		}
		prj, _, _, ok := searchResolveProjectRepo(context.Background(),
			[]stackparse.Frame{fr("com.x.A"), fr("com.y.B")}, "", nil, search, codeSearchLimit)
		if !ok || prj != "P2" {
			t.Fatalf("ikinci frame'e geçilmedi: ok=%v prj=%q (çağrı=%d)", ok, prj, call)
		}
	})
	t.Run("arama hatası künyeye yazılır", func(t *testing.T) {
		search := func(context.Context, string) ([]CodeSearchHit, error) {
			return nil, errSearchProbe
		}
		_, _, note, ok := searchResolveProjectRepo(context.Background(),
			[]stackparse.Frame{fr("com.x.A")}, "", nil, search, codeSearchLimit)
		if ok || !strings.Contains(note, "organizasyon araması başarısız") {
			t.Errorf("ok=%v note=%q", ok, note)
		}
	})
	t.Run("hepsi ıska → dürüst cümle", func(t *testing.T) {
		search := func(context.Context, string) ([]CodeSearchHit, error) { return nil, nil }
		_, _, note, ok := searchResolveProjectRepo(context.Background(),
			[]stackparse.Frame{fr("com.x.A")}, "", nil, search, codeSearchLimit)
		if ok || !strings.Contains(note, "eşleşme bulamadı") {
			t.Errorf("ok=%v note=%q", ok, note)
		}
	})
	t.Run("aranabilir frame yok → koşamadı", func(t *testing.T) {
		search := func(context.Context, string) ([]CodeSearchHit, error) {
			t.Fatal("sınıfsız frame'le arama yapılmamalı")
			return nil, nil
		}
		_, _, note, ok := searchResolveProjectRepo(context.Background(),
			[]stackparse.Frame{fr("")}, "", nil, search, codeSearchLimit)
		if ok || !strings.Contains(note, "koşamadı") {
			t.Errorf("ok=%v note=%q", ok, note)
		}
	})
}

var errSearchProbe = errors.New("http 404: extension not installed")

// TestNewerBranchWins — GÜNCELLİK (operatör: "daha güncel dosyaları
// dikkate alabilirsin").
//
// Arama sonucu TARİH taşımıyor. Ama kurulumun kendi branş sözleşmesi
// (Ayarlar → branş sırası) güncelliğin en iyi vekili — ve bir tahmin
// değil, operatörün YAZDIĞI bir karar.
func TestNewerBranchWins(t *testing.T) {
	hits := []CodeSearchHit{
		{Project: "P", Repository: "r", Path: "/src/com/x/A.java", Branch: "legacy"},
		{Project: "P", Repository: "r", Path: "/src/com/x/A.java", Branch: "release"},
	}
	got, ok := PickSearchHit(hits, "", frameFor("com.x.A", "m"), []string{"release", "master"})
	if !ok || got.Branch != "release" {
		t.Errorf("branş sırası kullanılmadı: %+v", got)
	}
	// ⚠ ASIL VAKA: İKİSİ DE listede, farklı RÜTBEDE. Yukarıdaki iki
	// iddia yalnız ÜYELİK bonusunu ölçüyor (legacy listede yok), yani
	// rütbe ağırlığını sökmek onları kırmıyordu — mutasyon denemesinde
	// tam bu oldu. Rütbeyi ölçen tek girdi budur.
	both := []CodeSearchHit{
		{Project: "P", Repository: "r", Path: "/src/com/x/A.java", Branch: "master"},
		{Project: "P", Repository: "r", Path: "/src/com/x/A.java", Branch: "release"},
	}
	if got, _ := PickSearchHit(both, "", frameFor("com.x.A", "m"), []string{"release", "master"}); got.Branch != "release" {
		t.Errorf("ikisi de listedeyken RÜTBE kullanılmadı: %+v", got)
	}
	if got, _ := PickSearchHit(both, "", frameFor("com.x.A", "m"), []string{"master", "release"}); got.Branch != "master" {
		t.Errorf("rütbe ters çevrildiğinde seçim değişmedi: %+v", got)
	}

	// Sıra TERSİNE çevrilince seçim de değişmeli — puan gerçekten
	// sıradan geliyor, sabit bir tercih değil.
	got, _ = PickSearchHit(hits, "", frameFor("com.x.A", "m"), []string{"legacy", "release"})
	if got.Branch != "legacy" {
		t.Errorf("branş sırası ters çevrildiğinde seçim değişmedi: %+v", got)
	}
}

// TestSearchURLPinsAPIVersion70 — v0.10.98, operatör-raporlu canlı 400:
// on-prem sunucu 7.1'i reddediyor ("latest supported is 7.0"). Sürümü
// 7.1'e geri almak, aramayı o kurulumda TEKRAR sıfır gün çalıştırır.
func TestSearchURLPinsAPIVersion70(t *testing.T) {
	u := searchURL(Settings{BaseURL: "https://devops.example.local/tfs", Collection: "DefaultCollection"})
	if !strings.HasSuffix(u, "api-version=7.0") {
		t.Fatalf("kod arama api-version 7.0 olmalı (on-prem tavanı): %s", u)
	}
}

// ── v0.10.100 — hata-kodu aramasının sözleşmeleri ───────────────────────
func TestHuntErrorCodeWindows(t *testing.T) {
	body := "line1\nthrow new CoreException(\"Acme.X.CustomerCardsNoFlag\");\nline3\nline4"
	search := func(_ context.Context, q string) ([]CodeSearchHit, error) {
		if q != "CustomerCardsNoFlag" {
			t.Errorf("sorgu token'ın kendisi olmalı: %q", q)
		}
		return []CodeSearchHit{
			{Project: "P", Repository: "", Path: "$/P/old/X.cs"}, // TFVC elenir
			{Project: "FIN", Repository: "financial-core", Path: "/src/Svc/X.cs", Branch: "release"},
		}, nil
	}
	var gotPrj string
	fetchIn := func(_ context.Context, prj, repo, br, pth string) (string, error) {
		gotPrj = prj
		return body, nil
	}
	out, notes := huntErrorCodeWindows(context.Background(),
		[]string{"CustomerCardsNoFlag"}, []string{"release", "master"}, 5, search, fetchIn, codeSearchLimit)
	if len(out) != 1 {
		t.Fatalf("pencere=%d: %v", len(out), notes)
	}
	w := out[0]
	// Pencere token'ın İLK geçtiği satıra merkezlenir — uzantı fark etmez.
	if w.Line != 2 || !strings.Contains(w.Content, "CustomerCardsNoFlag") {
		t.Errorf("satır=%d içerik=%q", w.Line, w.Content)
	}
	// Çapraz-proje çekimi + başka-depo etiketi (operatör yolu kendi
	// deposunda aramasın).
	if gotPrj != "FIN" || !strings.HasPrefix(w.Path, "financial-core:") {
		t.Errorf("proje/etiket: prj=%q path=%q", gotPrj, w.Path)
	}
	if w.Frame != "hata kodu: CustomerCardsNoFlag" {
		t.Errorf("frame etiketi: %q", w.Frame)
	}
	if len(notes) == 0 || !strings.Contains(notes[len(notes)-1], "dil-bağımsız") {
		t.Errorf("künye yok: %v", notes)
	}
}

func TestHuntErrorCodeWindowsFailureIsReported(t *testing.T) {
	_, notes := huntErrorCodeWindows(context.Background(), []string{"SomethingLong"},
		nil, 5,
		func(context.Context, string) ([]CodeSearchHit, error) { return nil, errSearchProbe },
		func(context.Context, string, string, string, string) (string, error) { return "", nil }, codeSearchLimit)
	if len(notes) == 0 || !strings.Contains(notes[0], "hata-kodu araması başarısız") {
		t.Errorf("arama hatası künyeye yazılmadı: %v", notes)
	}
}

// Ulaşılabilirlik: av FetchCode'dan gerçekten çağrılıyor
// ([[feedback-tested-but-unreachable]]).
func TestErrorCodeHuntIsReachable(t *testing.T) {
	src := readDevopsSource(t, "code.go")
	if !strings.Contains(src, "huntErrorCodeWindows(ctx, errTokens") {
		t.Error("hata-kodu avı FetchCode'dan çağrılmıyor — ölü yol")
	}
}

// ── v0.10.226 — GÜNCELLİK: çoklu depo isabetinde en son commit'lenen depo ──
//
// Operatör (2026-09-01): "birden fazla repo çıkabilir, en güncel olanını baz
// alabilir." v0.10.74 güncelliği branş sırasıyla VEKİL ediyordu (arama
// sonucu tarih taşımaz); şimdi depo başına son commit tarihi (Git API)
// gerçek sinyal. Sıra: TFVC elenir → EN YENİ depo (tarih biliniyorsa) →
// konvansiyon deposu → paket yolu → branş sırası → deterministik kuyruk.
// Tarih bilinmeyen depolar eski kurallara düşer (aramayı kırmaz).

func TestPickSearchHitRecency_NewestRepoWins(t *testing.T) {
	frame := stackparse.Frame{Class: "com.example.card.CardDetailBusiness", Method: "handle"}
	hits := []CodeSearchHit{
		{Project: "BSA", Repository: "card-legacy", Path: "/src/com/example/card/CardDetailBusiness.java", Branch: "release"},
		{Project: "BSA", Repository: "card-v2", Path: "/other/CardDetailBusiness.java", Branch: "master"},
		{Project: "BSA", Repository: "card-v2", Path: "/other/CardDetailBusiness.java", Branch: "release"},
	}
	rec := map[string]time.Time{
		RecencyKey("BSA", "card-legacy"): time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		RecencyKey("BSA", "card-v2"):     time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC),
	}
	// Konvansiyon deposu VE paket yolu eski depoyu işaret ediyor; güncellik
	// yine de yeni depoyu seçer, depo içinde branş sırası (release) kazanır.
	h, ok := PickSearchHitRecency(hits, "card-legacy", frame, []string{"release", "master"}, rec)
	if !ok || h.Repository != "card-v2" || h.Branch != "release" {
		t.Fatalf("newest repo must win (then branch order): %+v ok=%v", h, ok)
	}
	// Tarih bilinmiyorsa eski kurallar: konvansiyon deposu kazanır.
	h, ok = PickSearchHitRecency(hits, "card-legacy", frame, []string{"release", "master"}, nil)
	if !ok || h.Repository != "card-legacy" {
		t.Fatalf("without recency the convention repo wins: %+v", h)
	}
	// Yalnız BİR deponun tarihi biliniyorsa o depo bilinmeyenlerin önüne geçer.
	h, ok = PickSearchHitRecency(hits, "", frame, nil, map[string]time.Time{RecencyKey("BSA", "card-v2"): time.Now()})
	if !ok || h.Repository != "card-v2" {
		t.Fatalf("known-date repo outranks unknown: %+v", h)
	}
	// PickSearchHit (eski imza) = tarihsiz yol, davranışı değişmedi.
	h2, _ := PickSearchHit(hits, "card-legacy", frame, []string{"release", "master"})
	if h2.Repository != "card-legacy" {
		t.Fatalf("PickSearchHit wrapper drifted: %+v", h2)
	}
}

func TestRecencyKey(t *testing.T) {
	if RecencyKey("BSA", "Card-V2") != RecencyKey("bsa", "card-v2") {
		t.Fatalf("recency key must be case-insensitive (Azure DevOps names are)")
	}
}
