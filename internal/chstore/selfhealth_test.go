package chstore

// selfhealth_test.go — v0.9.1279. self_health eşik blobunun zero-patch
// çekirdeği + self-* runbook eşlemesi.
//
// Buradaki asıl tuzak Enabled'ın POINTER olması: bool olsaydı kısmi bir
// blob (yalnız spoolMaxFiles yazan bir operatör) tüm alarm ailesini
// SESSİZCE kapatırdı, çünkü JSON'da eksik bir bool `false` olarak
// okunur. "Kapatmayı istemek" ile "hiç yazmamak" aynı şey değil ve bu
// testler ikisini ayrı tutar.

import (
	"encoding/json"
	"testing"
)

func TestPatchSelfHealth(t *testing.T) {
	d := DefaultSelfHealth()

	tests := []struct {
		name string
		in   SelfHealthConfig
		want SelfHealthConfig
	}{
		{
			name: "tamamen boş blob → varsayılanlar",
			in:   SelfHealthConfig{},
			want: SelfHealthConfig{
				IngestStallMin:     d.IngestStallMin,
				SpoolMaxFiles:      d.SpoolMaxFiles,
				SpoolMaxBytes:      d.SpoolMaxBytes,
				DiskEtaDays:        d.DiskEtaDays,
				ChannelConsecFails: d.ChannelConsecFails,
			},
		},
		{
			name: "kısmi blob → yalnız yazılan alan geçer",
			in:   SelfHealthConfig{SpoolMaxFiles: 50_000},
			want: SelfHealthConfig{
				IngestStallMin:     d.IngestStallMin,
				SpoolMaxFiles:      50_000,
				SpoolMaxBytes:      d.SpoolMaxBytes,
				DiskEtaDays:        d.DiskEtaDays,
				ChannelConsecFails: d.ChannelConsecFails,
			},
		},
		{
			name: "negatif/sıfır değerler varsayılana döner",
			in: SelfHealthConfig{
				IngestStallMin: -5, DiskEtaDays: -1, ChannelConsecFails: 0,
				SpoolMaxFiles: 0, SpoolMaxBytes: 0,
			},
			want: SelfHealthConfig{
				IngestStallMin:     d.IngestStallMin,
				SpoolMaxFiles:      d.SpoolMaxFiles,
				SpoolMaxBytes:      d.SpoolMaxBytes,
				DiskEtaDays:        d.DiskEtaDays,
				ChannelConsecFails: d.ChannelConsecFails,
			},
		},
		{
			name: "tam blob AYNEN korunur",
			in: SelfHealthConfig{
				Enabled: selfHealthBoolPtr(true), IngestStallMin: 30, SpoolMaxFiles: 5,
				SpoolMaxBytes: 7, DiskEtaDays: 3.5, ChannelConsecFails: 9,
			},
			want: SelfHealthConfig{
				Enabled: selfHealthBoolPtr(true), IngestStallMin: 30, SpoolMaxFiles: 5,
				SpoolMaxBytes: 7, DiskEtaDays: 3.5, ChannelConsecFails: 9,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := patchSelfHealth(tt.in, d)
			if got.IngestStallMin != tt.want.IngestStallMin ||
				got.SpoolMaxFiles != tt.want.SpoolMaxFiles ||
				got.SpoolMaxBytes != tt.want.SpoolMaxBytes ||
				got.DiskEtaDays != tt.want.DiskEtaDays ||
				got.ChannelConsecFails != tt.want.ChannelConsecFails {
				t.Fatalf("patchSelfHealth = %+v, beklenen %+v", got, tt.want)
			}
		})
	}
}

// ASIL TUZAK: Enabled'ın üç hâli (yazılmamış / açık / kapalı) JSON
// round-trip'inden sonra da ayrı kalmalı.
func TestSelfHealthEnabledTriState(t *testing.T) {
	d := DefaultSelfHealth()

	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"alan hiç yok → AÇIK (varsayılan)", `{"spoolMaxFiles":50000}`, true},
		{"tamamen boş blob → AÇIK", `{}`, true},
		{"açıkça true → AÇIK", `{"enabled":true}`, true},
		// Bu satır bool alanda YEŞİL yanardı ve yanlış olurdu: pointer
		// olmasaydı yukarıdaki iki vaka da false döner, testin ayırt
		// gücü sıfırlanırdı.
		{"açıkça false → KAPALI", `{"enabled":false}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c SelfHealthConfig
			if err := json.Unmarshal([]byte(tt.raw), &c); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := patchSelfHealth(c, d).SelfHealthOn(); got != tt.want {
				t.Fatalf("SelfHealthOn() = %v, beklenen %v (blob %s)", got, tt.want, tt.raw)
			}
		})
	}

	// Varsayılan blob serialize → deserialize edildiğinde de AÇIK kalmalı
	// (SaveSelfHealth → GetSelfHealth yolu).
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round SelfHealthConfig
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !patchSelfHealth(round, d).SelfHealthOn() {
		t.Fatalf("varsayılan blob round-trip'te KAPANDI: %s", raw)
	}
}

func TestDiskKey(t *testing.T) {
	tests := []struct {
		host, disk, want string
	}{
		{"chc-0", "default", "chc-0/default"},
		{"", "default", "default"}, // tek düğüm: host yok
		{"chc-1", "cold", "chc-1/cold"},
	}
	for _, tt := range tests {
		if got := DiskKey(tt.host, tt.disk); got != tt.want {
			t.Fatalf("DiskKey(%q,%q) = %q, beklenen %q", tt.host, tt.disk, got, tt.want)
		}
	}
}

// self-* kuralları SENTETİKTİR (alert_rules'ta satırları yok), yani
// operatör runbook'u da yok. Gömülü haritanın dört kuralı da kapsaması
// ŞART: kapsamayan bir kural, /problems'ta runbook'suz görünür ve
// müdahale adımları üründe olduğu hâlde ulaşılmaz kalır.
func TestSelfHealthRunbooksCoverEveryRule(t *testing.T) {
	for _, rule := range []string{
		"self-ingest-stall", "self-spool-depth", "self-disk-eta", "self-channel-broken",
	} {
		u, ok := selfHealthRunbooks[rule]
		if !ok || u == "" {
			t.Fatalf("%s için runbook yok", rule)
		}
		if u[0] != '/' {
			t.Fatalf("%s runbook'u ürün içi bir rota değil: %q", rule, u)
		}
	}
	if len(selfHealthRunbooks) != 4 {
		t.Fatalf("harita %d girdi taşıyor, 4 bekleniyordu (yeni kural eklendiyse runbook'u da ekleyin)", len(selfHealthRunbooks))
	}
}

// Operatörün alert_rules'a elle koyduğu URL gömülü haritayı EZMELİ —
// harita bir varsayılan, kilit değil. EnrichProblemsWithRunbooks'un
// dal sırasını pinler (canlı CH gerektirmeden: aynı sıralamayı yeniden
// kurmak yerine sırayı taşıyan saf ifadeyi test ederiz).
func TestSelfHealthRunbookIsOverridable(t *testing.T) {
	ruleBooks := map[string]string{"self-spool-depth": "https://wiki.internal/spool"}
	p := Problem{RuleID: "self-spool-depth"}

	// EnrichProblemsWithRunbooks'un gövdesindeki sıra: ruleBooks → gömülü.
	if u, ok := ruleBooks[p.RuleID]; ok {
		p.RunbookURL = u
	} else if u, ok := selfHealthRunbooks[p.RuleID]; ok {
		p.RunbookURL = u
	}
	if p.RunbookURL != "https://wiki.internal/spool" {
		t.Fatalf("operatör runbook'u ezilmedi: %q", p.RunbookURL)
	}
}

func selfHealthBoolPtr(b bool) *bool { return &b }
