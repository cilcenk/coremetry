package chstore

import (
	"os"
	"strings"
	"testing"
)

// sample_bias_test.go — v0.10.56. ÖNEK ÖRNEKLEMESİ ÖRNEKLEM DEĞİLDİR.
//
// ── ÖLÇÜLDÜ ─────────────────────────────────────────────────────────────
//
// `spans` birincil anahtarı (service_name, time). ORDER BY'sız bir LIMIT
// o anahtarın ÖNEKİNİ döndürür, yani ALFABETİK OLARAK İLK servisleri.
// Canlı ClickHouse'da ölçüldü (100 servislik pencere, 5.000 satır tavan):
//
//	önek örneklemesi   →   5 / 100 servis  (%5)
//	LIMIT n BY servis  → 100 / 100 servis
//
// K8s kapsama kartının BÜTÜN amacı "filonun hangi kısmı k8s bağlamı
// yayıyor" — önek örneklemesiyle cevap, alfabetik ilk beş servisten
// üretilmiş bir FİLO İDDİASI oluyor. Kartın kendi uyarısı ("örneklem")
// bunu KARŞILAMIYOR: operatör örneklemi rastgele sanıyor.
//
// ── İKİNCİ KUSUR: SÜZGEÇ LIMIT'TEN SONRA ────────────────────────────────
//
// Pod envanterinde `pod != ''` DIŞ sorgudaydı: tavan, pod adı TAŞIMAYAN
// satırlara da harcanıyordu. Filonun bir kısmı k8s bağlamı yaymıyorsa —
// ki bu kartın ölçtüğü şeyin ta kendisi — tavan onlarla dolup envanter
// BOŞ dönebiliyordu.
//
// Bu, deponun "LIMIT'ten sonra süzme" ailesinin (v0.9.322→343) SQL
// biçimi. audit.sh CHECK 8 yalnız Go biçimini tarıyor (store Limit: +
// Go döngüsünde eleme) — kapı kaçırmadı, kapsamı dışındaydı. Bu test o
// boşluğun SQL yarısını, bilinen iki örnek üzerinde kapatıyor.

// samplingQueries — ham `spans` üstünde ÖRNEKLEMELİ okuma yapan yerler.
var samplingQueries = []struct {
	file string
	// perServiceConst — servis başına kotayı taşıyan sabitin adı.
	perServiceConst string
}{
	{"k8s_coverage.go", "k8sCoveragePerService"},
	{"pod_inventory.go", "podInventoryPerService"},
}

func TestSamplesAreSpreadAcrossServices(t *testing.T) {
	for _, q := range samplingQueries {
		t.Run(q.file, func(t *testing.T) {
			src := flatWSCH(readCHSource(t, q.file))
			if !strings.Contains(src, "LIMIT %d BY service_name") {
				t.Errorf("%s örneklemi servisler arasına YAYMIYOR — ORDER BY'sız "+
					"LIMIT (service_name, time) anahtarının ÖNEKİNİ verir, yani "+
					"alfabetik ilk servisleri; kart filo iddiasını onlardan üretir "+
					"(ölçüldü: 5/100 servis)", q.file)
			}
			if !strings.Contains(src, "const "+q.perServiceConst) {
				t.Errorf("%s servis-başına kotayı adlandırılmış bir sabitte "+
					"taşımıyor — gerekçe koda yazılmalı", q.file)
			}
		})
	}
}

// TestPodInventoryFiltersBeforeTheLimit — TAVAN BOŞA HARCANMASIN.
func TestPodInventoryFiltersBeforeTheLimit(t *testing.T) {
	src := flatWSCH(readCHSource(t, "pod_inventory.go"))

	inner := strings.Index(src, "has(res_keys, 'k8s.pod.name') LIMIT")
	if inner < 0 {
		t.Fatal("pod süzgeci İÇ sorguda değil — tavan pod adı taşımayan " +
			"satırlara harcanır ve filonun k8s yaymayan kısmı büyükse " +
			"envanter BOŞ döner")
	}
	// Dış `WHERE pod != ''` savunma olarak kalabilir ama TEK savunma
	// olamaz: iç süzgeç ondan ÖNCE gelmeli.
	outer := strings.Index(src, "WHERE pod != ''")
	if outer >= 0 && outer < inner {
		t.Error("dış süzgeç iç süzgeçten ÖNCE — sıra tersine dönmüş")
	}
}

func readCHSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("%s okunamadı: %v", name, err)
	}
	return string(b)
}

// flatWSCH — ardışık boşlukları teke indirir. SQL dizeleri kaynakta
// satır sarıyor ve girintileniyor; çıplak alt-dize araması onlara takılır
// (bu gece üç kez ısıran sınıf).
func flatWSCH(s string) string { return strings.Join(strings.Fields(s), " ") }
