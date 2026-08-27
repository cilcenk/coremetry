package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/devops"
)

// v0.9.829 — Settings → Azure DevOps / TFS bağlantı katmanı.
//
// mergeDevOpsSettings is the whole secret contract in one pure
// function, so it is table-tested directly rather than through an
// HTTP round-trip. The PAT never round-trips to the browser, which
// means "empty means keep" is load-bearing: get it wrong and an
// operator who edits the Project field wipes working credentials.

const (
	storedPAT = "STORED-PAT-abc123"
	freshPAT  = "FRESH-PAT-xyz789"
)

func baseCur() devops.Settings {
	return devops.Settings{
		BaseURL:    "https://dev.example.local/tfs",
		Collection: "DefaultCollection",
		PAT:        storedPAT,
		Flavor:     devops.FlavorAuto,
	}
}

func baseInput() devopsSettingsInput {
	return devopsSettingsInput{
		BaseURL:    "https://dev.example.local/tfs",
		Collection: "DefaultCollection",
		Flavor:     devops.FlavorAuto,
	}
}

// ── the secret merge table ──────────────────────────────────────

func TestMergeDevOpsSettings_SecretMerge(t *testing.T) {
	cases := []struct {
		name    string
		pat     string
		cur     string
		wantPAT string
	}{
		{"empty preserves stored", "", storedPAT, storedPAT},
		{"sentinel preserves stored", secretKept, storedPAT, storedPAT},
		{"new value replaces", freshPAT, storedPAT, freshPAT},
		{"empty with nothing stored stays empty", "", "", ""},
		{"sentinel with nothing stored stays empty", secretKept, "", ""},
		{"first-time set", freshPAT, "", freshPAT},
		// A PAT that merely CONTAINS the sentinel is a real value,
		// not a keep-signal — the comparison must be exact.
		{"value containing the sentinel is not a keep-signal",
			"pre********post", storedPAT, "pre********post"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := baseInput()
			in.PAT = c.pat
			cur := baseCur()
			cur.PAT = c.cur

			cfg, bad := mergeDevOpsSettings(in, cur)
			if bad != "" {
				t.Fatalf("unexpected rejection: %s", bad)
			}
			if cfg.PAT != c.wantPAT {
				t.Errorf("PAT = %q, want %q", cfg.PAT, c.wantPAT)
			}
		})
	}
}

// Clearing the server URL removes the integration — the stored
// PAT must go with it rather than lingering in system_settings
// as an orphan no screen shows.
func TestMergeDevOpsSettings_ClearingURLDropsPAT(t *testing.T) {
	for _, pat := range []string{"", secretKept} {
		in := baseInput()
		in.BaseURL = ""
		in.PAT = pat

		cfg, bad := mergeDevOpsSettings(in, baseCur())
		if bad != "" {
			t.Fatalf("unexpected rejection: %s", bad)
		}
		if cfg.PAT != "" {
			t.Errorf("pat=%q: stored PAT survived a cleared connection: %q", pat, cfg.PAT)
		}
	}
}

// Editing a non-secret field with a blank PAT box is the exact
// gesture that must not clear credentials.
func TestMergeDevOpsSettings_EditingOtherFieldsKeepsPAT(t *testing.T) {
	in := baseInput()
	in.Project = "ProjA"
	in.Username = "svc_coremetry"
	in.InsecureSkipVerify = true
	in.Flavor = devops.FlavorTFS

	cfg, bad := mergeDevOpsSettings(in, baseCur())
	if bad != "" {
		t.Fatalf("unexpected rejection: %s", bad)
	}
	if cfg.PAT != storedPAT {
		t.Errorf("PAT = %q, want the stored one preserved", cfg.PAT)
	}
	if cfg.Project != "ProjA" || cfg.Username != "svc_coremetry" ||
		!cfg.InsecureSkipVerify || cfg.Flavor != devops.FlavorTFS {
		t.Errorf("non-secret edits did not land: %+v", cfg)
	}
}

// ── validation ──────────────────────────────────────────────────

func TestMergeDevOpsSettings_Validation(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(*devopsSettingsInput)
		wantReject string // substring; "" = accept
	}{
		{"auto is the default flavor", func(in *devopsSettingsInput) { in.Flavor = "" }, ""},
		{"azure-devops-server accepted",
			func(in *devopsSettingsInput) { in.Flavor = devops.FlavorServer }, ""},
		{"tfs accepted", func(in *devopsSettingsInput) { in.Flavor = devops.FlavorTFS }, ""},
		{"unknown flavor rejected",
			func(in *devopsSettingsInput) { in.Flavor = "github" }, "flavor must be one of"},
		{"schemeless URL rejected",
			func(in *devopsSettingsInput) { in.BaseURL = "dev.example.local/tfs" }, "http:// or https://"},
		{"http accepted (internal networks)",
			func(in *devopsSettingsInput) { in.BaseURL = "http://dev.example.local/tfs" }, ""},
		{"uppercase scheme accepted",
			func(in *devopsSettingsInput) { in.BaseURL = "HTTPS://dev.example.local" }, ""},
		{"empty URL accepted — that's how you clear the connection",
			func(in *devopsSettingsInput) { in.BaseURL = "" }, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := baseInput()
			c.mutate(&in)
			_, bad := mergeDevOpsSettings(in, baseCur())
			if c.wantReject == "" {
				if bad != "" {
					t.Errorf("unexpected rejection: %s", bad)
				}
				return
			}
			if !strings.Contains(bad, c.wantReject) {
				t.Errorf("rejection = %q, want it to mention %q", bad, c.wantReject)
			}
		})
	}
}

func TestMergeDevOpsSettings_TrimsWhitespace(t *testing.T) {
	in := devopsSettingsInput{
		BaseURL:    "  https://dev.example.local/tfs  ",
		Collection: "  DefaultCollection ",
		Project:    " ProjA ",
		Username:   " svc ",
		Flavor:     " tfs ",
	}
	cfg, bad := mergeDevOpsSettings(in, devops.Settings{})
	if bad != "" {
		t.Fatalf("unexpected rejection: %s", bad)
	}
	if cfg.BaseURL != "https://dev.example.local/tfs" || cfg.Collection != "DefaultCollection" ||
		cfg.Project != "ProjA" || cfg.Username != "svc" || cfg.Flavor != devops.FlavorTFS {
		t.Errorf("whitespace survived the merge: %+v", cfg)
	}
}

// The PAT is deliberately NOT trimmed — a token may legitimately
// begin or end with characters we have no business editing, and a
// silent trim would produce a 401 nobody can explain.
func TestMergeDevOpsSettings_PATNotTrimmed(t *testing.T) {
	in := baseInput()
	in.PAT = " padded-pat "
	cfg, bad := mergeDevOpsSettings(in, devops.Settings{})
	if bad != "" {
		t.Fatalf("unexpected rejection: %s", bad)
	}
	if cfg.PAT != " padded-pat " {
		t.Errorf("PAT = %q, want it byte-for-byte", cfg.PAT)
	}
}

// ── the audit pin ───────────────────────────────────────────────

// Secrets must never reach audit_log. hasPat is the only
// secret-adjacent bit allowed, and it is already in the GET shape.
func TestDevOpsAuditDetails_NoSecrets(t *testing.T) {
	svc := devops.New()
	svc.Configure(devops.Settings{
		BaseURL:            "https://dev.example.local/tfs",
		Collection:         "DefaultCollection",
		Project:            "ProjA",
		Username:           "svc_coremetry",
		PAT:                storedPAT,
		Flavor:             devops.FlavorTFS,
		InsecureSkipVerify: true,
	})
	blob := string(devopsAuditDetails(svc.Snapshot()))

	if strings.Contains(blob, storedPAT) {
		t.Fatalf("PAT reached audit details: %s", blob)
	}
	// The username is an identity too — it stays out.
	if strings.Contains(blob, "svc_coremetry") {
		t.Fatalf("username reached audit details: %s", blob)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(blob), &got); err != nil {
		t.Fatalf("audit details are not valid JSON: %v", err)
	}
	// Exact key set — a future field can't drift in unreviewed.
	// repoPrefixes / branchOrder joined in v0.9.830: not secrets, and
	// a convention edit silently repoints which source file every AI
	// answer quotes, so the trail wants it.
	// codeSearch v0.10.75'te eklendi: SIR DEĞİL, ama denetlenmesi gereken
	// bir karar — açıldığında Coremetry organizasyonun TAMAMINDA kod
	// aramaya başlıyor ve kanıt olarak başka depoların dosyalarını
	// gösteriyor. Kimin ne zaman açtığı izde durmalı.
	// appPrefixes / codeLookupLimit v0.10.112: önek listesi hangi
	// dosyanın ÖNCE kanıt olacağını, tavan kaç dosyanın kanıt olabileceğini
	// değiştirir — repoPrefixes ile aynı sınıf, izde durmalı.
	want := []string{"baseUrl", "collection", "project", "flavor", "hasPat",
		"insecureSkipVerify", "repoPrefixes", "branchOrder", "codeSearch",
		"appPrefixes", "codeLookupLimit"}
	if len(got) != len(want) {
		t.Errorf("audit keys = %v, want exactly %v", keysOf(got), want)
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("audit details missing %q: %s", k, blob)
		}
	}
	if got["hasPat"] != true {
		t.Errorf("hasPat = %v, want true", got["hasPat"])
	}
	// The host IS in the trail on purpose — not a secret, and the
	// most useful thing to have when someone repoints the server.
	if got["baseUrl"] != "https://dev.example.local/tfs" {
		t.Errorf("baseUrl = %v, want the server URL recorded", got["baseUrl"])
	}
}

func TestDevOpsAuditDetails_HasPATFalseWhenUnset(t *testing.T) {
	svc := devops.New()
	svc.Configure(devops.Settings{BaseURL: "https://dev.example.local", Flavor: devops.FlavorAuto})
	var got map[string]any
	if err := json.Unmarshal(devopsAuditDetails(svc.Snapshot()), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["hasPat"] != false {
		t.Errorf("hasPat = %v, want false", got["hasPat"])
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ── v0.9.830: adlandırma konvansiyonu alanları ──────────────────

// TestMergeDevOpsSettings_ConventionLists — repoPrefixes / branchOrder
// normalizasyonu. Kritik olan BOŞ → nil: alanı temizleyen operatör
// varsayılana dönmeli, "tek bir imkânsız önek" listesine değil.
func TestMergeDevOpsSettings_ConventionLists(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil stays nil (defaults apply)", nil, nil},
		{"empty slice → nil", []string{}, nil},
		{"blank entries dropped → nil", []string{"", "   "}, nil},
		{"entries trimmed", []string{" bsa- ", "svc-"}, []string{"bsa-", "svc-"}},
		{"blank entry dropped, rest kept", []string{"bsa-", "", "svc-"}, []string{"bsa-", "svc-"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInput()
			in.RepoPrefixes = tc.in
			in.BranchOrder = tc.in
			cfg, bad := mergeDevOpsSettings(in, baseCur())
			if bad != "" {
				t.Fatalf("unexpected 400: %s", bad)
			}
			if !equalStrings(cfg.RepoPrefixes, tc.want) {
				t.Errorf("RepoPrefixes = %v, want %v", cfg.RepoPrefixes, tc.want)
			}
			if !equalStrings(cfg.BranchOrder, tc.want) {
				t.Errorf("BranchOrder = %v, want %v", cfg.BranchOrder, tc.want)
			}
		})
	}
}

// TestDevOpsSnapshotEchoesResolvedConvention — snapshot RESOLVED
// değerleri döner: hiçbir şey kaydedilmemişken kart iki boş kutu
// göstermemeli, çözücünün gerçekte neyi soyduğunu göstermeli.
func TestDevOpsSnapshotEchoesResolvedConvention(t *testing.T) {
	svc := devops.New()
	svc.Configure(devops.Settings{BaseURL: "https://dev.example.local/tfs"})
	snap := svc.Snapshot()
	if !equalStrings(snap.RepoPrefixes, devops.DefaultRepoPrefixes()) {
		t.Errorf("RepoPrefixes = %v, want defaults %v", snap.RepoPrefixes, devops.DefaultRepoPrefixes())
	}
	if !equalStrings(snap.BranchOrder, devops.DefaultBranchOrder()) {
		t.Errorf("BranchOrder = %v, want defaults %v", snap.BranchOrder, devops.DefaultBranchOrder())
	}

	svc.Configure(devops.Settings{
		BaseURL:      "https://dev.example.local/tfs",
		RepoPrefixes: []string{"svc-"}, BranchOrder: []string{"main"},
	})
	snap = svc.Snapshot()
	if !equalStrings(snap.RepoPrefixes, []string{"svc-"}) || !equalStrings(snap.BranchOrder, []string{"main"}) {
		t.Errorf("saved convention not echoed: %v / %v", snap.RepoPrefixes, snap.BranchOrder)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestMergeDevOpsSettings_AppPrefixesAndLookupLimit — v0.10.112: uygulama
// önekleri konvansiyon listesi gibi temizlenir (boşlar düşer, hiç
// kalmazsa nil = "ayar yok"); tavan 0 kalır (varsayılan demek), negatif
// 0'a, üst sınır devops.MaxCodeLookupLimit'e sıkışır.
func TestMergeDevOpsSettings_AppPrefixesAndLookupLimit(t *testing.T) {
	cases := []struct {
		name       string
		prefixes   []string
		limit      int
		wantPrefix []string
		wantLimit  int
	}{
		{"boş", nil, 0, nil, 0},
		{"temizlenir", []string{" com.banka.odeme. ", "", "com.banka.kart."}, 8, []string{"com.banka.odeme.", "com.banka.kart."}, 8},
		{"yalnız boşluk → nil", []string{"  ", ""}, 0, nil, 0},
		{"negatif → 0", nil, -3, nil, 0},
		{"tavan üstü sıkışır", nil, 999, nil, devops.MaxCodeLookupLimit},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := devopsSettingsInput{BaseURL: "https://tfs.example.local", AppPrefixes: c.prefixes, CodeLookupLimit: c.limit}
			cfg, bad := mergeDevOpsSettings(in, baseCur())
			if bad != "" {
				t.Fatalf("beklenmeyen ret: %s", bad)
			}
			if !reflect.DeepEqual(cfg.AppPrefixes, c.wantPrefix) {
				t.Errorf("AppPrefixes=%v, istenen %v", cfg.AppPrefixes, c.wantPrefix)
			}
			if cfg.CodeLookupLimit != c.wantLimit {
				t.Errorf("CodeLookupLimit=%d, istenen %d", cfg.CodeLookupLimit, c.wantLimit)
			}
		})
	}
	// Audit izi iki alanı da taşır (v0.9.830 gerekçesi: sıralamayı
	// değiştiren ayar, her admin'in kanıtını değiştirir).
	svc := devops.New()
	svc.Configure(devops.Settings{BaseURL: "https://x", AppPrefixes: []string{"com.banka."}, CodeLookupLimit: 9})
	det := string(devopsAuditDetails(svc.Snapshot()))
	if !strings.Contains(det, `"appPrefixes":["com.banka."]`) || !strings.Contains(det, `"codeLookupLimit":9`) {
		t.Errorf("audit detayı eksik: %s", det)
	}
}
