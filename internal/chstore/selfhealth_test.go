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
	"time"
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
				IngestStallMin:      d.IngestStallMin,
				SpoolMaxFiles:       d.SpoolMaxFiles,
				SpoolMaxBytes:       d.SpoolMaxBytes,
				DiskEtaDays:         d.DiskEtaDays,
				ChannelConsecFails:  d.ChannelConsecFails,
				VolumeSpikeFactor:   d.VolumeSpikeFactor,
				VolumeSpikeMinSpans: d.VolumeSpikeMinSpans,
			},
		},
		{
			name: "kısmi blob → yalnız yazılan alan geçer",
			in:   SelfHealthConfig{SpoolMaxFiles: 50_000},
			want: SelfHealthConfig{
				IngestStallMin:      d.IngestStallMin,
				SpoolMaxFiles:       50_000,
				SpoolMaxBytes:       d.SpoolMaxBytes,
				DiskEtaDays:         d.DiskEtaDays,
				ChannelConsecFails:  d.ChannelConsecFails,
				VolumeSpikeFactor:   d.VolumeSpikeFactor,
				VolumeSpikeMinSpans: d.VolumeSpikeMinSpans,
			},
		},
		{
			name: "negatif/sıfır değerler varsayılana döner",
			in: SelfHealthConfig{
				IngestStallMin: -5, DiskEtaDays: -1, ChannelConsecFails: 0,
				SpoolMaxFiles: 0, SpoolMaxBytes: 0,
				VolumeSpikeFactor: -2, VolumeSpikeMinSpans: 0,
			},
			want: SelfHealthConfig{
				IngestStallMin:      d.IngestStallMin,
				SpoolMaxFiles:       d.SpoolMaxFiles,
				SpoolMaxBytes:       d.SpoolMaxBytes,
				DiskEtaDays:         d.DiskEtaDays,
				ChannelConsecFails:  d.ChannelConsecFails,
				VolumeSpikeFactor:   d.VolumeSpikeFactor,
				VolumeSpikeMinSpans: d.VolumeSpikeMinSpans,
			},
		},
		{
			name: "tam blob AYNEN korunur",
			in: SelfHealthConfig{
				Enabled: selfHealthBoolPtr(true), IngestStallMin: 30, SpoolMaxFiles: 5,
				SpoolMaxBytes: 7, DiskEtaDays: 3.5, ChannelConsecFails: 9,
				VolumeSpikeFactor: 12, VolumeSpikeMinSpans: 4242,
			},
			want: SelfHealthConfig{
				Enabled: selfHealthBoolPtr(true), IngestStallMin: 30, SpoolMaxFiles: 5,
				SpoolMaxBytes: 7, DiskEtaDays: 3.5, ChannelConsecFails: 9,
				VolumeSpikeFactor: 12, VolumeSpikeMinSpans: 4242,
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
				got.ChannelConsecFails != tt.want.ChannelConsecFails ||
				got.VolumeSpikeFactor != tt.want.VolumeSpikeFactor ||
				got.VolumeSpikeMinSpans != tt.want.VolumeSpikeMinSpans {
				t.Fatalf("patchSelfHealth = %+v, beklenen %+v", got, tt.want)
			}
		})
	}
}

// v0.9.1294 — GERİYE UYUM: hacim vidaları aileye BEŞ kural sonra eklendi,
// yani sahadaki her kayıtlı blob onlarsız. Alanların eksik olduğu bir
// blob'un varsayılana düşmesi teorik bir incelik değil: düşmezse
// VolumeSpikeFactor 0 kalır, volumeSpikeDecision "kural kapalı" der ve
// v0.9.1279'da ayar kaydetmiş HER kurulum yeni kuralı SESSİZCE kapalı
// alır. Enabled pointer'ının kapattığı sınıfın aynısı, sayısal tarafta.
func TestSelfHealthVolumeFieldsBackfill(t *testing.T) {
	d := DefaultSelfHealth()
	// v0.9.1279'un yazdığı biçimin birebir kendisi (hacim alanları YOK).
	const legacy = `{"enabled":true,"ingestStallMin":10,"spoolMaxFiles":100000,` +
		`"spoolMaxBytes":10737418240,"diskEtaDays":7,"channelConsecFails":3}`

	var c SelfHealthConfig
	if err := json.Unmarshal([]byte(legacy), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := patchSelfHealth(c, d)
	if got.VolumeSpikeFactor != d.VolumeSpikeFactor {
		t.Fatalf("eski blob'da volumeSpikeFactor = %v, varsayılan %v bekleniyordu",
			got.VolumeSpikeFactor, d.VolumeSpikeFactor)
	}
	if got.VolumeSpikeMinSpans != d.VolumeSpikeMinSpans {
		t.Fatalf("eski blob'da volumeSpikeMinSpans = %d, varsayılan %d bekleniyordu",
			got.VolumeSpikeMinSpans, d.VolumeSpikeMinSpans)
	}
	// Eski alanlar da AYNEN geçmeli — geriye uyum tek yönlü değildir.
	if got.IngestStallMin != 10 || got.ChannelConsecFails != 3 || got.DiskEtaDays != 7 {
		t.Fatalf("eski blob'un kendi değerleri bozuldu: %+v", got)
	}
	if !got.SelfHealthOn() {
		t.Fatal("eski blob okununca aile kapandı")
	}
}

// v0.9.1294 — "P1 OLMASIN" DİREKTİFİNİN PİNİ.
//
// Öncelik yazılamaz, OKUMA ANINDA türer (severity + oran + comparator).
// Kural bu yüzden tek bir şeyi seçebiliyor: severity="warning". Bu test
// o seçimin gerçekten yettiğini ölçüyor — merdivenin warning dalı hiçbir
// oranda, hiçbir yaşta P1 üretmemeli. Üretirse maliyet uyarısı gerçek
// kesintilerin arasına karışır.
func TestVolumeSpikePriorityLadder(t *testing.T) {
	cfg := NormalizeProblemPriority(DefaultProblemPriority())
	now := time.Now().UnixNano()
	day := now - int64(24*time.Hour)
	week := now - int64(7*24*time.Hour)

	tests := []struct {
		name             string
		value, threshold float64
		startedAt        int64
		want             string
	}{
		// Eşiği yeni geçmiş sıçrama: P3 (rahat vakitte).
		{"5.7× / eşik 4 → P3", 5.714, 4, now, "P3"},
		{"tam eşik 4× / 4 → P3", 4, 4, now, "P3"},
		// Eşiğin BigBreachRatio katı: P2. Sınır vidada (varsayılan 2×),
		// kuralda değil — emitter yalnız çifti taşır.
		{"8× / eşik 4 (2.0 oran) → P2", 8, 4, now, "P2"},
		{"32× / eşik 4 → P2", 32, 4, now, "P2"},
		// YAŞ: bir gün ve bir hafta açık kalsa da P1 YOK. (Stale-critical
		// terfisi yalnız critical'a uygulanır; warning'in P1 kapısı yok.)
		{"5.7× bir gündür açık → hâlâ P3", 5.714, 4, day, "P3"},
		{"32× bir haftadır açık → hâlâ P2", 32, 4, week, "P2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Problem{
				RuleID: "self-volume-spike", Severity: "warning", Status: "open",
				Value: tt.value, Threshold: tt.threshold, StartedAt: tt.startedAt,
			}
			got, reason := computePriority(p, now, cfg)
			if got == "P1" {
				t.Fatalf("MALİYET UYARISI P1 OLDU (%s) — direktif ihlali", reason)
			}
			if got != tt.want {
				t.Fatalf("öncelik = %s (%s), beklenen %s", got, reason, tt.want)
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
		"self-ingest-stall", "self-spool-depth", "self-disk-eta",
		"self-channel-broken", "self-volume-spike",
	} {
		u, ok := selfHealthRunbooks[rule]
		if !ok || u == "" {
			t.Fatalf("%s için runbook yok", rule)
		}
		if u[0] != '/' {
			t.Fatalf("%s runbook'u ürün içi bir rota değil: %q", rule, u)
		}
	}
	if len(selfHealthRunbooks) != 5 {
		t.Fatalf("harita %d girdi taşıyor, 5 bekleniyordu (yeni kural eklendiyse runbook'u da ekleyin)", len(selfHealthRunbooks))
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
