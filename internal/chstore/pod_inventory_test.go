package chstore

import (
	"os"
	"strings"
	"testing"
)

// v0.10.40 — K8s entity katmanı Faz 1'in ingest'e dokunmayan yarısı.
//
// KİMLİK: (k8s.namespace.name, k8s.pod.name) — operatör kararı
// 2026-08-26. uid prod'da gelmiyor ve beklenmiyor.

// TestLooksStatefulSetName — SESSİZ BİRLEŞMİŞ ÖMÜR uyarısı.
//
// ⚠ Kimliğin tek zayıf noktası bu. Deployment pod'ları rastgele sonek
// taşıyor, yani her yeniden yaratma YENİ ad → ömürler doğal ayrışıyor.
// StatefulSet pod adı SABİT (`svc-0` hep `svc-0`): restart'tan sonra
// aynı ad döner ve iki ayrı pod ömrü TEK satırda birleşir —
// firstSeen/lastSeen iki hayatı kapsar ve "bu pod 40 gündür ayakta"
// gibi YANLIŞ bir cümle üretir.
//
// Sezgi kesinlik değil ve öyle olduğu adıyla söyleniyor. Yanlış pozitif
// ZARARSIZ (fazladan uyarı); yanlış negatif ise sessizce birleşmiş bir
// ömür — o yüzden eşik gevşek.
func TestLooksStatefulSetName(t *testing.T) {
	for _, tc := range []struct {
		name string
		pod  string
		want bool
	}{
		// StatefulSet: ordinal ile biter.
		{"tek haneli ordinal", "kafka-0", true},
		{"çift haneli ordinal", "kafka-12", true},
		{"uzun ad", "bsa-core-postgres-0", true},

		// Deployment: replicaset hash + rastgele sonek.
		{"deployment pod'u", "bsa-mobile-login-prod-59df758cc-scdwq", false},
		{"kısa rastgele sonek", "svc-abc12", false},
		{"hash biter", "api-7d4f9c", false},

		// Kenar durumlar — hiçbiri panik etmemeli.
		{"boş", "", false},
		{"tek tire", "-", false},
		{"yalnız rakam", "12345", false},
		{"tire ile biter", "svc-", false},
		{"tireden sonra rakam yok", "svc-abc", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksStatefulSetName(tc.pod); got != tc.want {
				t.Errorf("looksStatefulSetName(%q) = %v; want %v", tc.pod, got, tc.want)
			}
		})
	}
}

// TestPodInventoryIsSampledAndBounded — CLAUDE.md sert kısıtı.
//
// Ham `spans` okuyan her sorgu LIMIT + max_execution_time + zaman-sınırlı
// WHERE taşımak zorunda.
func TestPodInventoryIsSampledAndBounded(t *testing.T) {
	b, err := os.ReadFile("pod_inventory.go")
	if err != nil {
		t.Fatalf("okunamadı: %v", err)
	}
	src := string(b)
	for _, must := range []string{
		"LIMIT %d",
		"max_execution_time = 25",
		"WHERE time >= ? AND time <= ?",
	} {
		if !strings.Contains(src, must) {
			t.Errorf("ham spans sorgusunda eksik: %s", must)
		}
	}
	// Örneklem tavanı ZARFTA: "gördüğüm bu kadar" ile "olan bu kadar"
	// ayrımı operatörün elinde kalmalı.
	if !strings.Contains(src, "SampleRows: podInventorySampleRows") {
		t.Error("örneklem tavanı zarfta taşınmıyor")
	}
	// Kimlik (namespace, pod) — operatör kararı.
	if !strings.Contains(src, "GROUP BY ns, pod") {
		t.Error("kimlik (namespace, pod) değil — kararla uyuşmuyor")
	}
	// Pod'suz satırlar envantere girmemeli: boş kimlik bir pod değildir.
	if !strings.Contains(src, "WHERE pod != ''") {
		t.Error("pod'suz satırlar eleniyor değil — boş kimlikli 'pod' üretilir")
	}
}

// TestStabilityFlagIsCarried — sınır SESSİZ bırakılmıyor.
//
// Bayrak taşınmazsa arayüz StatefulSet ömrünü tek hayat sanar ve
// entity katmanının İLK çıktısı yanlış olur.
func TestStabilityFlagIsCarried(t *testing.T) {
	b, err := os.ReadFile("pod_inventory.go")
	if err != nil {
		t.Fatalf("okunamadı: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "r.NameStable = looksStatefulSetName(r.Pod)") {
		t.Error("NameStable doldurulmuyor — birleşmiş ömür sessiz kalır")
	}
	if !strings.Contains(src, `json:"nameStable"`) {
		t.Error("NameStable zarfta taşınmıyor — arayüz uyaramaz")
	}
}

// TestNoMaterializedViewWasAdded — BİLİNÇLİ ERTELEME.
//
// Faz 1 aslında bir `pod_seen` MV'si öngörüyordu. Eklenmedi ve sebebi
// ölçülebilir: service_seen UCUZ bir promoted kolona (service_name)
// gruplanıyor; namespace/pod promoted DEĞİL, dizi çıkarımıyla geliyor.
// Bir MV bunu HER eklenen span satırında, SÜZGEÇSİZ yapardı.
// (db_summary_5m de dizi çıkarımı yapıyor ama yalnız db_system != ”
// alt kümesinde — emsal değil.)
//
// Bu test o kararı çiviliyor: MV bir gün eklenirse, ingest maliyeti
// ölçülmüş ve promoted kolon kararı verilmiş olarak eklensin.
func TestNoMaterializedViewWasAdded(t *testing.T) {
	b, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("store.go okunamadı: %v", err)
	}
	if strings.Contains(string(b), "pod_seen") {
		t.Error("pod_seen MV'si eklenmiş — ingest yoluna süzgeçsiz dizi çıkarımı " +
			"koymadan önce maliyeti ÖLÇÜLMELİ ve promoted kolon kararı verilmeli " +
			"(v0.10.40 gerekçesi, pod_inventory.go başlığı)")
	}
}
