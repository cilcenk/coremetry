package logstore

import (
	"encoding/json"
	"strings"
	"testing"
)

// v0.9.1249 — Kibana-parite artığı: bağlam modalına pod kapsamı.
//
// Pod, ES tarafında BUGÜNE KADAR yalnız bir KQL kısayoluydu
// (expandShorthand) — yapısal bir clause yoktu, yani Filter.Pod
// dolduğunda sorgu onu sessizce yok sayardı. Bu testler clause'un
// cluster emsaliyle AYNI şekli taşıdığını çakar:
//
//   - her aday alan için exactTermsBothShapes (dinamik `.keyword`
//     alt-alanı + ECS'te keyword yazılan çıplak alan, exists-guard'lı),
//   - hepsi tek bool.should + minimum_should_match:1 altında,
//   - pod BOŞSA hiçbir pod clause'u üretilmez (bağlamın varsayılanı
//     geniş komşuluktur; boş değerle terim sormak SIFIR doküman eşler).

func TestPodFilterTriesBothFieldShapes(t *testing.T) {
	s := &ESStore{}
	s.cfg.defaults()
	s.fields = s.cfg.Fields
	raw, err := json.Marshal(s.buildQuery(Filter{Pod: "payment-api-7d6f9b54c5-xkv2m"}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	q := string(raw)
	for _, want := range []string{
		// Operatörün prod şekli (OpenShift cluster-logging) ÖNCE.
		`"kubernetes.pod_name.keyword":"payment-api-7d6f9b54c5-xkv2m"`,
		`"kubernetes.pod_name":"payment-api-7d6f9b54c5-xkv2m"`,
		// OTel semconv + ES resource yolu.
		`"k8s.pod.name.keyword":"payment-api-7d6f9b54c5-xkv2m"`,
		`"kubernetes.pod.name.keyword":"payment-api-7d6f9b54c5-xkv2m"`,
		`"resource_attributes.k8s.pod.name.keyword":"payment-api-7d6f9b54c5-xkv2m"`,
		`"pod_name.keyword":"payment-api-7d6f9b54c5-xkv2m"`,
		// Çıplak dal exists-guard'sız GİTMEZ: dinamik mapping'de
		// analiz edilmiş alana terim atmak token eşleşmesi demektir
		// (v0.8.239 dersi) — pod adları tireli, tam bu tuzak.
		`"must_not":[{"exists":{"field":"kubernetes.pod_name.keyword"}}]`,
		`"must_not":[{"exists":{"field":"k8s.pod.name.keyword"}}]`,
		`"minimum_should_match":1`,
	} {
		if !strings.Contains(q, want) {
			t.Errorf("pod clause missing %s in: %s", want, q)
		}
	}
}

// Pod boşken clause HİÇ çıkmamalı — aksi halde her bağlam okuması
// eşleşmeyen bir terim taşır ve iki yarı da sessizce boşalır.
func TestPodFilterAbsentWhenEmpty(t *testing.T) {
	s := &ESStore{}
	s.cfg.defaults()
	s.fields = s.cfg.Fields
	raw, _ := json.Marshal(s.buildQuery(Filter{Service: "checkout"}))
	if strings.Contains(string(raw), "pod") {
		t.Errorf("boş Pod pod clause üretmemeli: %s", raw)
	}
}

// Aday alan listesi FE'nin POD_FIELDS'iyle aynı sözleşme: gösterilen
// pod adı (lib/logPod.ts podOfLog) bu filtreyle BULUNABİLİR olmalı.
// TS tarafındaki ayna kapısı logPod.test.ts'te; burada listenin kendisi
// (sıra + kapsam) pinleniyor.
func TestESPodFieldsShape(t *testing.T) {
	want := []string{
		"kubernetes.pod_name",
		"k8s.pod.name",
		"kubernetes.pod.name",
		"resource_attributes.k8s.pod.name",
		"pod_name",
	}
	if len(esPodFields) != len(want) {
		t.Fatalf("esPodFields kapsamı değişmiş: %v", esPodFields)
	}
	for i, w := range want {
		if esPodFields[i] != w {
			t.Errorf("esPodFields[%d] = %q, beklenen %q", i, esPodFields[i], w)
		}
	}
}
