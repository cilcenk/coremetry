package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.10.129 — Operator-reported: "modeli Settings'ten kaydediyorum, yine
// eski modele dönüyor" (CoSRE → Model, prod + test ortamı).
//
// Kök neden (lokalde ölçüldü): system_settings ReplicatedReplacingMergeTree;
// PutSetting yalnız (key, value) yazıyordu, version/updated_at sunucu
// DEFAULT'uydu → blok özeti yalnız (key,value)'dan çıkıyor. Aynı blob daha
// önce yazıldıysa (A → B → A) Replicated INSERT dedup'u ikinci A'yı SESSİZCE
// düşürüyor (iki özdeş INSERT → 1 satır); FINAL en yüksek version'lı B'yi
// döndürüyor, 30 s poll / reload sinyali bellekteki A'yı B'ye geri alıyor.
//
// Düzeltme: version + updated_at İSTEMCİ damgasıyla açık yazılır — her blok
// benzersiz, dedup tetiklenmez. Tek yer: tüm ayar blobları (CoSRE, Thanos,
// entity, tempo, …) aynı yoldan geçer.

func TestSettingsInsertCarriesExplicitVersion(t *testing.T) {
	for _, col := range []string{"key", "value", "updated_at", "version"} {
		if !strings.Contains(settingsInsertSQL, col) {
			t.Fatalf("PutSetting INSERT'i %q kolonunu açıkça yazmalı (dedup kalkanı): %s", col, settingsInsertSQL)
		}
	}
}

func TestSettingsVersionIsUniquePerCall(t *testing.T) {
	t0 := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	a := settingsVersion(t0)
	b := settingsVersion(t0.Add(time.Nanosecond))
	if a == b || b <= a {
		t.Fatalf("ardışık çağrılar artan benzersiz version vermeli: %d %d", a, b)
	}
	if a != uint64(t0.UnixNano()) {
		t.Fatalf("version = UnixNano (sunucu DEFAULT'uyla aynı birim): %d", a)
	}
}
