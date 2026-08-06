package chstore

import (
	"testing"
	"strings"
)

// TestMergeTeams pins the auto-derive ownership rule (v0.8.95 fill, v0.8.100
// rename-propagation): the deriver owns a field while it's empty OR equals the
// value it last auto-wrote (*_team_auto); a human edit (value != auto) pins it.
// When owned the field tracks the derived value (rename propagates); manual
// edits and the other metadata fields are never touched.
func TestMergeTeams(t *testing.T) {
	cases := []struct {
		name                       string
		md                         ServiceMetadata
		derived                    ServiceTeams
		wantOwner, wantSRE         string
		wantOwnerAuto, wantSREAuto string
		wantChanged                bool
	}{
		{"fill both empty", ServiceMetadata{Service: "s"}, ServiceTeams{"ug", "sy"}, "ug", "sy", "ug", "sy", true},
		{"manual owner (no auto) pinned, fill sre", ServiceMetadata{Service: "s", OwnerTeam: "manual"}, ServiceTeams{"ug", "sy"}, "manual", "sy", "", "sy", true},
		{"manual both pinned, no change", ServiceMetadata{Service: "s", OwnerTeam: "mo", SRETeam: "ms"}, ServiceTeams{"ug", "sy"}, "mo", "ms", "", "", false},
		{"derived empty owner only fills sre", ServiceMetadata{Service: "s"}, ServiceTeams{"", "sy"}, "", "sy", "", "sy", true},
		{"derived both empty, no change", ServiceMetadata{Service: "s"}, ServiceTeams{"", ""}, "", "", "", "", false},
		// v0.8.100 — auto-owned field tracks a rename in the span attrs.
		{"auto-owned updates on rename", ServiceMetadata{Service: "s", OwnerTeam: "old", OwnerTeamAuto: "old", SRETeam: "sold", SRETeamAuto: "sold"}, ServiceTeams{"new", "snew"}, "new", "snew", "new", "snew", true},
		{"auto-owned same value, no change", ServiceMetadata{Service: "s", OwnerTeam: "x", OwnerTeamAuto: "x", SRETeam: "y", SRETeamAuto: "y"}, ServiceTeams{"x", "y"}, "x", "y", "x", "y", false},
		// Human edited owner away from its auto value → pinned, deriver backs off.
		{"manual edit (owner != auto) pins, sre still owned", ServiceMetadata{Service: "s", OwnerTeam: "manual", OwnerTeamAuto: "derivedX", SRETeam: "sy", SRETeamAuto: "sy"}, ServiceTeams{"derivedY", "sy2"}, "manual", "sy2", "derivedX", "sy2", true},
	}
	for _, c := range cases {
		got, changed := mergeTeams(c.md, c.derived)
		if got.OwnerTeam != c.wantOwner || got.SRETeam != c.wantSRE || changed != c.wantChanged ||
			got.OwnerTeamAuto != c.wantOwnerAuto || got.SRETeamAuto != c.wantSREAuto {
			t.Errorf("%s: got owner=%q sre=%q ownerAuto=%q sreAuto=%q changed=%v; want %q/%q/%q/%q/%v",
				c.name, got.OwnerTeam, got.SRETeam, got.OwnerTeamAuto, got.SRETeamAuto, changed,
				c.wantOwner, c.wantSRE, c.wantOwnerAuto, c.wantSREAuto, c.wantChanged)
		}
	}

	// Non-team fields must survive the merge (UpsertServiceMetadata is a
	// full-row replace, so a dropped field would clobber a manual edit).
	md := ServiceMetadata{Service: "s", Description: "keep", Repository: "repo", ChatChannel: "chan", RunbookURL: "rb"}
	got, _ := mergeTeams(md, ServiceTeams{"ug", "sy"})
	if got.Description != "keep" || got.Repository != "repo" || got.ChatChannel != "chan" || got.RunbookURL != "rb" {
		t.Errorf("mergeTeams must preserve non-team fields, got %+v", got)
	}
}

// TestMergeNamespace — v0.8.436. Ownership/pin semantics must stay
// byte-identical to mergeTeams: deriver owns empty-or-own fields,
// manual edits pin, renames propagate while owned.
func TestMergeNamespace(t *testing.T) {
	tests := []struct {
		name    string
		md      ServiceMetadata
		ns      string
		want    string
		changed bool
	}{
		{"fills empty", ServiceMetadata{}, "payments", "payments", true},
		{"rename propagates while deriver-owned",
			ServiceMetadata{Namespace: "old", NamespaceAuto: "old"}, "payments", "payments", true},
		{"manual edit pins (value != auto)",
			ServiceMetadata{Namespace: "curated", NamespaceAuto: "old"}, "payments", "curated", false},
		{"same value is a no-op",
			ServiceMetadata{Namespace: "payments", NamespaceAuto: "payments"}, "payments", "payments", false},
		{"empty derive never clears",
			ServiceMetadata{Namespace: "payments", NamespaceAuto: "payments"}, "", "payments", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := mergeNamespace(tc.md, tc.ns)
			if got.Namespace != tc.want || changed != tc.changed {
				t.Fatalf("ns=%q changed=%v, want %q/%v", got.Namespace, changed, tc.want, tc.changed)
			}
			if changed && got.NamespaceAuto != tc.ns {
				t.Fatalf("provenance not re-stamped: %q", got.NamespaceAuto)
			}
		})
	}
}

// TestDeriveNamespaceSQLShape — house bounds + the two attribute
// spellings in preference order (resource scope before span scope).
func TestDeriveNamespaceSQLShape(t *testing.T) {
	for _, frag := range []string{
		"FROM spans",
		"time >= ? AND time <= ?",
		"LIMIT 2000000",
		// v0.9.715 sonrası birleşik SQL: dış LIMIT kombo tablosuna (50000)
		"LIMIT 50000",
		"SETTINGS max_execution_time = 25",
		"has(res_keys, 'service.namespace')",
		"has(res_keys, 'k8s.namespace.name')",
		"has(attr_keys, 'service.namespace')",
		"has(attr_keys, 'k8s.namespace.name')",
	} {
		if !strings.Contains(deriveMetadataAllSQL, frag) {
			t.Errorf("missing %q", frag)
		}
	}
	// resource spelling must be checked BEFORE the span-scope fallback
	// (multiIf order is the preference order).
	if strings.Index(deriveMetadataAllSQL, "has(res_keys, 'service.namespace')") >
		strings.Index(deriveMetadataAllSQL, "has(attr_keys, 'service.namespace')") {
		t.Fatal("resource scope must precede span scope in the multiIf")
	}
}

// TestNamespaceAutoSurvivesUnrelatedEdit — v0.8.438 regression.
// Review-confirmed: putServiceMetadata copied only the team *_auto
// provenance; namespace_auto zeroed on ANY catalog edit, so
// mergeNamespace treated the derived value as a human pin forever.
// The pure invariant pinned here: a row whose Namespace equals its
// (correctly carried) NamespaceAuto stays deriver-owned; the same row
// with a ZEROED auto is treated as pinned — the bug's exact mechanism.
func TestNamespaceAutoSurvivesUnrelatedEdit(t *testing.T) {
	derived := ServiceMetadata{Namespace: "payments", NamespaceAuto: "payments"}
	if got, changed := mergeNamespace(derived, "payments-v2"); !changed || got.Namespace != "payments-v2" {
		t.Fatalf("carried auto must stay deriver-owned: %+v changed=%v", got, changed)
	}
	zeroedAuto := ServiceMetadata{Namespace: "payments", NamespaceAuto: ""}
	if _, changed := mergeNamespace(zeroedAuto, "payments-v2"); changed {
		t.Fatal("zeroed auto reads as a human pin — the handler MUST carry NamespaceAuto forward")
	}
}

// v0.9.531 — operatör-bildirimli (prod): BFF servislerinde k8s
// deployment adı servis adındaki env ekini taşımıyor (servis
// mobile-loans-bff-prod, pod mobile-loans-bff-6b8f49b9d5-8hrtj).
// Katalog deriver'ı yalnız deployment.name attr'larını okuyordu; BFF o
// attr'ı basmadığı için deployment_auto boş kalıyor ve Pods /
// Infrastructure sekmeleri "No pods matched" gösteriyordu — CPU/Memory
// grafikleri dahil (PromQL seçicisi de servis adına düşüyordu).
//
// Çözüm: gözlemlenen pod adlarından türetim. Bu test türetimin
// MUHAFAZAKÂRLIK sınırlarını pinler — katalog alanı yazan bir sezgisel,
// görüntüleme sezgiselinden (thanos stripPodSuffixes) daha sıkı olmak
// ZORUNDA: yanlış deployment adı, yanlış pod'ları servise bağlar.
func TestDeploymentFromPodName(t *testing.T) {
	cases := []struct{ name, pod, want string }{
		// Asıl hedef: Deployment şekli (rand5 + rs-hash birlikte).
		{"BFF prod pod'u", "mobile-loans-bff-6b8f49b9d5-8hrtj", "mobile-loans-bff"},
		{"BSA pod'u (env ekli deployment)", "bsa-digital-limitcore-prod-864cd95d87-q9dt9", "bsa-digital-limitcore-prod"},
		{"oneagent yan pod'u ayrı aday üretir", "bsa-digital-limitcore-prod-oneagent-7d98d8b99d-m6r8f", "bsa-digital-limitcore-prod-oneagent"},

		// StatefulSet: kısa sayısal ordinal.
		{"statefulset-0", "chc-0", "chc"},
		{"statefulset çok haneli", "kafka-broker-12", "kafka-broker"},

		// Emin olunamayanlar → "". Her biri gerçek bir yanlış-pozitif
		// sınıfı: katalog alanına girseydi yanlış pod eşleşmesi doğardı.
		{"DaemonSet şekli hostname'le ayırt edilemez", "node-exporter-x8k2p", ""},
		{"hostname rand5'e benzeyen kuyrukla", "vm-app01", ""},
		{"rs-hash'siz rand5", "mobile-loans-bff-8hrtj", ""},
		{"rand5 sesli harf içeriyor (k8s alfabesi dışı)", "svc-6b8f49b9d5-abcde", ""},
		{"UUID instance id", "550e8400-e29b-41d4-a716-446655440000", ""},
		{"uzun sayısal kuyruk ordinal değil", "batch-20260802", ""},
		{"tek segment", "localhost", ""},
		{"boş", "", ""},
		// rs-hash sınırları: 8-10 hex.
		{"rs-hash 7 karakter (kısa)", "svc-abc1234-8hrtj", ""},
		{"rs-hash 11 karakter (uzun)", "svc-abc1234def5-8hrtj", ""},
		{"rs-hash hex dışı", "svc-abcdefgh-8hrtj", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deploymentFromPodName(c.pod); got != c.want {
				t.Errorf("deploymentFromPodName(%q) = %q, beklenen %q", c.pod, got, c.want)
			}
		})
	}
}

// Baskın aday seçimi: yan pod'lar (oneagent) ana deployment'ı ezmemeli,
// eşitlikte seçim deterministik olmalı (map sırası rastgele).
func TestDeploymentsFromPodCounts(t *testing.T) {
	got := deploymentsFromPodCounts(map[string]map[string]uint64{
		"mobile-loans-bff-prod": {
			"mobile-loans-bff-6b8f49b9d5-8hrtj": 900,
			"mobile-loans-bff-6b8f49b9d5-vdp54": 850,
			"mobile-loans-bff-oneagent-7d98d8b99d-m6r8f": 40,
		},
		"uuid-only-svc": {
			"550e8400-e29b-41d4-a716-446655440000": 1000,
		},
		"tie-svc": {
			"aaa-6b8f49b9d5-8hrtj": 10,
			"bbb-6b8f49b9d5-8hrtj": 10,
		},
	})
	if got["mobile-loans-bff-prod"] != "mobile-loans-bff" {
		t.Errorf("baskın aday mobile-loans-bff olmalı, got %q", got["mobile-loans-bff-prod"])
	}
	if _, ok := got["uuid-only-svc"]; ok {
		t.Error("hiç güvenli aday yoksa servis haritada OLMAMALI")
	}
	if got["tie-svc"] != "aaa" {
		t.Errorf("eşitlikte alfabetik ilk seçilmeli (deterministik), got %q", got["tie-svc"])
	}
}
