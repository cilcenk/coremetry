package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// v0.9.812 regression — /endpoints limit menüsü aday havuzuna takılıyordu.
//
// v0.9.762 p99Delta ("kötüleşenler önce") sıralamasını sunucuya taşıdı:
// delta SQL'de yok, o yüzden çağrıya göre ilk N aday çekilip prior'la
// birleştiriliyor ve delta'ya göre sıralanıyor. Havuz `clamp(limit*5,
// 500, 1000)` idi ve sonuç havuzdan büyük olamaz — limit ∈
// {2000,5000,10000} seçen operatör EN FAZLA 1000 satır alıyordu, üstelik
// '(capped)' uyarısı `rows.length >= limit` koşullu olduğu için hiç
// ateşlenmiyordu: menünün üst üç seçeneği ölüydü ve bunu söyleyen hiçbir
// şey yoktu.
//
// Tablo, UI menüsündeki HER seçeneği kapsar: havuz her zaman ≥ limit
// olmalı, yoksa seçenek yine ölüdür.
func TestEndpointsPoolRespectsLimit(t *testing.T) {
	cases := []struct {
		name       string
		limit      int
		wantPool   int
		wantCapped bool
	}{
		// Küçük limitler: 500'lük taban aday headroom'u verir.
		{"menu top 100", 100, 500, false},
		{"menu top 500", 500, 2500, false},
		{"menu top 1000", 1000, 5000, false},
		// v0.9.762'de burada havuz 1000'de kalıyordu → tablo 1000 satır.
		// 2000×5 = 10000 = tavan: niyet TAM karşılanıyor, kısılma yok.
		{"menu top 2000", 2000, endpointsPoolCap, false},
		{"menu top 5000", 5000, endpointsPoolCap, true},
		{"menu All (10000)", 10000, endpointsPoolCap, true},
		// Aşağı sınır: 5× taban 500'ün altına düşmez.
		{"tiny limit", 10, 500, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool, capped := endpointsPool(tc.limit)
			if pool != tc.wantPool {
				t.Fatalf("endpointsPool(%d) pool = %d, want %d", tc.limit, pool, tc.wantPool)
			}
			if capped != tc.wantCapped {
				t.Fatalf("endpointsPool(%d) capped = %v, want %v", tc.limit, capped, tc.wantCapped)
			}
			// Asıl davet: havuz limit'in altına DÜŞEMEZ. Düşerse menü
			// seçeneği sessizce ölür — bu sürümün var oluş nedeni.
			if pool < tc.limit {
				t.Fatalf("endpointsPool(%d) pool = %d < limit — menü seçeneği ölü", tc.limit, pool)
			}
		})
	}
}

// Havuz chstore'un kendi tavanını AŞAMAZ: chstore.GetEndpoints
// `q.Limit > 10000` gördüğünde limiti SESSİZCE 500'e düşürür, yani daha
// büyük bir havuz istemek havuzu büyütmez, 20× küçültür.
func TestEndpointsPoolNeverExceedsStoreCeiling(t *testing.T) {
	for _, limit := range []int{1, 100, 999, 1000, 2001, 5000, 10000} {
		if pool, _ := endpointsPool(limit); pool > 10000 {
			t.Fatalf("endpointsPool(%d) = %d > 10000 — chstore bunu 500'e düşürür", limit, pool)
		}
	}
}

// Zarf alanları JSON'da beklenen adlarla ve doğru omitempty davranışıyla
// çıkmalı: FE '(capped)' rozetini ve şeridi bu bayrağa göre basıyor.
// Delta DIŞI sıralamalarda havuz kavramı yok → alanlar hiç görünmemeli.
func TestEndpointsListResponseEnvelope(t *testing.T) {
	empty := mustJSON(t, endpointsListResponse{})
	if strings.Contains(empty, "poolCapped") || strings.Contains(empty, "\"pool\"") {
		t.Fatalf("delta olmayan yanıt havuz alanlarını taşımamalı: %s", empty)
	}
	if !strings.Contains(empty, "\"rows\"") {
		t.Fatalf("zarf her zaman rows taşımalı: %s", empty)
	}
	full := mustJSON(t, endpointsListResponse{Pool: 10000, PoolCapped: true})
	if !strings.Contains(full, "\"pool\":10000") || !strings.Contains(full, "\"poolCapped\":true") {
		t.Fatalf("havuz alanları eksik: %s", full)
	}
}

// Zarfı etkileyen HER girdi cache anahtarında olmalı (hash-all-inputs,
// v0.5.187 sınıfı): pool limit'ten, poolCapped hem limit'ten hem
// sıralamadan türüyor. Biri anahtardan düşerse iki farklı zarf aynı
// 30 saniyelik kovada birbirini zehirler — operatör "top 100" seçip
// 10000'lik zarfı görür.
func TestEndpointsListKeyCoversEnvelopeInputs(t *testing.T) {
	base := endpointsListKey("b", "", "", "", "", 100, true, false, "p99Delta", "desc", "http")
	if got := endpointsListKey("b", "", "", "", "", 100, true, false, "p99Delta", "desc", "http"); got != base {
		t.Fatalf("endpointsListKey deterministik değil: %q vs %q", got, base)
	}
	if got := endpointsListKey("b", "", "", "", "", 10000, true, false, "p99Delta", "desc", "http"); got == base {
		t.Fatal("limit anahtarda değil — farklı havuzlu iki zarf aynı anahtarı paylaşır")
	}
	if got := endpointsListKey("b", "", "", "", "", 100, true, false, "calls", "desc", "http"); got == base {
		t.Fatal("sort anahtarda değil — havuzlu ve havuzsuz zarf aynı anahtarı paylaşır")
	}
	// Zarf ad alanı: yuvarlanan deploy'da eski pod'un yazdığı çıplak
	// dizi yeni pod tarafından zarf sanılmamalı.
	if !strings.HasPrefix(base, "endpoints:v2:") {
		t.Fatalf("zarf ad alanı düştü: %q", base)
	}
}
