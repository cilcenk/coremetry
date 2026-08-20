package chstore

import (
	"encoding/json"
	"testing"
	"time"
)

// v0.9.775 — exception triyaj pencereleri operatör vidası oldu.
// Doğrulama tek kaynaktan (NormalizeExceptionTriage) geçer: API sınırı
// ve okuma yolu AYNI kuralları uygular, yoksa elle düzenlenmiş bir
// system_settings satırı basamağı sessizce ters çevirebilirdi.
func TestNormalizeExceptionTriage(t *testing.T) {
	d := DefaultExceptionTriage()

	cases := []struct {
		name string
		in   ExceptionTriageConfig
		want ExceptionTriageConfig
	}{
		{
			// Hiç kaydedilmemiş / boş JSON: her alan 0 gelir ve
			// `int` "yok" ile "açıkça 0"ı ayırt edemez. 0 burada
			// "hiçbir patlama asla P1 olmasın" demek olurdu —
			// güvenli tarafın TERSİ.
			name: "sıfır değerler → varsayılan",
			in:   ExceptionTriageConfig{},
			want: d,
		},
		{
			name: "negatif değerler → varsayılan",
			in:   ExceptionTriageConfig{P1FreshHours: -4, P2SameDayHours: -1, StaleResolveHours: -99},
			want: d,
		},
		{
			name: "kısmi PUT — yalnız P1 verilmiş",
			in:   ExceptionTriageConfig{P1FreshHours: 8},
			want: ExceptionTriageConfig{P1FreshHours: 8, P2SameDayHours: 24, StaleResolveHours: 24, BurstMinRate: 100, BurstMinTotal: 1000, P1MinOccurrences: 500},
		},
		{
			// Ters basamak: P2 penceresi P1'den darsa taze bir
			// patlama P1'i geçip doğrudan P3'e düşerdi —
			// v0.9.699'un düzelttiği uçurumun ta kendisi.
			name: "ters basamak kelepçelenir",
			in:   ExceptionTriageConfig{P1FreshHours: 12, P2SameDayHours: 4, StaleResolveHours: 24},
			want: ExceptionTriageConfig{P1FreshHours: 12, P2SameDayHours: 12, StaleResolveHours: 24, BurstMinRate: 100, BurstMinTotal: 1000, P1MinOccurrences: 500},
		},
		{
			name: "geçerli ayar aynen geçer",
			in:   ExceptionTriageConfig{P1FreshHours: 2, P2SameDayHours: 48, StaleResolveHours: 72},
			want: ExceptionTriageConfig{P1FreshHours: 2, P2SameDayHours: 48, StaleResolveHours: 72, BurstMinRate: 100, BurstMinTotal: 1000, P1MinOccurrences: 500},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeExceptionTriage(c.in); got != c.want {
				t.Fatalf("Normalize(%+v) = %+v, beklenen %+v", c.in, got, c.want)
			}
		})
	}
}

// Varsayılanlar v0.9.775 kararını taşımalı: P1 tazeliği 4 saat (problem
// tarafındaki "critical open ≥4h → P1" ile simetrik), P2 aynı-gün 24,
// bayat süpürme eski sabitle (DefaultExceptionStaleHorizon) AYNI.
func TestDefaultExceptionTriage(t *testing.T) {
	d := DefaultExceptionTriage()
	if d.P1FreshHours != 4 {
		t.Errorf("P1FreshHours = %d, beklenen 4", d.P1FreshHours)
	}
	if d.P2SameDayHours != 24 {
		t.Errorf("P2SameDayHours = %d, beklenen 24", d.P2SameDayHours)
	}
	if d.StaleHorizon() != DefaultExceptionStaleHorizon {
		t.Errorf("StaleHorizon() = %v, DefaultExceptionStaleHorizon (%v) ile ayrışmış — "+
			"vidanın varsayılanı eski sabiti birebir korumalı ki yükseltme davranışı değiştirmesin",
			d.StaleHorizon(), DefaultExceptionStaleHorizon)
	}
}

// Saat → süre çevrimi TEK kaynaktan. Birim karışması bu depoda
// tekrarlayan bir hata sınıfı (retention_test.go dersi): her pencere
// ayrı ayrı sınanır, "biri doğruysa hepsi doğrudur" varsayımı yok.
func TestExceptionTriageWindows(t *testing.T) {
	c := ExceptionTriageConfig{P1FreshHours: 4, P2SameDayHours: 24, StaleResolveHours: 72}
	if got := c.P1Window(); got != 4*time.Hour {
		t.Errorf("P1Window() = %v, beklenen 4h", got)
	}
	if got := c.P2Window(); got != 24*time.Hour {
		t.Errorf("P2Window() = %v, beklenen 24h", got)
	}
	if got := c.StaleHorizon(); got != 72*time.Hour {
		t.Errorf("StaleHorizon() = %v, beklenen 72h", got)
	}

	// Bozuk config üzerinden çağrılsa bile pencere kullanılabilir
	// çıkmalı — çağıran ayrıca Normalize etmek zorunda kalmasın.
	zero := ExceptionTriageConfig{}
	if got := zero.P1Window(); got != 4*time.Hour {
		t.Errorf("sıfır config P1Window() = %v, beklenen varsayılan 4h", got)
	}
	if got := zero.StaleHorizon(); got != DefaultExceptionStaleHorizon {
		t.Errorf("sıfır config StaleHorizon() = %v, beklenen %v", got, DefaultExceptionStaleHorizon)
	}
}

// JSON anahtarları frontend sözleşmesi — camelCase, lib/types.ts ile
// birebir. Bir yeniden adlandırma kaydedilmiş ayarı sessizce
// varsayılana düşürürdü (Unmarshal hata vermez, alan boş kalır).
func TestExceptionTriageJSONKeys(t *testing.T) {
	raw, err := json.Marshal(ExceptionTriageConfig{
		P1FreshHours: 4, P2SameDayHours: 24, StaleResolveHours: 24,
		// v0.9.1188 — patlama kapıları da tel şeklinin parçası.
		BurstMinRate: 100, BurstMinTotal: 1000, P1MinOccurrences: 500,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"p1FreshHours":4,"p2SameDayHours":24,"staleResolveHours":24,"burstMinRate":100,"burstMinTotal":1000,"p1MinOccurrences":500}`
	if string(raw) != want {
		t.Fatalf("JSON = %s, beklenen %s", raw, want)
	}

	var back ExceptionTriageConfig
	if err := json.Unmarshal([]byte(want), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back != (ExceptionTriageConfig{
		P1FreshHours: 4, P2SameDayHours: 24, StaleResolveHours: 24,
		BurstMinRate: 100, BurstMinTotal: 1000, P1MinOccurrences: 500,
	}) {
		t.Fatalf("round-trip bozuldu: %+v", back)
	}
}
