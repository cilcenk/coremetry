package logstore

import (
	"strings"
	"testing"
)

// v0.9.1250 — histogram kırılım eksenleri CH tarafı. İki ayrı sözleşme
// çakılıyor:
//
//  1. chLogsNamespaceExpr'in ŞEKLİ (logs tablosu attribute'ları PARALEL
//     DİZİ tutar; Map erişimi yazan ifade v0.8.400'de UNKNOWN_IDENTIFIER
//     ile patlamıştı ve CH backend'i o yolu kullanmadığı için uzun süre
//     sessiz kalmıştı).
//  2. chLogsGroupExpr'in WHITELIST'i: frontend'in SUNDUĞU her eksen
//     backend'de gerçekten uygulanmış olmalı. Tanınmayan değer tek bir
//     '_total' serisine düşer — v0.9.1220 cluster/namespace'i tam bu
//     yüzden sunmamıştı; bu test o düşüşün geri gelmesini engeller.

func TestChLogsNamespaceExpr_ResArrayLookup(t *testing.T) {
	for _, want := range []string{
		"res_values[indexOf(res_keys, 'k8s.namespace.name')]",
		"res_values[indexOf(res_keys, 'kubernetes.namespace_name')]",
		"res_values[indexOf(res_keys, 'kubernetes.namespace')]",
		"res_values[indexOf(res_keys, 'namespace')]",
		"coalesce(",
	} {
		if !strings.Contains(chLogsNamespaceExpr, want) {
			t.Errorf("chLogsNamespaceExpr missing %q:\n%s", want, chLogsNamespaceExpr)
		}
	}
	// Kanonik semconv anahtarı ÖNCE coalesce edilmeli (env/cluster/pod
	// zincirlerindeki aynı kural).
	canonical := strings.Index(chLogsNamespaceExpr, "'k8s.namespace.name'")
	legacy := strings.Index(chLogsNamespaceExpr, "'kubernetes.namespace_name'")
	if canonical < 0 || legacy < 0 || canonical > legacy {
		t.Fatalf("kanonik k8s.namespace.name önce gelmeli:\n%s", chLogsNamespaceExpr)
	}
	for _, banned := range []string{
		"resource_attributes[", "attributes[",
		// ES DOKÜMAN yolları; OTLP resource anahtarı olarak asla var
		// olmazlar — buraya konurlarsa ölü SQL olurlar.
		"resource_attributes.k8s.namespace.name",
		"resource.k8s.namespace.name",
	} {
		if strings.Contains(chLogsNamespaceExpr, banned) {
			t.Errorf("chLogsNamespaceExpr must not contain %q:\n%s", banned, chLogsNamespaceExpr)
		}
	}
}

func TestChLogsGroupExpr_AxisWhitelist(t *testing.T) {
	cases := []struct {
		name    string
		groupBy string
		want    string // ifadenin İÇERMESİ gereken parça
		total   bool   // gruplanmamış tek seriye düşmeli mi
	}{
		{name: "boş = toplam", groupBy: "", want: "'_total'", total: true},
		{name: "servis", groupBy: "service", want: "service_name"},
		{name: "seviye", groupBy: "severity", want: "startsWith(upper(severity_text), 'FATAL')"},
		{name: "cluster", groupBy: "cluster", want: "'k8s.cluster.name'"},
		{name: "namespace", groupBy: "namespace", want: "'k8s.namespace.name'"},
		// Tanınmayan eksen mevcut davranışı korur (400 değil, sessiz
		// severity'ye kaydırma da değil).
		{name: "bilinmeyen", groupBy: "pod", want: "'_total'", total: true},
		{name: "büyük harf eşleşmez", groupBy: "Cluster", want: "'_total'", total: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := chLogsGroupExpr(c.groupBy)
			if !strings.Contains(got, c.want) {
				t.Fatalf("chLogsGroupExpr(%q) = %q, %q içermeli", c.groupBy, got, c.want)
			}
			if !c.total && got == "'_total'" {
				t.Fatalf("chLogsGroupExpr(%q) sessizce _total'a düştü — eksen sunulmamalıydı", c.groupBy)
			}
		})
	}
}

// Boş türetim kararı (v0.9.1250): cluster/namespace opsiyonel resource
// attribute'ları — taşımayan satırlar ELENMEZ (servis ekseni de elemiyor
// ve CH yolunda ES'teki total-eksi-toplam OTHER sentezi YOK, elenirse
// yığın sessizce eksik sayardı) ve boş dize olarak da dönmez (boş
// lejant çipi). OTHER adını alırlar: ES'in atfedilemeyen artığıyla aynı ad,
// frontend'in (v0.9.1220 collapseGroups) zaten "diğer"e katladığı ad.
func TestChLogsGroupExpr_EmptyDerivationBecomesOther(t *testing.T) {
	for _, axis := range []string{"cluster", "namespace"} {
		got := chLogsGroupExpr(axis)
		if !strings.Contains(got, "'OTHER'") {
			t.Errorf("%s ekseni boş türetimi adlandırmıyor (OTHER yok):\n%s", axis, got)
		}
		if !strings.Contains(got, "nullIf(") {
			t.Errorf("%s ekseni boş değeri nullIf ile yakalamıyor:\n%s", axis, got)
		}
	}
	// Servis ekseni bu sarmalayıcıyı ALMAZ: service_name boş gelmez ve
	// alınırsa gerçek bir servis adı OTHER'a katlanırdı.
	if strings.Contains(chLogsGroupExpr("service"), "OTHER") {
		t.Error("servis ekseni OTHER sarmalayıcısı almamalı")
	}
}
