package chstore

import (
	"strings"
	"testing"
)

// v0.9.45 — operator-reported: /services listesinde rozetlerin çoğu
// görünmüyordu. İki kök neden pinlenir:
//
//  1. runtimeAttrCond, badge'in render şartını oluşturan ÜÇ attribute'un
//     üçünü de içermeli (formatRuntime: language > runtime name > SDK
//     version). Koşuldan biri düşerse o alanla render olan rozetler
//     için "attribute'lu son span" seçimi bozulur ve karışık-resource
//     servislerde rozet piyangosu geri gelir.
//  2. Batch ve tekil sorgular AYNI koşul sabitini kullanmalı — iki yol
//     ayrışırsa /services listesi ile servis detayı farklı rozet
//     gösterir (aynı bug'ın sinsi hali).
func TestRuntimeAttrCondCoversBadgeFields(t *testing.T) {
	for _, key := range []string{
		"telemetry.sdk.language",
		"process.runtime.name",
		"telemetry.sdk.version",
	} {
		if !strings.Contains(runtimeAttrCond, "'"+key+"'") {
			t.Errorf("runtimeAttrCond %q anahtarını kapsamıyor — rozet render şartıyla ayrıştı", key)
		}
	}
	if !strings.Contains(runtimeAttrCond, "has(res_keys,") {
		t.Error("runtimeAttrCond has(res_keys, …) formunda değil")
	}
}
