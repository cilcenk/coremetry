package entity

import (
	"testing"
	"time"
)

// v0.10.129 — özellik bayrağı + vidalar (system_settings["entity_layer"]).
// Sözleşme: kapalı varsayılan (mevcut sayfalar etkilenmez); süreler
// kelepçeli — yanlış bir vida ne Thanos'u ne CH'yi boğar:
//   syncInterval 15s..1h (varsayılan 60s), podGap 1m..24h (10m),
//   staleAfter 1h..30d (24h), parallelClusters 1..16 (4).
// Süre alanları JSON'da Go süre dizesi ("60s", "10m") — operatör girdisi.

func TestSettingsDefaultsAndClamp(t *testing.T) {
	d := DefaultSettings()
	if d.Enabled {
		t.Fatal("bayrak varsayılan KAPALI olmalı")
	}
	if d.SyncInterval != "60s" || d.PodGap != "10m" || d.StaleAfter != "24h" || d.ParallelClusters != 4 {
		t.Fatalf("varsayılanlar: %+v", d)
	}
	r := d.Resolved()
	if r.SyncInterval != time.Minute || r.PodGap != 10*time.Minute || r.StaleAfter != 24*time.Hour || r.ParallelClusters != 4 {
		t.Fatalf("çözülmüş varsayılanlar: %+v", r)
	}
	cases := []struct {
		name string
		in   Settings
		want Resolved
	}{
		// ParallelClusters 0 = "ayarlanmadı" (omitempty JSON'da 0 ile boş ayırt edilemez) → varsayılan; alt kelepçe negatifte.
		{"alt kelepçe", Settings{SyncInterval: "1s", PodGap: "1s", StaleAfter: "1s", ParallelClusters: -3},
			Resolved{SyncInterval: 15 * time.Second, PodGap: time.Minute, StaleAfter: time.Hour, ParallelClusters: 1}},
		{"üst kelepçe", Settings{SyncInterval: "48h", PodGap: "100h", StaleAfter: "1000h", ParallelClusters: 99},
			Resolved{SyncInterval: time.Hour, PodGap: 24 * time.Hour, StaleAfter: 30 * 24 * time.Hour, ParallelClusters: 16}},
		{"bozuk dize → varsayılan", Settings{SyncInterval: "bir dakika", PodGap: "", StaleAfter: "x"},
			Resolved{SyncInterval: time.Minute, PodGap: 10 * time.Minute, StaleAfter: 24 * time.Hour, ParallelClusters: 4}},
		{"her birim", Settings{SyncInterval: "2m", PodGap: "1h", StaleAfter: "2d", ParallelClusters: 2},
			Resolved{SyncInterval: 2 * time.Minute, PodGap: time.Hour, StaleAfter: 48 * time.Hour, ParallelClusters: 2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.in.Resolved()
			got.Enabled = false
			if got != c.want {
				t.Fatalf("%+v, beklenen %+v", got, c.want)
			}
		})
	}
}

// v0.10.129 — PUT sonrası kendi reload sinyali (ya da 30 s poll) henüz
// replike olmamış ESKİ blobu okuyup bellekteki yeni değeri geri alıyordu
// (lokalde ölçüldü: PUT enabled=false → hemen GET enabled=true). Blob bir
// updatedAt taşır; LoadPersisted daha eski (ya da damgasız) bir blobu
// bellekteki daha yeni değerin ÜSTÜNE yazmaz.
func TestLoadPersistedDoesNotRegress(t *testing.T) {
	svc := NewSettingsService()
	newer := Settings{Enabled: false, UpdatedAt: 200}
	older := Settings{Enabled: true, UpdatedAt: 100}
	svc.Configure(newer)
	if svc.applyLoaded(older) {
		t.Fatal("eski damgalı blob bellekteki yeniyi ezmemeli")
	}
	if svc.Current().Enabled {
		t.Fatal("değer geri alındı")
	}
	if !svc.applyLoaded(Settings{Enabled: true, UpdatedAt: 300}) || !svc.Current().Enabled {
		t.Fatal("daha yeni blob uygulanmalı")
	}
	// Damgasız (eski sürüm) blob: bellekte damga yoksa uygulanır, varsa uygulanmaz.
	fresh := NewSettingsService()
	if !fresh.applyLoaded(Settings{Enabled: true}) || !fresh.Current().Enabled {
		t.Fatal("damgasız blob boş belleğe uygulanmalı")
	}
	if fresh.applyLoaded(Settings{Enabled: false}) {
		// damgasız → damgasız: eşit (0 < 0 değil) → uygulanır; sözleşme: eşitlikte uygula
		if fresh.Current().Enabled {
			t.Fatal("eşit damgada yeni blob uygulanmalı")
		}
	}
}
