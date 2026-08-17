package insight

import (
	"math"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// signals_test.go — v0.9.1129 (AI Faz 2.1).
//
// Bu tablolar kartın DETERMİNİSTİK yarısının sözleşmesi: AI kapalı bir
// kurulumda operatörün gördüğü satırların tamamı buradan çıkıyor. Bir
// projeksiyon regresyonu prose'a benzemez — sessizce YANLIŞ sayı
// gösterir ve kimse "model yanlış söylemiş" diyemez.

const min = int64(60 * 1e9) // 1 dk, ns

func sigByLabel(sigs []Signal, label string) (Signal, bool) {
	for _, s := range sigs {
		if s.Label == label {
			return s, true
		}
	}
	return Signal{}, false
}

func TestExceptionSignals(t *testing.T) {
	now := int64(1_700_000_000) * 1e9

	cases := []struct {
		name      string
		ev        ExceptionEvidence
		wantLabel map[string]string // label → beklenen value
		wantSev   map[string]string // label → beklenen severity
		absent    []string
	}{
		{
			name: "canlı grup: son oluşum 5dk önce, deploy adayı var",
			ev: ExceptionEvidence{
				Fingerprint: "fp1", Type: "java.lang.NullPointerException",
				Service: "checkout", Occurrences: 1240, Last24: 380, PeakCount: 91,
				FirstSeenNs: now - 3*24*3600*1e9, LastSeenNs: now - 5*min,
				Deploys: []DeployCandidate{
					{Version: "v1.4.0", OffsetSec: 5400},         // 90dk önce
					{Version: "v1.4.1", OffsetSec: 300},          // 5dk önce ← en yakın
					{Version: "v1.4.2", OffsetSec: 60, After: true}, // SONRA — aday değil
				},
				NowNs: now,
			},
			wantLabel: map[string]string{
				"Oluşum":       "1.240 · son 24s 380 · tepe 91",
				"Tip":          "java.lang.NullPointerException",
				"Servis":       "checkout",
				"Son oluşum":   "5dk önce",
				"İlk görülme":  "3gün önce",
				"Yakın deploy": "v1.4.1 · grup başlangıcından 5dk önce",
			},
			wantSev: map[string]string{
				"Oluşum":       SevErr,
				"Son oluşum":   SevErr,
				"Yakın deploy": SevWarn,
			},
		},
		{
			name: "sakin grup: 24 saatte hiç yok",
			ev: ExceptionEvidence{
				Service: "orders", Occurrences: 12, Last24: 0,
				FirstSeenNs: now - 30*24*3600*1e9, LastSeenNs: now - 9*24*3600*1e9,
				NowNs:       now,
			},
			wantLabel: map[string]string{"Oluşum": "12 · son 24s 0", "Son oluşum": "9gün önce"},
			wantSev:   map[string]string{"Oluşum": SevOK, "Son oluşum": ""},
			// Tip boş → satır HİÇ çizilmez (boş değerli sinyal atılır).
			absent: []string{"Tip", "Yakın deploy"},
		},
		{
			name: "yalnız SONRASI deploy → aday satırı YOK",
			ev: ExceptionEvidence{
				Service: "billing", Occurrences: 3, LastSeenNs: now - 2*3600*1e9, NowNs: now,
				Deploys: []DeployCandidate{{Version: "v9", OffsetSec: 120, After: true}},
			},
			wantSev: map[string]string{"Son oluşum": SevWarn}, // 2sa → sıcak
			absent:  []string{"Yakın deploy"},
		},
		{
			name:   "damgasız grup: yaş satırları atlanır, çökme yok",
			ev:     ExceptionEvidence{Service: "x", NowNs: now},
			absent: []string{"Son oluşum", "İlk görülme"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, truncated := ExceptionSignals(tc.ev)
			if truncated {
				t.Error("exception projeksiyonunda liste-şekilli sinyal yok, truncated true olamaz")
			}
			for label, want := range tc.wantLabel {
				s, ok := sigByLabel(got, label)
				if !ok {
					t.Errorf("%q sinyali yok (%+v)", label, got)
					continue
				}
				if s.Value != want {
					t.Errorf("%q = %q; want %q", label, s.Value, want)
				}
			}
			for label, want := range tc.wantSev {
				s, ok := sigByLabel(got, label)
				if !ok {
					t.Errorf("%q sinyali yok", label)
					continue
				}
				if s.Severity != want {
					t.Errorf("%q severity = %q; want %q", label, s.Severity, want)
				}
			}
			for _, label := range tc.absent {
				if _, ok := sigByLabel(got, label); ok {
					t.Errorf("%q sinyali çizilmemeliydi", label)
				}
			}
			// MUTLAK SAAT YASAĞI (contract.go): sunucu UTC basar, sayfanın
			// kalanı tarayıcı saatini — yan yana çelişirler.
			for _, s := range got {
				if strings.Contains(s.Value, ":") && !strings.Contains(s.Value, "://") {
					t.Errorf("%q değeri saat gibi görünüyor (%q) — zaman BAĞIL olmalı", s.Label, s.Value)
				}
			}
		})
	}
}

func TestNearestDeployBefore(t *testing.T) {
	cases := []struct {
		name string
		in   []DeployCandidate
		want string
	}{
		{name: "boş", in: nil, want: ""},
		{name: "yalnız sonrası", in: []DeployCandidate{{Version: "a", OffsetSec: 10, After: true}}, want: ""},
		{name: "tek önce", in: []DeployCandidate{{Version: "a", OffsetSec: 900}}, want: "a"},
		{
			name: "en yakın önce kazanır",
			in: []DeployCandidate{
				{Version: "uzak", OffsetSec: 9000},
				{Version: "yakın", OffsetSec: 60},
				{Version: "sonra", OffsetSec: 1, After: true},
			},
			want: "yakın",
		},
		{
			// SIFIR uzaklıklı SONRA adayı: sub-saniye deploy ns→sn
			// kırpmasında 0'a düşer. İşaretle kodlanmış olsaydı burada
			// "önce" sanılırdı — After bayrağının varlık sebebi.
			name: "sıfır uzaklıklı sonra adayı yön bayrağıyla elenir",
			in: []DeployCandidate{
				{Version: "sonra0", OffsetSec: 0, After: true},
				{Version: "önce", OffsetSec: 120},
			},
			want: "önce",
		},
		{name: "sürümsüz aday atlanır", in: []DeployCandidate{{Version: " ", OffsetSec: 1}}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := NearestDeployBefore(tc.in)
			if tc.want == "" {
				if ok {
					t.Fatalf("aday bulunmamalıydı: %+v", got)
				}
				return
			}
			if !ok || got.Version != tc.want {
				t.Fatalf("got %+v (ok=%v); want %q", got, ok, tc.want)
			}
		})
	}
}

func TestProblemSignals(t *testing.T) {
	now := int64(1_700_000_000) * 1e9

	ev := ProblemEvidence{
		ID: "p1", Service: "checkout", Metric: "error_rate",
		Severity: "critical", Priority: "P1", PriorityReason: "kritik + deploy 4dk önce",
		Comparator: ">", Status: "open", Value: 12.5, Threshold: 5,
		StartedNs: now - 5*3600*1e9, NowNs: now,
		FromNs: now - 3600*1e9, ToNs: now,
		Deploy: &DeployRef{Version: "v2.1.0", AgeSec: 240, HasImpact: true,
			P99DeltaPct: 34, ErrDeltaPP: 2.4},
		Hyp: &HypothesisRef{TopSuspect: "payments-api", Confidence: 0.72,
			Candidates: []string{"redis", "postgres", "auth", "cdn"}, Others: 4},
		Blast:  &BlastRef{TotalCallers: 12, CascadingCallers: 3, TopCallers: []string{"web", "mobile-bff", "batch", "admin"}},
		SlowOp: &OpRef{Name: "POST /pay", P95Ms: 842, ErrorRate: 6.5},
	}

	got, truncated := ProblemSignals(ev)
	if !truncated {
		t.Error("aday ve çağıran listeleri tavana takıldı, truncated true olmalı")
	}
	want := map[string]string{
		"Şiddet":             "kritik",
		"Öncelik":            "P1 — kritik + deploy 4dk önce",
		"İhlal":              "error_rate 12.5 > eşik 5",
		"Süre":               "açık · 5sa",
		"Deploy":             "v2.1.0 · 4dk önce · p99 +34% · hata +2.4pp",
		"Kök-neden adayı":    "payments-api · güven %72",
		"Diğer adaylar":      "redis, postgres, auth +1",
		"Etki alanı":         "12 çağıran · 3 kaskad · web, mobile-bff, batch +1",
		"En yavaş operasyon": "POST /pay · p95 842ms · hata %6.5",
	}
	for label, w := range want {
		s, ok := sigByLabel(got, label)
		if !ok {
			t.Errorf("%q sinyali yok (%+v)", label, got)
			continue
		}
		if s.Value != w {
			t.Errorf("%q = %q; want %q", label, s.Value, w)
		}
	}
	// Şiddet renkleri: kritik ihlal + kaskad = err; ölçülmüş kötü deploy = err.
	for label, w := range map[string]string{
		"Şiddet": SevErr, "Öncelik": SevErr, "İhlal": SevErr,
		"Deploy": SevErr, "Etki alanı": SevErr, "Kök-neden adayı": SevWarn,
		"En yavaş operasyon": "",
	} {
		s, _ := sigByLabel(got, label)
		if s.Severity != w {
			t.Errorf("%q severity = %q; want %q", label, s.Severity, w)
		}
	}
	// Sinyal türlerinin hepsi kapalı kümede olmalı (FE ikon eşlemesi).
	allowed := map[string]bool{SignalDeploy: true, SignalProblem: true, SignalOpDelta: true,
		SignalBlast: true, SignalException: true, SignalGeneric: true}
	for _, s := range got {
		if !allowed[s.Kind] {
			t.Errorf("bilinmeyen sinyal türü %q (%q)", s.Kind, s.Label)
		}
	}
}

func TestProblemSignalsSparseEvidence(t *testing.T) {
	now := int64(1_700_000_000) * 1e9
	// Hipotez/blast/deploy YOK, comparator YOK, çözülmüş problem.
	res := now - 3600*1e9
	ev := ProblemEvidence{
		ID: "p2", Service: "orders", Metric: "p95_ms", Severity: "warning",
		Value: 900, Threshold: 400, StartedNs: now - 2*3600*1e9, ResolvedNs: res,
		NowNs: now, FromNs: now - 2*3600*1e9, ToNs: res,
	}
	got, truncated := ProblemSignals(ev)
	if truncated {
		t.Error("liste yok, truncated false olmalı")
	}
	if s, _ := sigByLabel(got, "İhlal"); s.Value != "p95_ms 900 (eşik 400)" {
		t.Errorf("karşılaştırıcısız ihlal = %q", s.Value)
	}
	if s, _ := sigByLabel(got, "Süre"); s.Value != "çözüldü · 1sa açık kaldı" || s.Severity != SevOK {
		t.Errorf("çözülmüş süre satırı = %+v", s)
	}
	if s, _ := sigByLabel(got, "Şiddet"); s.Severity != SevWarn {
		t.Errorf("uyarı şiddeti = %q", s.Severity)
	}
	for _, absent := range []string{"Deploy", "Kök-neden adayı", "Diğer adaylar", "Etki alanı", "En yavaş operasyon", "Öncelik"} {
		if _, ok := sigByLabel(got, absent); ok {
			t.Errorf("kanıt yokken %q sinyali çizildi", absent)
		}
	}
}

func TestProblemSignalsOpenLongEnoughWarns(t *testing.T) {
	now := int64(1_700_000_000) * 1e9
	for _, tc := range []struct {
		ageH int64
		want string
	}{{1, ""}, {3, ""}, {4, SevWarn}, {26, SevWarn}} {
		ev := ProblemEvidence{ID: "p", Service: "s", StartedNs: now - tc.ageH*3600*1e9, NowNs: now}
		got, _ := ProblemSignals(ev)
		s, ok := sigByLabel(got, "Süre")
		if !ok {
			t.Fatalf("%dsa: Süre sinyali yok", tc.ageH)
		}
		if s.Severity != tc.want {
			t.Errorf("%dsa açık → severity %q; want %q", tc.ageH, s.Severity, tc.want)
		}
	}
}

func TestCapNames(t *testing.T) {
	cases := []struct {
		name     string
		in       []string
		total    int
		wantKept []string
		wantCut  int
	}{
		{name: "boş", in: nil, total: 0, wantKept: []string{}, wantCut: 0},
		{name: "tavan altı", in: []string{"a", "b"}, total: 2, wantKept: []string{"a", "b"}, wantCut: 0},
		{name: "tam tavan", in: []string{"a", "b", "c"}, total: 3, wantKept: []string{"a", "b", "c"}, wantCut: 0},
		{name: "tavan üstü", in: []string{"a", "b", "c", "d", "e"}, total: 5, wantKept: []string{"a", "b", "c"}, wantCut: 2},
		{name: "total daha büyük", in: []string{"a", "b", "c"}, total: 9, wantKept: []string{"a", "b", "c"}, wantCut: 6},
		// Boş ad GERÇEK bir gizlenmiş ad değil: çağıran GERÇEK adların
		// sayısını verir, çöp kayıt "+1" üretmez.
		{name: "boş adlar süzülür", in: []string{"a", " ", "b"}, total: 2, wantKept: []string{"a", "b"}, wantCut: 0},
		// Yukarı-akış kırpması: liste 3, gerçek toplam 8 → "+5".
		{name: "yukarı akış kırpması dürüst", in: []string{"a", "b", "c"}, total: 8, wantKept: []string{"a", "b", "c"}, wantCut: 5},
		// total, gerçek listeden KÜÇÜK gelirse (çağıran zaten kırpmış)
		// negatif "+N" basmamalıyız.
		{name: "tutarsız total negatif üretmez", in: []string{"a", "b"}, total: 1, wantKept: []string{"a", "b"}, wantCut: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kept, cut := capNames(tc.in, tc.total)
			if strings.Join(kept, ",") != strings.Join(tc.wantKept, ",") {
				t.Errorf("kept = %v; want %v", kept, tc.wantKept)
			}
			if cut != tc.wantCut {
				t.Errorf("cut = %d; want %d", cut, tc.wantCut)
			}
		})
	}
}

// TestFmtDurTREveryUnit — v0.6.36 birim-karıştırma kuralı: değer+birim
// üreten şablon HER birimi test eder. Dört dal (sn/dk/sa/gün) + sınırlar.
func TestFmtDurTREveryUnit(t *testing.T) {
	cases := []struct {
		sec  int64
		want string
	}{
		{-5, "0sn"}, {0, "0sn"}, {1, "1sn"}, {59, "59sn"},
		{60, "1dk"}, {119, "1dk"}, {3599, "59dk"},
		{3600, "1sa"}, {3660, "1sa 1dk"}, {7200, "2sa"}, {86399, "23sa 59dk"},
		{86400, "1gün"}, {90000, "1gün 1sa"}, {259200, "3gün"},
	}
	for _, tc := range cases {
		if got := FmtDurTR(tc.sec); got != tc.want {
			t.Errorf("FmtDurTR(%d) = %q; want %q", tc.sec, got, tc.want)
		}
	}
}

// TestFmtDurTRMatchesGuidedSpelling — KAYNAK PİNİ. Aynı operatör aynı
// gün sohbette "38dk", kartta "38 min" görmemeli; iki formatlayıcı ayrı
// pakette yaşıyor (fmtAgoTR, internal/api/copilot_guided.go).
func TestFmtDurTRMatchesGuidedSpelling(t *testing.T) {
	const src = "../../api/copilot_guided.go"
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("%s okunamadı (fmtAgoTR taşındıysa pini yeniden konumlandır): %v", src, err)
	}
	body := string(b)
	i := strings.Index(body, "func fmtAgoTR(")
	if i < 0 {
		t.Fatalf("fmtAgoTR bulunamadı — pini yeniden konumlandır")
	}
	block := body[i:]
	if j := strings.Index(block, "\n}\n"); j > 0 {
		block = block[:j]
	}
	for _, verb := range []string{`"%dsn"`, `"%ddk"`, `"%dsa"`, `"%dsa %ddk"`, `"%dgün"`, `"%dgün %dsa"`} {
		if !strings.Contains(block, verb) {
			t.Errorf("fmtAgoTR %s biçimini artık kullanmıyor — FmtDurTR ile ayrıştı", verb)
		}
	}
}

func TestFmtNum(t *testing.T) {
	for _, tc := range []struct {
		in   uint64
		want string
	}{{0, "0"}, {7, "7"}, {999, "999"}, {1000, "1.000"}, {1240, "1.240"},
		{12345, "12.345"}, {1234567, "1.234.567"}} {
		if got := fmtNum(tc.in); got != tc.want {
			t.Errorf("fmtNum(%d) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// TestSignalsNeverPrintInfinity — sıfır tabanlı yüzde deltaları ±Inf
// üretebiliyor ve bu sayılar DİZEYE gömülü olduğu için writeJSON'un
// sanitizeFloats'ı onları temizleyemez. "p99 +Inf%" hem operatöre hem
// MODELE gider.
func TestSignalsNeverPrintInfinity(t *testing.T) {
	now := int64(1_700_000_000) * 1e9
	inf := math.Inf(1)
	ev := ProblemEvidence{
		ID: "p", Service: "s", Metric: "error_rate",
		Value: math.NaN(), Threshold: inf,
		StartedNs: now - 60*1e9, NowNs: now,
		Deploy: &DeployRef{Version: "v1", AgeSec: 60, HasImpact: true,
			P99DeltaPct: inf, ErrDeltaPP: math.NaN()},
		Hyp: &HypothesisRef{TopSuspect: "redis", Confidence: math.NaN()},
	}
	got, _ := ProblemSignals(ev)
	for _, s := range got {
		for _, bad := range []string{"Inf", "NaN"} {
			if strings.Contains(s.Value, bad) {
				t.Errorf("%q değeri %s taşıyor: %q", s.Label, bad, s.Value)
			}
		}
	}
	// Satır KAYBOLMAZ, yalnız ölçülemeyen parça düşer.
	if s, ok := sigByLabel(got, "Deploy"); !ok || s.Value != "v1 · 1dk önce" {
		t.Errorf("deploy satırı = %+v; sürüm+yaş korunmalı", s)
	}
	if s, ok := sigByLabel(got, "Kök-neden adayı"); !ok || s.Value != "redis" {
		t.Errorf("aday satırı = %+v; ad korunmalı", s)
	}
}

func TestFmtF(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
		// 0.125 → "0.12": %.2f yarı-değeri EN YAKIN ÇİFTE yuvarlar
		// (Go/IEEE davranışı). Kart sayısı olduğu için sorun değil, ama
		// beklentiyi pinliyoruz ki bir gün strconv'a geçilirse fark edilsin.
	}{{15, "15"}, {12.5, "12.5"}, {12.50, "12.5"}, {0.125, "0.12"}, {0, "0"}, {-3.20, "-3.2"}} {
		if got := fmtF(tc.in); got != tc.want {
			t.Errorf("fmtF(%v) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// ════════════════════════════════════════════════════════════════════
// v0.9.1137 (Faz 2.4) — log-pattern + slow-query projeksiyonları.
// ════════════════════════════════════════════════════════════════════

func TestLogPatternSignals(t *testing.T) {
	now := int64(1_700_000_000) * 1e9

	cases := []struct {
		name      string
		ev        LogPatternEvidence
		wantLabel map[string]string
		wantSev   map[string]string
		absent    []string
		wantTrunc bool
	}{
		{
			name: "patlama: oran, hacim, servis kırılımı",
			ev: LogPatternEvidence{
				Pattern: "Out of memory", Kind: "spike",
				CurrentCount: 1240, BaselineCount: 320, Ratio: 3.875,
				Service: "checkout", WindowSec: 300,
				TopServices: []PatternServiceRef{
					{Service: "checkout", Count: 900}, {Service: "web", Count: 340},
				},
				Sample:     "java.lang.OutOfMemoryError: Java heap  space\n\tat com.x.Y",
				LastSeenNs: now - 120*1e9, NowNs: now,
			},
			wantLabel: map[string]string{
				"Durum":               "PATLAMA ×3.88",
				"Pencere":             "son 5dk",
				"Hacim":               "şimdi 1.240 · taban 320",
				"Baskın servis":       "checkout",
				"Etkilenen servisler": "checkout (900), web (340)",
				"Son görülme":         "2dk önce",
				// Örnek satır TEK satıra indirilmiş ve boşluklar tekilleşmiş.
				"Örnek satır": "java.lang.OutOfMemoryError: Java heap space at com.x.Y",
			},
			wantSev: map[string]string{
				"Durum": SevErr, "Hacim": "", "Son görülme": SevErr,
			},
		},
		{
			name: "yeni desen: taban YOK, oran iddiası da yok",
			ev: LogPatternEvidence{
				Pattern: "Disk full", Kind: "new", CurrentCount: 42,
				Service: "worker", WindowSec: 900,
				LastSeenNs: now - 3*3600*1e9, NowNs: now,
			},
			wantLabel: map[string]string{
				"Durum":       "YENİ — bu pencerede ilk kez göründü",
				"Pencere":     "son 15dk",
				"Hacim":       "şimdi 42 · taban yok (yeni)",
				"Son görülme": "3sa önce",
			},
			wantSev: map[string]string{"Durum": SevWarn, "Son görülme": SevWarn},
			// Tek servisli kırılım LİSTE satırı üretmez (Baskın servis zaten söylüyor).
			absent: []string{"Etkilenen servisler", "Örnek satır"},
		},
		{
			name: "servis kırılımı tavana takılır → Truncated",
			ev: LogPatternEvidence{
				Pattern: "Auth failures", Kind: "spike", Ratio: 5,
				CurrentCount: 500, BaselineCount: 100, WindowSec: 60,
				TopServices: []PatternServiceRef{
					{Service: "a", Count: 5}, {Service: "b", Count: 4},
					{Service: "c", Count: 3}, {Service: "d", Count: 2},
					{Service: "e", Count: 1},
				},
				NowNs: now,
			},
			wantLabel: map[string]string{
				"Pencere":             "son 1dk",
				"Etkilenen servisler": "a (5), b (4), c (3) +2",
			},
			wantTrunc: true,
		},
		{
			name:   "boş kanıt çökmez, satır da uydurmaz",
			ev:     LogPatternEvidence{Pattern: "X", NowNs: now},
			absent: []string{"Durum", "Pencere", "Baskın servis", "Son görülme", "Örnek satır"},
			// Hacim satırı 0 sayıyla da yazılır (sıfır bir CEVAP).
			wantLabel: map[string]string{"Hacim": "şimdi 0"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, trunc := LogPatternSignals(tc.ev)
			for label, want := range tc.wantLabel {
				s, ok := sigByLabel(got, label)
				if !ok {
					t.Errorf("%q satırı yok: %+v", label, got)
					continue
				}
				if s.Value != want {
					t.Errorf("%q = %q; want %q", label, s.Value, want)
				}
			}
			for label, want := range tc.wantSev {
				s, _ := sigByLabel(got, label)
				if s.Severity != want {
					t.Errorf("%q şiddeti = %q; want %q", label, s.Severity, want)
				}
			}
			for _, label := range tc.absent {
				if _, ok := sigByLabel(got, label); ok {
					t.Errorf("%q satırı üretilmemeliydi", label)
				}
			}
			if trunc != tc.wantTrunc {
				t.Errorf("truncated = %v; want %v", trunc, tc.wantTrunc)
			}
			// MUTLAK SAAT YASAĞI: hiçbir değer tarih damgası taşımaz.
			for _, s := range got {
				if strings.Contains(s.Value, "2023-") || strings.Contains(s.Value, "T00:") {
					t.Errorf("%q mutlak saat taşıyor: %q", s.Label, s.Value)
				}
			}
		})
	}
}

// Şiddet karışımı YOK ve kart onu UYDURMUYOR: projeksiyonda "severity"
// diye bir satır olmaması bilinçli (detektörün okuması o boyutu
// döndürmüyor; göstermek YENİ bir sorgu şekli isterdi).
func TestLogPatternSignalsHasNoSeverityMix(t *testing.T) {
	got, _ := LogPatternSignals(LogPatternEvidence{
		Pattern: "X", Kind: "spike", CurrentCount: 10, BaselineCount: 2, Ratio: 5})
	for _, s := range got {
		if strings.Contains(strings.ToLower(s.Label), "seviye") ||
			strings.Contains(strings.ToLower(s.Label), "severity") {
			t.Errorf("olmayan bir kırılım gösteriliyor: %+v", s)
		}
	}
}

func TestSlowQuerySignals(t *testing.T) {
	now := int64(1_700_000_000) * 1e9

	cases := []struct {
		name      string
		ev        SlowQueryEvidence
		wantLabel map[string]string
		wantSev   map[string]string
		absent    []string
		wantTrunc bool
	}{
		{
			name: "yavaş sınıf: p99 kırmızı, çağıran kırılımı",
			ev: SlowQueryEvidence{
				StmtParam: "12345|oracle",
				Statement: "SELECT  *\nFROM T WHERE ID = ?",
				DBSystem:  "oracle", DBName: "COREBANK",
				Calls: 12345, Errors: 82,
				TotalMs: 41_200, AvgMs: 3.3, P95Ms: 842, P99Ms: 1240, MaxMs: 3100,
				Callers: []CallerRef{
					{Service: "payments-api", Calls: 8100, P95Ms: 902, TotalMs: 30_000},
					{Service: "web", Calls: 4245, P95Ms: 400, TotalMs: 11_200},
				},
				FromNs: now - 3600*1e9, ToNs: now, NowNs: now,
			},
			wantLabel: map[string]string{
				"Gecikme":       "p95 842ms · p99 1.2sn · maks 3.1sn",
				"Hacim":         "12.345 çağrı · toplam 41.2sn · ort 3ms",
				"Hata":          "82 hata (%0.7)",
				"Motor":         "oracle · COREBANK",
				"En çok çağıran": "payments-api · 8.100 çağrı · p95 902ms",
				"Çağıranlar":    "2 servis: payments-api, web",
				"İfade":         "SELECT * FROM T WHERE ID = ?",
			},
			wantSev: map[string]string{"Gecikme": SevErr, "Hata": SevErr, "Hacim": ""},
		},
		{
			name: "sağlıklı sınıf: hata satırı YOK, p99 sarı",
			ev: SlowQueryEvidence{
				StmtParam: "9", Statement: "UPDATE T SET A = ?",
				DBSystem: "postgresql", DBName: "default",
				Calls: 40, TotalMs: 12_000, AvgMs: 300, P95Ms: 280, P99Ms: 420,
				FromNs: now - 900*1e9, ToNs: now, NowNs: now,
			},
			wantLabel: map[string]string{
				"Gecikme": "p95 280ms · p99 420ms",
				// 'default' MV nöbetçisi — veritabanı adı gibi BASILMAZ.
				"Motor": "postgresql",
				"Hacim": "40 çağrı · toplam 12.0sn · ort 300ms",
			},
			wantSev: map[string]string{"Gecikme": SevWarn},
			absent:  []string{"Hata", "En çok çağıran", "Çağıranlar"},
		},
		{
			name: "çağıran okuması TAVANA dayandı → Truncated",
			ev: SlowQueryEvidence{
				StmtParam: "5", Statement: "SELECT 1", DBSystem: "mysql",
				Calls: 10, P95Ms: 10, P99Ms: 20,
				Callers: []CallerRef{
					{Service: "a"}, {Service: "b"}, {Service: "c"},
					{Service: "d"}, {Service: "e"}, {Service: "f"},
				},
				CallersCapped: true,
				FromNs:        now - 900*1e9, ToNs: now, NowNs: now,
			},
			wantLabel: map[string]string{"Çağıranlar": "6 servis: a, b, c +3"},
			wantTrunc: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, trunc := SlowQuerySignals(tc.ev)
			for label, want := range tc.wantLabel {
				s, ok := sigByLabel(got, label)
				if !ok {
					t.Errorf("%q satırı yok: %+v", label, got)
					continue
				}
				if s.Value != want {
					t.Errorf("%q = %q; want %q", label, s.Value, want)
				}
			}
			for label, want := range tc.wantSev {
				s, _ := sigByLabel(got, label)
				if s.Severity != want {
					t.Errorf("%q şiddeti = %q; want %q", label, s.Severity, want)
				}
			}
			for _, label := range tc.absent {
				if _, ok := sigByLabel(got, label); ok {
					t.Errorf("%q satırı üretilmemeliydi", label)
				}
			}
			if trunc != tc.wantTrunc {
				t.Errorf("truncated = %v; want %v", trunc, tc.wantTrunc)
			}
		})
	}
}

// Sonsuz/NaN değerler DİZEYE gömülü basılmaz (writeJSON'un sanitizeFloats'ı
// bunları göremez — v0.9.1132'nin dersi).
func TestSlowQuerySignalsNeverPrintInfinity(t *testing.T) {
	inf, nan := math.Inf(1), math.NaN()
	got, _ := SlowQuerySignals(SlowQueryEvidence{
		StmtParam: "1", Statement: "SELECT 1", DBSystem: "oracle",
		Calls: 10, Errors: 1, TotalMs: inf, AvgMs: nan, P95Ms: inf, P99Ms: nan, MaxMs: inf,
		Callers: []CallerRef{{Service: "a", Calls: 1, P95Ms: inf}},
	})
	for _, s := range got {
		for _, bad := range []string{"Inf", "NaN", "+Inf"} {
			if strings.Contains(s.Value, bad) {
				t.Errorf("%q değeri %s taşıyor: %q", s.Label, bad, s.Value)
			}
		}
	}
	// Satır KAYBOLMAZ: ölçülemeyen parça "—" olur, sayılabilen kalır.
	if s, ok := sigByLabel(got, "Hacim"); !ok || !strings.Contains(s.Value, "10 çağrı") {
		t.Errorf("hacim satırı = %+v; çağrı sayısı korunmalı", s)
	}
}

// fmtMs — HER BİRİM dalı tabloda (v0.6.36 kuralı: değer+birim üreten
// şablon her birimi testler). Eşik değerleri de: 999/1000 ve 59999/60000.
func TestFmtMsEveryUnit(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{0, "0ms"}, {0.4, "0ms"}, {1, "1ms"}, {842, "842ms"}, {999, "999ms"},
		{999.6, "1000ms"}, // %.0f yuvarlaması dalı DEĞİŞTİRMEZ (< 1000 testi ham değerde)
		{1000, "1.0sn"}, {1240, "1.2sn"}, {41_200, "41.2sn"}, {59_999, "60.0sn"},
		{60_000, "1.0dk"}, {138_000, "2.3dk"}, {3_600_000, "60.0dk"},
		{-1, "—"},
	} {
		if got := fmtMs(tc.in); got != tc.want {
			t.Errorf("fmtMs(%v) = %q; want %q", tc.in, got, tc.want)
		}
	}
	if got := fmtMs(math.Inf(1)); got != "—" {
		t.Errorf("fmtMs(+Inf) = %q", got)
	}
	if got := fmtMs(math.NaN()); got != "—" {
		t.Errorf("fmtMs(NaN) = %q", got)
	}
}

// oneLine — RUNE sınırında keser. Bayt kesmesi geçersiz UTF-8 üretir ve o
// dize hem panele hem PROMPT'a gider (anomaly.truncateSample bayt kesiyor;
// kart o tuzağı tekrarlamıyor).
func TestOneLineIsRuneSafe(t *testing.T) {
	in := "çğüşiö" + strings.Repeat("ı", 20)
	got := oneLine(in, 8)
	if !utf8.ValidString(got) {
		t.Errorf("geçersiz UTF-8 üretildi: %q", got)
	}
	if r := []rune(got); len(r) != 9 { // 8 + "…"
		t.Errorf("rune sayısı = %d; want 9 (%q)", len(r), got)
	}
	// Satır sonları ve tekrarlı boşluklar tekilleşir.
	if got := oneLine("a\n\tb   c", 0); got != "a b c" {
		t.Errorf("oneLine boşluk normalizasyonu = %q", got)
	}
	if got := oneLine("kısa", 100); got != "kısa" {
		t.Errorf("tavan altında kesildi: %q", got)
	}
}
