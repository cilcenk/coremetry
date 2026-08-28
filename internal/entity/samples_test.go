package entity

import "testing"

// v0.10.129 — Thanos yanıtı → Snapshot (saf; "sahte Thanos yanıtları"
// testlerinin temeli). Etiket adları KSM'nin kendi adları; eksik etiket
// boş string (satır düşmez — yokluğu üst katman karar verir). Aynı pod
// birden çok seri satırında görünürse (container başına kube_pod_info
// çıkmaz ama restarts vb. çıkar) tekilleştirilir.

func TestSnapshotFromSamples(t *testing.T) {
	samples := SampleSets{
		NodeInfo: []Sample{
			{Labels: map[string]string{"node": "w1", "internal_ip": "10.0.0.1", "kernel_version": "5.14", "os_image": "RHCOS", "system_uuid": "uuid-1"}},
		},
		PodInfo: []Sample{
			{Labels: map[string]string{"namespace": "pay", "pod": "api-x1", "uid": "u1", "node": "w1", "pod_ip": "10.1.0.5", "created_by_kind": "ReplicaSet", "created_by_name": "api-7d9f"}},
			{Labels: map[string]string{"namespace": "pay", "pod": "api-x1", "uid": "u1", "node": "w1"}}, // tekrar → tekil
			{Labels: map[string]string{"namespace": "", "pod": "nons"}},                                 // namespace yok → düşer
		},
		PodOwner: []Sample{
			{Labels: map[string]string{"namespace": "pay", "pod": "api-x1", "owner_kind": "ReplicaSet", "owner_name": "api-7d9f"}},
		},
		RSOwner: []Sample{
			{Labels: map[string]string{"namespace": "pay", "replicaset": "api-7d9f", "owner_kind": "Deployment", "owner_name": "api"}},
		},
		JobOwner: []Sample{
			{Labels: map[string]string{"namespace": "pay", "job_name": "eod-1", "owner_kind": "CronJob", "owner_name": "eod"}},
		},
		ContainerInfo: []Sample{
			{Labels: map[string]string{"namespace": "pay", "pod": "api-x1", "container": "app", "image": "reg/api:1"}},
		},
	}
	s := SnapshotFromSamples(samples)
	if len(s.Nodes) != 1 || s.Nodes[0].Node != "w1" || s.Nodes[0].SystemUUID != "uuid-1" || s.Nodes[0].InternalIP != "10.0.0.1" {
		t.Fatalf("node: %+v", s.Nodes)
	}
	if len(s.Pods) != 1 {
		t.Fatalf("pod tekilleşmeli ve namespace'siz düşmeli: %+v", s.Pods)
	}
	p := s.Pods[0]
	if p.Namespace != "pay" || p.Pod != "api-x1" || p.UID != "u1" || p.Node != "w1" || p.IP != "10.1.0.5" || p.CreatedByKind != "ReplicaSet" {
		t.Fatalf("pod alanları: %+v", p)
	}
	if len(s.PodOwners) != 1 || s.PodOwners[0].OwnerName != "api-7d9f" {
		t.Fatalf("pod owner: %+v", s.PodOwners)
	}
	if len(s.RSOwners) != 1 || s.RSOwners[0].ReplicaSet != "api-7d9f" || s.RSOwners[0].OwnerName != "api" {
		t.Fatalf("rs owner: %+v", s.RSOwners)
	}
	if len(s.JobOwners) != 1 || s.JobOwners[0].Job != "eod-1" || s.JobOwners[0].OwnerKind != "CronJob" {
		t.Fatalf("job owner: %+v", s.JobOwners)
	}
	if len(s.Containers) != 1 || s.Containers[0].Image != "reg/api:1" {
		t.Fatalf("container: %+v", s.Containers)
	}
	// Uçtan uca: normalize edilince Deployment çözülür.
	ents, _ := Normalize("c-1", s)
	found := false
	for _, e := range ents {
		if e.ID == "wl:c-1/pay/Deployment/api" {
			found = true
		}
	}
	if !found {
		t.Fatal("örneklerden Deployment iş yükü çözülmeli")
	}
	// Boş yanıt boş Snapshot (hata değil): kısmi cluster.
	if e := SnapshotFromSamples(SampleSets{}); len(e.Pods) != 0 || len(e.Nodes) != 0 {
		t.Fatal("boş örnek boş snapshot")
	}
}

// Her seri için PromQL — matcher doQuery'de eklenir; burada yalnız
// seçici + zaman filtresi olmayan anlık sorgular (KSM anlık durum).
// Görev kısıtı: filtresiz seri taraması YOK → her sorgu metrik adı + pod!="" gibi
// bir seçici taşır; namespace süzgeci ns matcher ile.
func TestSnapshotQueriesCarrySelectors(t *testing.T) {
	qs := SnapshotQueries(`,namespace=~"pay.*"`)
	if len(qs) != 6 {
		t.Fatalf("altı sorgu beklenir, alınan %d", len(qs))
	}
	for name, q := range qs {
		if q == "" || !contains(q, "{") {
			t.Errorf("%s seçici taşımalı: %q", name, q)
		}
		if name != "node_info" && !contains(q, `namespace=~"pay.*"`) {
			t.Errorf("%s namespace süzgecini taşımalı: %q", name, q)
		}
	}
	if contains(qs["node_info"], "namespace") {
		t.Fatal("kube_node_info namespace etiketi taşımaz; süzgeç eklenmemeli")
	}
}

func contains(s, sub string) bool { return len(sub) == 0 || indexOf(s, sub) >= 0 }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
