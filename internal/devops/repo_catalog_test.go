package devops

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// repo_catalog_test.go — v0.9.1236 kaçış kapısının SAF çekirdeği.
//
// Neden burası kritik: eşleştirici yanlış bir adayı seçerse sonuç
// "kod yok" değil, BAŞKA BİR UYGULAMANIN kodunun kanıt diye
// gösterilmesidir. O yüzden testler yalnız "buldu mu"yu değil,
// hangi BASAMAĞIN tuttuğunu ve çoklu adayda seçimin DETERMİNİSTİK
// olduğunu da pinliyor.

func TestMatchRepoName(t *testing.T) {
	// Sahadaki asıl vaka: konvansiyon küçük harf üretir
	// (bsa-cashmanagement-cashflow-prod → cashmanagement-cashflow),
	// gerçek depo "CashManagement.CashFlow" olabilir.
	server := []string{
		"CashManagement.CashFlow",
		"Payments.Core",
		"cash_management-Legacy",
		"digital-mobile-pushconfirm",
	}

	cases := []struct {
		name     string
		want     string
		have     []string
		wantName string
		wantRung string
		wantAlts []string
	}{
		{
			name: "birebir eşleşme", want: "Payments.Core", have: server,
			wantName: "Payments.Core", wantRung: RepoRungExact,
		},
		{
			name: "yalnız harf yazımı farklı", want: "digital-mobile-PushConfirm", have: server,
			wantName: "digital-mobile-pushconfirm", wantRung: RepoRungFold,
		},
		{
			name: "ayraç + harf farkı (asıl vaka)", want: "cashmanagement-cashflow", have: server,
			wantName: "CashManagement.CashFlow", wantRung: RepoRungLoose,
		},
		{
			name: "nokta ↔ alt çizgi", want: "cash.management.legacy", have: server,
			wantName: "cash_management-Legacy", wantRung: RepoRungLoose,
		},
		{
			name: "eşleşme yok", want: "billing-gateway", have: server,
			wantName: "", wantRung: "",
		},
		{
			name: "boş istek", want: "  ", have: server,
			wantName: "", wantRung: "",
		},
		{
			name: "boş liste", want: "anything", have: nil,
			wantName: "", wantRung: "",
		},
		{
			// Basamak ÖNCELİĞİ: fold varken loose'a düşülmez.
			name: "fold, loose'u yener", want: "CASHFLOW",
			have:     []string{"cash-flow", "CashFlow"},
			wantName: "CashFlow", wantRung: RepoRungFold,
		},
		{
			// Birden çok aday: sunucu sırasına DEĞİL, sıralı ilkine.
			name: "çoklu aday deterministik", want: "cashflow",
			have:     []string{"Cash_Flow", "cash-flow", "CASH.FLOW"},
			wantName: "CASH.FLOW", wantRung: RepoRungLoose,
			wantAlts: []string{"Cash_Flow", "cash-flow"},
		},
		{
			// Aynı liste ters sırada AYNI sonucu vermeli.
			name: "çoklu aday, ters sıra, aynı sonuç", want: "cashflow",
			have:     []string{"CASH.FLOW", "cash-flow", "Cash_Flow"},
			wantName: "CASH.FLOW", wantRung: RepoRungLoose,
			wantAlts: []string{"Cash_Flow", "cash-flow"},
		},
		{
			name: "yinelenen adlar tek sayılır", want: "core",
			have:     []string{"Core", "Core", "Core"},
			wantName: "Core", wantRung: RepoRungFold,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchRepoName(tc.want, tc.have)
			if got.Name != tc.wantName {
				t.Fatalf("Name=%q, istenen %q", got.Name, tc.wantName)
			}
			if got.Rung != tc.wantRung {
				t.Fatalf("Rung=%q, istenen %q", got.Rung, tc.wantRung)
			}
			if len(tc.wantAlts) > 0 && !reflect.DeepEqual(got.Alts, tc.wantAlts) {
				t.Fatalf("Alts=%v, istenen %v", got.Alts, tc.wantAlts)
			}
		})
	}
}

// TestMatchRepoNameNear — eşleşme yoksa 2-3 YAKIN ad; liste DÖKÜLMEZ.
func TestMatchRepoNameNear(t *testing.T) {
	have := []string{
		"cashflow-api", "cashflow-worker", "cashflow-batch", "cashflow-ui",
		"payments-core", "identity", "billing",
	}
	m := MatchRepoName("cashflow", have)
	if m.Name != "" {
		t.Fatalf("eşleşmemeliydi, Name=%q", m.Name)
	}
	if len(m.Near) == 0 {
		t.Fatal("Near boş — operatöre hiçbir ipucu verilmiyor")
	}
	if len(m.Near) > repoNearMax {
		t.Fatalf("Near=%d ad, tavan %d — tam liste dökülüyor", len(m.Near), repoNearMax)
	}
	for _, n := range m.Near {
		if !strings.HasPrefix(n, "cashflow") {
			t.Fatalf("alakasız aday önerildi: %q (%v)", n, m.Near)
		}
	}

	t.Run("hiç benzemeyen liste öneri üretmez", func(t *testing.T) {
		m := MatchRepoName("zzz-unrelated", []string{"payments-core", "identity"})
		if len(m.Near) != 0 {
			t.Fatalf("gürültü önerildi: %v", m.Near)
		}
	})

	t.Run("içerme önek uzunluğunu yener", func(t *testing.T) {
		// "cashflow" isteniyor: "bsa-cashflow" ÖNEKten hiç puan almaz
		// (ortak önek 0) ama içerdiği için en üste çıkmalı.
		m := MatchRepoName("cashflow", []string{"cashier-desk", "bsa-cashflow"})
		if len(m.Near) == 0 || m.Near[0] != "bsa-cashflow" {
			t.Fatalf("Near=%v, ilk sırada bsa-cashflow beklenirdi", m.Near)
		}
	})
}

func TestNormalizeRepoName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"CashManagement.CashFlow", "cashmanagementcashflow"},
		{"cash_management-CashFlow", "cashmanagementcashflow"},
		{"cash management cashflow", "cashmanagementcashflow"},
		{"  Payments.Core  ", "paymentscore"},
		{"", ""},
		{"---", ""},
	}
	for _, tc := range cases {
		if got := normalizeRepoName(tc.in); got != tc.want {
			t.Errorf("normalizeRepoName(%q)=%q, istenen %q", tc.in, got, tc.want)
		}
	}
}

// TestIsAuthClassErr — PAT arızasında kaçış kapısı hiç açılmamalı.
func TestIsAuthClassErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"401", errors.New("http 401 — check the PAT and its scopes (Code: Read)"), true},
		{"403", errors.New("http 403 — check the PAT and its scopes (Code: Read)"), true},
		{"404 (kapı AÇILMALI)", errors.New("http 404: TF401019: repo does not exist"), false},
		{"bulunamadı (kapı AÇILMALI)", errors.New(`depo "x" bulunamadı`), false},
		{"ağ hatası (kapı AÇILMALI)", errors.New("dial tcp: connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAuthClassErr(tc.err); got != tc.want {
				t.Fatalf("isAuthClassErr=%v, istenen %v", got, tc.want)
			}
		})
	}
}

func TestWithNote(t *testing.T) {
	cases := []struct{ note, reason, want string }{
		{"", "", ""},
		{"", "hata", "hata"},
		{"düzeltildi", "", "düzeltildi"},
		{"düzeltildi", "hata", "düzeltildi · hata"},
	}
	for _, tc := range cases {
		if got := withNote(tc.note, tc.reason); got != tc.want {
			t.Errorf("withNote(%q,%q)=%q, istenen %q", tc.note, tc.reason, got, tc.want)
		}
	}
}

// TestReposURLShape — liste ucu ile ada göre çözüm ucu AYNI kökten.
func TestReposURLShape(t *testing.T) {
	cfg := Settings{BaseURL: "https://tfs.example/tfs/", Collection: "DefaultCollection", Project: "BSA"}
	root := reposURL(cfg)
	if root != "https://tfs.example/tfs/DefaultCollection/BSA/_apis/git/repositories" {
		t.Fatalf("reposURL=%q", root)
	}
	if one := repoURL(cfg, "Cash.Flow"); one != root+"/Cash.Flow" {
		t.Fatalf("repoURL=%q, kök %q ile başlamalıydı", one, root)
	}
}
