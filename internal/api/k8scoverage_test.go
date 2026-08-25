package api

import (
	"os"
	"strings"
	"testing"
)

// v0.10.36 — K8s entity katmanı Faz 0: bağlam kapsama kartı.
//
// Kart, entity katmanının asıl adımının (k8sattributes + RBAC) KABUL
// TESTİ olacak: prod'da collector restart'ı gerektiren o değişiklikten
// önce ve sonra aynı tablo. Bu yüzden kartın kendi ölçümü yanlışsa,
// ölçmek için var olduğu şeyi bozar.

func TestSnapK8sCoverageRange(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   int64
		want int64
	}{
		{"boş → 1 saat", 0, 3600},
		{"negatif → 1 saat", -5, 3600},
		// ⚠ ÜSTE yuvarlama. Alta yuvarlamak operatörün istediğinden DAR
		// bir pencere ölçmek olurdu ve seyrek yayan bir servis örneklemden
		// düşerdi — yani kartın yanlış "alan yok" demesi.
		{"basamak altı → üste", 300, 900},
		{"tam basamak", 900, 900},
		{"900 üstü → 3600", 901, 3600},
		{"3600 üstü → 21600", 3601, 21600},
		{"21600 üstü → 86400", 21601, 86400},
		{"tavanın üstü → tavan", 999999, 86400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := snapK8sCoverageRange(tc.in); got != tc.want {
				t.Errorf("snapK8sCoverageRange(%d) = %d; want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestSnapNeverNarrows — sözleşmenin tek cümlesi.
//
// Basamak HİÇBİR girdide istenen pencereden DAR olamaz. Daralırsa kart
// ölçmediği bir aralık için "alan yok" der.
func TestSnapNeverNarrows(t *testing.T) {
	for s := int64(1); s <= 86400; s += 137 {
		if got := snapK8sCoverageRange(s); got < s {
			t.Fatalf("istenen %ds için basamak %ds — DAHA DAR, kart ölçmediğini 'yok' sanar", s, got)
		}
	}
}

// TestCoverageKeyCarriesEveryInput — cache anahtarı sözleşmesi.
//
// Basamak kaldırılırsa serbest saniye değeri anahtar kardinalitesini
// patlatır ve cache HİÇ tutmaz (v0.8.270 / v0.10.16 sınıfı).
func TestCoverageKeyCarriesEveryInput(t *testing.T) {
	b, err := os.ReadFile("k8scoverage.go")
	if err != nil {
		t.Fatalf("k8scoverage.go okunamadı: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "sec = snapK8sCoverageRange(sec)") {
		t.Error("pencere basamağa oturtulmuyor — her tık yeni cache anahtarı, cache ölü kalır")
	}
	if !strings.Contains(src, `"k8s-coverage:v1:r=%d:l=%d"`) {
		t.Error("cache anahtarı TÜM girdileri taşımıyor (pencere + limit)")
	}
	// Uç yalnız admin: filo genelinde servis adı listeliyor.
	if !strings.Contains(src, "auth.RequireRole(auth.RoleAdmin, s.getK8sCoverage)") {
		t.Error("uç admin kapısı taşımıyor")
	}
	// /api-route sözleşmesi: kendi dosyasında register fonksiyonu.
	if !strings.Contains(src, "func (s *Server) registerK8sCoverageRoutes(") {
		t.Error("rota kaydı kendi dosyasında değil (/api-route sözleşmesi)")
	}
}

// TestCoverageQueryIsSampledAndBounded — CLAUDE.md sert kısıtı.
//
// Ham `spans` okuyan her sorgu LIMIT + max_execution_time + zaman-sınırlı
// WHERE taşımak zorunda. Örnekleme ayrıca v0.7.30 dersi: milyar-span
// ölçeğinde pencere geneli arrayJoin zaman aşımına uğrar ve HATA döner —
// yüzey de sessizce boş kalır.
func TestCoverageQueryIsSampledAndBounded(t *testing.T) {
	b, err := os.ReadFile("../chstore/k8s_coverage.go")
	if err != nil {
		t.Fatalf("k8s_coverage.go okunamadı: %v", err)
	}
	src := string(b)
	for _, must := range []string{
		"LIMIT %d",                      // iç örneklem
		"max_execution_time = 25",       // tavan
		"WHERE time >= ? AND time <= ?", // zaman sınırı
	} {
		if !strings.Contains(src, must) {
			t.Errorf("ham spans sorgusunda eksik: %s", must)
		}
	}
	// Örneklem boyu ZARFTA olmalı: "0 gördüm" ile "örneklem yetmedi"
	// ayrımı operatörün elinde kalmalı.
	if !strings.Contains(src, "SampleRows: k8sCoverageSampleRows") {
		t.Error("örneklem tavanı zarfta taşınmıyor — operatör ölçümün sınırını göremez")
	}
	// Kimlik alanları RESOURCE ekseninden okunuyor: bu depoda k8s bağlamı
	// attr_keys'te DEĞİL (ölçüldü).
	if !strings.Contains(src, "has(res_keys, 'k8s.pod.uid')") {
		t.Error("uid resource ekseninden okunmuyor")
	}
}
