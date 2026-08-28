package entity

import "testing"

// v0.10.129 — K8s entity katmanı adım 3: kimlik üretimi (design §1.2).
//
// Sözleşme: entity_id = "<tip>:<cluster_id>/<doğal anahtar>"; cluster_id
// Remote Cluster kaydının id'si (keşfedilmez); namespace/pod adları
// cluster'lar arası benzersiz DEĞİL → aynı ad iki cluster'da iki farklı
// id. Servis ekseni cluster'sız ("svc:<name>") — bugünkü servis kimliği.
// ParseID ters yönü verir (URL'den varlığa gezinme).

func TestEntityIDs(t *testing.T) {
	cases := []struct{ name, got, want string }{
		{"cluster", ClusterID("c-1"), "cluster:c-1"},
		{"node", NodeID("c-1", "worker-0"), "node:c-1/worker-0"},
		{"namespace", NamespaceID("c-1", "payments"), "ns:c-1/payments"},
		{"workload", WorkloadID("c-1", "payments", "Deployment", "api"), "wl:c-1/payments/Deployment/api"},
		{"pod", PodID("c-1", "payments", "api-7d9f-x1"), "pod:c-1/payments/api-7d9f-x1"},
		{"container", ContainerID("c-1", "payments", "api-7d9f-x1", "app"), "ctr:c-1/payments/api-7d9f-x1/app"},
		{"service (cluster'sız)", ServiceID("checkout-api"), "svc:checkout-api"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: %q, beklenen %q", c.name, c.got, c.want)
		}
	}
	// Aynı ad iki cluster'da iki farklı varlık.
	if PodID("c-1", "ns", "api-0") == PodID("c-2", "ns", "api-0") {
		t.Fatal("pod id cluster'a göre ayrışmalı")
	}
}

func TestParseID(t *testing.T) {
	cases := []struct {
		id       string
		wantType string
		wantCID  string
		wantName string
		ok       bool
	}{
		{"pod:c-1/payments/api-7d9f-x1", TypePod, "c-1", "api-7d9f-x1", true},
		{"wl:c-1/payments/StatefulSet/db", TypeWorkload, "c-1", "db", true},
		{"node:c-1/worker-0", TypeNode, "c-1", "worker-0", true},
		{"cluster:c-1", TypeCluster, "c-1", "c-1", true},
		{"svc:checkout-api", TypeService, "", "checkout-api", true},
		{"ctr:c-1/ns/pod/app", TypeContainer, "c-1", "app", true},
		{"garbage", "", "", "", false},
		{"pod:c-1", "", "", "", false}, // eksik bileşen
		{"", "", "", "", false},
	}
	for _, c := range cases {
		ref, ok := ParseID(c.id)
		if ok != c.ok {
			t.Fatalf("%q: ok=%v, beklenen %v", c.id, ok, c.ok)
		}
		if !ok {
			continue
		}
		if ref.Type != c.wantType || ref.ClusterID != c.wantCID || ref.Name != c.wantName {
			t.Fatalf("%q: %+v", c.id, ref)
		}
		// Gidiş-dönüş.
		if ref.String() != c.id {
			t.Fatalf("%q gidiş-dönüş bozuk: %q", c.id, ref.String())
		}
	}
	// Ad içinde '/' olan pod (olmaz ama) yine de son bileşen olarak çözülür.
	ref, ok := ParseID("pod:c-1/ns/a/b")
	if !ok || ref.Namespace != "ns" || ref.Name != "a/b" {
		t.Fatalf("fazla '/' son bileşende kalmalı: %+v %v", ref, ok)
	}
}
