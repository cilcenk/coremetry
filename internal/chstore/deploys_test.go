package chstore

import (
	"strings"
	"testing"
	"time"
	"os"
)

// v0.9.205 — Operator-reported: image-tag'siz bir serviste (zincir
// service.version'a düşer; "2.1.6.Final-redhat-00001" gibi SABİT bir
// artifact sürümü) Service Overview her pencerenin SOL KENARINA sahte
// bir deploy işaretçisi basıyordu — pencere-içi min(time) "deploy"
// sayılıyordu. Sözleşme: tarama pencere-öncesi lookback'i kapsar ve
// HAVING yalnız pencere İÇİNDE başlayan sürümleri geçirir. Bu test o
// iki parçayı pinler; kaldıranı yakalar.
func TestServiceDeploysSQLWindowStartContract(t *testing.T) {
	if !strings.Contains(serviceDeploysSQL, "first_seen_ns >= ?") {
		t.Fatal("serviceDeploysSQL lost the HAVING first_seen_ns >= ? window-start bound — constant-version services will fake a deploy at every window's left edge again")
	}
	// HAVING, GROUP BY'dan sonra gelmeli (pencere-başı filtresi aggregate üstünde).
	g := strings.Index(serviceDeploysSQL, "GROUP BY version")
	h := strings.Index(serviceDeploysSQL, "first_seen_ns >= ?")
	if g < 0 || h < g {
		t.Fatal("window-start bound must live in the HAVING clause after GROUP BY")
	}
	if deployLookback < 24*time.Hour {
		t.Fatalf("deployLookback=%v — 24h altına inerse günlük batch servislerinin sessiz aralıkları sabit sürümü yine 'yeni' gösterir", deployLookback)
	}
}

// ── v0.9.451 — sahte restart (operator-reported) ─────────────────────
//
// /deploys web-fund-bff-pilot için 08:30'da "restart +1/-1 (aktif 2)"
// gösterdi; OpenShift'te 4 pod da 21 Mayıs'tan beri 0 restart'la
// Running'di. Sessiz pilot pod (≈0.002 core) 10+ dk span üretmeyince
// tek-bucket yumuşatma yetmiyor: susan pod "gitti", yeniden konuşan
// "geldi" okunuyor. Guard: eklenen+giden pod'lar AYNI ReplicaSet
// template-hash'indeyse (şablon değişmemiş) restart BASTIRILIR; sürüm
// değişimi (deploy kanıtı) guard'ı atlar; hash çıkarılamayan adlarda
// guard devre dışı.
func TestPodTemplateHash(t *testing.T) {
	cases := []struct {
		pod, want string
	}{
		{"web-fund-bff-85786bbdb5-6t4x4", "85786bbdb5"},
		{"web-fund-bff-85786bbdb5-wtdbw", "85786bbdb5"},
		{"app-5-abc12", "5"},          // DeploymentConfig: deploy numarası
		{"web-fund-bff-0", ""},        // StatefulSet ordinal: son segment 5 kr değil
		{"vm-host-01", ""},            // host adı şekli
		{"a29474161", ""},             // instance.id ilk-8 (tiresiz)
		{"x-y", ""},                   // segment yetersiz
		{"svc-ABCDE-fghij", ""},       // hash büyük harf → k8s şekli değil
	}
	for _, tc := range cases {
		if got := podTemplateHash(tc.pod); got != tc.want {
			t.Errorf("podTemplateHash(%q) = %q, want %q", tc.pod, got, tc.want)
		}
	}
}

func TestAnalyzeRolloutsFlickerSuppressed(t *testing.T) {
	t0 := time.Unix(1_753_900_800, 0)
	mk := func(min int, pods []string, ver string) rolloutBucket {
		return rolloutBucket{t: t0.Add(time.Duration(min) * time.Minute), pods: pods, version: ver}
	}
	const h = "85786bbdb5"
	pa, pb, pc := "svc-"+h+"-aaaaa", "svc-"+h+"-bbbbb", "svc-"+h+"-ccccc"

	t.Run("aynı hash +1/-1 flıker → bastırılır", func(t *testing.T) {
		res := analyzeRollouts("svc", []rolloutBucket{
			mk(0, []string{pa, pb}, "1.0"),
			mk(5, []string{pa, pb}, "1.0"),
			mk(10, []string{pa}, "1.0"),  // pb sustu
			mk(15, []string{pa}, "1.0"),  // hâlâ sessiz (smoothing aşıldı)
			mk(20, []string{pa, pc}, "1.0"), // pc konuştu, pb yok → +1/-1
			mk(25, []string{pa, pc}, "1.0"),
		})
		if len(res.Rollouts) != 0 {
			t.Fatalf("flıker restart üretti: %+v", res.Rollouts)
		}
	})

	t.Run("gerçek cutover: yeni RS hash → restart kalır", func(t *testing.T) {
		na, nb := "svc-ff00aa11bb-aaaaa", "svc-ff00aa11bb-bbbbb"
		res := analyzeRollouts("svc", []rolloutBucket{
			mk(0, []string{pa, pb}, ""),
			mk(5, []string{pa, pb}, ""),
			mk(10, []string{pa, pb}, ""),
			mk(15, []string{na, nb}, ""),
			mk(20, []string{na, nb}, ""),
			mk(25, []string{na, nb}, ""),
		})
		if len(res.Rollouts) != 1 || res.Rollouts[0].Kind != "restart" {
			t.Fatalf("gerçek cutover kayboldu: %+v", res.Rollouts)
		}
	})

	t.Run("sürüm değişimi aynı hash'te bile deploy kalır", func(t *testing.T) {
		res := analyzeRollouts("svc", []rolloutBucket{
			mk(0, []string{pa, pb}, "1.0"),
			mk(5, []string{pa, pb}, "1.0"),
			mk(10, []string{pa}, "1.0"),
			mk(15, []string{pa}, "1.0"),
			mk(20, []string{pa, pc}, "2.0"),
			mk(25, []string{pa, pc}, "2.0"),
		})
		if len(res.Rollouts) != 1 || res.Rollouts[0].Kind != "deploy" {
			t.Fatalf("sürüm-değişimli geçiş deploy olmalıydı: %+v", res.Rollouts)
		}
	})

	t.Run("hash çıkarılamayan adlar (host) → guard devre dışı, eski davranış", func(t *testing.T) {
		res := analyzeRollouts("svc", []rolloutBucket{
			mk(0, []string{"vmhost01", "vmhost02"}, ""),
			mk(5, []string{"vmhost01", "vmhost02"}, ""),
			mk(10, []string{"vmhost01"}, ""),
			mk(15, []string{"vmhost01"}, ""),
			mk(20, []string{"vmhost01", "vmhost03"}, ""),
			mk(25, []string{"vmhost01", "vmhost03"}, ""),
		})
		if len(res.Rollouts) != 1 {
			t.Fatalf("hash'siz adlarda tespit kaybolmamalı: %+v", res.Rollouts)
		}
	})
}

// v0.9.478 (operator-reported, prod: "15 dakikalık pencerede tüm filo
// deploy görünüyor — mevcut sürümleri deploy sanıyor") — tarama penceresi
// hüküm penceresiyle AYNI olunca min(time) her aktif sürüm için pencere
// içindeydi ve HAVING first_seen >= from TOTOLOJİYDİ. Pinler: tarama
// 24h geriden başlar (scanFrom), hüküm sınırı from'da kalır ve span_count
// yalnız pencere içini sayar (countIf) — lookback hacmi şişirmesin.
func TestDeployWindowJudgmentLookback(t *testing.T) {
	b, err := os.ReadFile("deploys.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "const deployJudgmentLookback = 24 * time.Hour") {
		t.Error("deployJudgmentLookback yok — hüküm ufku olmadan HAVING first_seen >= from totolojidir (her aktif sürüm 'deploy' görünür)")
	}
	if !strings.Contains(src, "scanFrom := from.Add(-deployJudgmentLookback)") {
		t.Error("tarama penceresi geriye açılmıyor — pencere-içi min(time) her zaman >= from")
	}
	if !strings.Contains(src, "countIf(time >= ?) AS span_count") {
		t.Error("span_count lookback'i sayıyor — hacim kolonu yalnız sayfa penceresini saymalı")
	}
	i := strings.Index(src, "func (s *Store) GetDeploysInWindow")
	if i < 0 {
		t.Fatal("GetDeploysInWindow yok")
	}
	// v0.9.508 — erişimci s.conn → s.telemetryReadConn() oldu (deploys.go
	// RoundRobin okuma havuzuna taşındı). Bu testin pinlediği şey BAĞLANTI
	// değil BIND SIRASI; sıra birebir aynı kaldı, yalnız beklenen metin
	// yeni erişimciye güncellendi.
	if !strings.Contains(src[i:], "Query(ctx, sql, from, scanFrom, to, from.UnixNano(), limit)") {
		t.Error("bind sırası bozuk — countIf(from), WHERE(scanFrom,to), HAVING(from) sırası şart")
	}
}
