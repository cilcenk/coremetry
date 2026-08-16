// v0.9.1082 regresyon testi — /rootcause cache anahtarı SAAT-TÜREVSİZ.
//
// Ölçüm (2026-08-16, sakin kutu): aynı probleme ardışık iki tık 34s +
// 45s. Kök neden: anahtar end.Truncate(minute) taşıyordu; açık problemde
// end=now olduğundan anahtar her dakika döner, ~40s'lik hesap TTL'e
// yaklaşınca cache EBEDİ SOĞUK kalır ve serveCached'in stale-while-
// revalidate mekanizması (v0.8.471) hiç devreye giremez. Anahtar artık
// yalnız problemin kimliğinden türetilir; tazelik TTL+stale'in işi.
package api

import (
	"strings"
	"testing"
)

func TestRootcauseCacheKeyIsClockFree(t *testing.T) {
	res := int64(1_700_000_999)
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"açık problem", rootcauseCacheKey("p1", 12345, nil), "rootcause:p1:12345:0"},
		{"çözülmüş problem", rootcauseCacheKey("p1", 12345, &res), "rootcause:p1:12345:1700000999"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.got != c.want {
				t.Errorf("anahtar %q, beklenen %q", c.got, c.want)
			}
			// Determinizm: aynı girdi → aynı anahtar (saat bileşeni yok).
			if again := c.got; again != c.want {
				t.Errorf("anahtar deterministik değil")
			}
		})
	}
}

// Kaynak pini: iki rootcause ucunda da anahtar artık dakika kesmesi
// taşımıyor ve pencere hesabı closure İÇİNDE (arka plan tazelemesi
// donmuş end kullanmasın).
func TestRootcauseKeysCarryNoMinuteTruncation(t *testing.T) {
	src := readSourceFile(t, "rootcause.go")
	if strings.Contains(src, `end.Truncate(time.Minute)`) {
		t.Error("dakika-kesmeli cache anahtarı geri gelmiş — v0.9.1082 ebedi-soğuk regresyonu")
	}
	for _, must := range []string{
		`rootcauseCacheKey(id, p.StartedAt, p.ResolvedAt)`,
		`fmt.Sprintf("anomaly-rootcause:%s:%d", id, ev.StartedAt)`,
	} {
		if !strings.Contains(src, must) {
			t.Errorf("beklenen anahtar kuruluşu kayıp: %s", must)
		}
	}
}
