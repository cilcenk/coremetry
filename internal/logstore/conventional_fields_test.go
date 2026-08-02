package logstore

import (
	"os"
	"strings"
	"testing"
)

// v0.9.452 (operatör isteği, prod OpenShift ES) — cluster filtresi üç
// OTel resource_attributes yolunu tarıyordu; OpenShift cluster-logging
// (ClusterLogForwarder) cluster adını ÜST-DÜZEY openshift.labels.cluster
// alanına yazar. Prod'da /logs cluster seçimi bu yüzden HİÇBİR kaydı
// eşleştirmiyordu. Pin: yol listede kalır.
func TestClusterFilterCoversOpenShiftLabels(t *testing.T) {
	b, err := os.ReadFile("elasticsearch.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, `"resource_attributes.k8s.cluster.name",`)
	if i < 0 {
		t.Fatal("cluster path list not found")
	}
	if !strings.Contains(src[i:i+400], `"openshift.labels.cluster",`) {
		t.Error("openshift.labels.cluster missing from the cluster filter paths — OpenShift cluster-logging indices match on the TOP-LEVEL field, not resource_attributes.*; the /logs cluster picker matches nothing on prod without it")
	}
}

// v0.9.452 — alfabetik listFieldsMax kırpması geç harfli konvansiyonel
// yolları (openshift.*) düşürüyordu; frontend'in Popular fields grubu
// "listede-varlık = gerçek" sözleşmesiyle çalıştığından cap'e kurban
// giden yol geri eklenir. seen'de OLMAYAN alan asla icat edilmez.
func TestEnsureConventionalFields(t *testing.T) {
	seen := map[string]struct{}{
		"aaa": {}, "bbb": {},
		"openshift.labels.cluster": {},
		"kubernetes.pod_name":      {},
	}
	out := ensureConventionalFields([]string{"aaa", "bbb"}, seen)
	joined := strings.Join(out, "|")
	if !strings.Contains(joined, "openshift.labels.cluster") || !strings.Contains(joined, "kubernetes.pod_name") {
		t.Errorf("mapping'de var olan konvansiyonel yollar geri eklenmedi: %v", out)
	}
	if strings.Contains(joined, "service_name") {
		t.Errorf("mapping'de OLMAYAN alan icat edildi: %v", out)
	}
	// idempotent: zaten listedeyse çoğaltma
	out2 := ensureConventionalFields(out, seen)
	if len(out2) != len(out) {
		t.Errorf("duplicate append: %v", out2)
	}
}

// v0.9.480 (operator-reported, prod OpenShift ES) — trace'in Logs
// sekmesinde bazı kayıtların SERVICE kolonu boştu: kayıt OTel-şekilli
// değil, düz service_name ise gövde JSON'unda ve OPERASYON adı
// (DIGITAL_TRANSFER_EFT). Asıl servis kimliği kubernetes.container_name.
// Pinler: (1) k8s işyükü alanları gösterim zincirinde VAR ve düz
// service_name'den ÖNCE (operasyon adının servis kolonunu ele geçirmesi
// yanlış yönde çözülmesin); (2) eklenen alanlar servis FİLTRESİNİN
// eşlediği alanlar (svcFields) — gösterilen ada tıklayıp filtreleyince
// boş dönmez (v0.8.265 sınıfı).
func TestServiceDisplayPrefersK8sWorkload(t *testing.T) {
	b, err := os.ReadFile("elasticsearch.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, `r.ServiceName = readPathAny(src, s.fields.Service,`)
	if i < 0 {
		t.Fatal("main service mapper not found")
	}
	chain := src[i : i+400]
	ik8s := strings.Index(chain, `"kubernetes.container_name"`)
	iflat := strings.Index(chain, `"service_name"`)
	if ik8s < 0 {
		t.Fatal("kubernetes.container_name gösterim zincirinde yok — OpenShift kayıtlarında SERVICE boş kalır")
	}
	if iflat >= 0 && iflat < ik8s {
		t.Error("düz service_name k8s işyükünden önce — uygulama-yayımlı operasyon adı servis kolonunu ele geçirir")
	}
	// v0.9.545 — iddia TEK SATIRLIK LİTERALDEN alan-varlığına çevrildi.
	// Sözleşme aynı: gösterim zincirindeki k8s alanları servis
	// FİLTRESİNİN de eşlediği alanlar olmalı, yoksa operatör gösterilen
	// ada tıklayınca boş liste alır (v0.8.265 sınıfı). Değişen tek şey
	// svcFields'ın çok satırlı hale gelmesi (labels.app + env-eki-soyulmuş
	// değer eklendi); literali pinlemek biçimi pinliyordu, davranışı değil.
	svcIdx := strings.Index(src, "svcFields := []string{")
	if svcIdx < 0 {
		t.Fatal("svcFields bulunamadı")
	}
	svcBlock := src[svcIdx : svcIdx+400]
	for _, fld := range []string{
		"s.fields.Service", `"kubernetes.container.name"`, `"kubernetes.container_name"`,
	} {
		if !strings.Contains(svcBlock, fld) {
			t.Errorf("servis filtresi %s alanını eşlemiyor — gösterilen ada tıklayınca filtre boş döner", fld)
		}
	}
}
