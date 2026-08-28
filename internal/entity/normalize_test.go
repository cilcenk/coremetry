package entity

import (
	"sort"
	"strings"
	"testing"
)

// v0.10.129 — metrik etiket normalizasyonu + owner zinciri (design §3,
// görev "Metrik etiket normalizasyonu" + "Owner zinciri tablo testleri").
//
// Sözleşme: bir cluster'ın Thanos anlık görüntüsü (kube_node_info,
// kube_pod_info, kube_pod_owner, kube_replicaset_owner, kube_job_owner,
// kube_pod_container_info) → varlıklar + ilişkiler, DETERMİNİSTİK sırada.
// Pod → ReplicaSet → Deployment çözülür; StatefulSet/DaemonSet doğrudan;
// Job → CronJob iki hop; Node (static pod) ve <none> iş yükü üretmez.
// Node ataması yoksa runs_on kenarı çıkmaz (pod yine varlık).

func snap() Snapshot {
	return Snapshot{
		Nodes: []NodeInfo{{Node: "w1", InternalIP: "10.0.0.1", SystemUUID: "uuid-w1"}, {Node: "w0"}},
		Pods: []PodInfo{
			{Namespace: "pay", Pod: "api-7d9f-x1", UID: "u1", Node: "w1", CreatedByKind: "ReplicaSet", CreatedByName: "api-7d9f"},
			{Namespace: "pay", Pod: "db-0", UID: "u2", Node: "w0", CreatedByKind: "StatefulSet", CreatedByName: "db"},
			{Namespace: "pay", Pod: "agent-abc", UID: "u3", Node: "w1", CreatedByKind: "DaemonSet", CreatedByName: "agent"},
			{Namespace: "pay", Pod: "eod-123-k9", UID: "u4", Node: "w1", CreatedByKind: "Job", CreatedByName: "eod-123"},
			{Namespace: "kube-system", Pod: "static-w1", UID: "u5", Node: "w1", CreatedByKind: "Node", CreatedByName: "w1"},
			{Namespace: "pay", Pod: "orphan", UID: "u6", Node: "", CreatedByKind: "<none>", CreatedByName: "<none>"},
			{Namespace: "pay", Pod: "rs-only-z1", UID: "u7", Node: "w1", CreatedByKind: "ReplicaSet", CreatedByName: "rs-only"},
		},
		PodOwners: []PodOwner{
			{Namespace: "pay", Pod: "api-7d9f-x1", OwnerKind: "ReplicaSet", OwnerName: "api-7d9f"},
			{Namespace: "pay", Pod: "db-0", OwnerKind: "StatefulSet", OwnerName: "db"},
			{Namespace: "pay", Pod: "agent-abc", OwnerKind: "DaemonSet", OwnerName: "agent"},
			{Namespace: "pay", Pod: "eod-123-k9", OwnerKind: "Job", OwnerName: "eod-123"},
			{Namespace: "kube-system", Pod: "static-w1", OwnerKind: "Node", OwnerName: "w1"},
			{Namespace: "pay", Pod: "orphan", OwnerKind: "<none>", OwnerName: "<none>"},
			{Namespace: "pay", Pod: "rs-only-z1", OwnerKind: "ReplicaSet", OwnerName: "rs-only"},
		},
		RSOwners: []RSOwner{
			{Namespace: "pay", ReplicaSet: "api-7d9f", OwnerKind: "Deployment", OwnerName: "api"},
			{Namespace: "pay", ReplicaSet: "rs-only", OwnerKind: "<none>", OwnerName: "<none>"},
		},
		JobOwners: []JobOwner{{Namespace: "pay", Job: "eod-123", OwnerKind: "CronJob", OwnerName: "eod"}},
		Containers: []ContainerInfo{
			{Namespace: "pay", Pod: "api-7d9f-x1", Container: "app", Image: "reg/api:1.2"},
			{Namespace: "pay", Pod: "api-7d9f-x1", Container: "sidecar", Image: "reg/sc:9"},
		},
	}
}

func TestResolveWorkload(t *testing.T) {
	s := snap()
	idx := IndexOwners(s)
	cases := []struct {
		pod      string
		wantKind string
		wantName string
		ok       bool
	}{
		{"api-7d9f-x1", "Deployment", "api", true},
		{"db-0", "StatefulSet", "db", true},
		{"agent-abc", "DaemonSet", "agent", true},
		{"eod-123-k9", "CronJob", "eod", true},
		{"rs-only-z1", "ReplicaSet", "rs-only", true}, // sahipsiz RS: RS'nin kendisi iş yükü
		{"static-w1", "", "", false},                  // Node sahipli static pod
		{"orphan", "", "", false},                     // <none>
		{"unknown-pod", "", "", false},
	}
	for _, c := range cases {
		ns := "pay"
		if c.pod == "static-w1" {
			ns = "kube-system"
		}
		kind, name, ok := ResolveWorkload(ns, c.pod, idx)
		if ok != c.ok || kind != c.wantKind || name != c.wantName {
			t.Errorf("%s: (%q,%q,%v), beklenen (%q,%q,%v)", c.pod, kind, name, ok, c.wantKind, c.wantName, c.ok)
		}
	}
}

func TestNormalizeSnapshot(t *testing.T) {
	ents, rels := Normalize("c-1", snap())
	byID := map[string]Entity{}
	for _, e := range ents {
		if _, dup := byID[e.ID]; dup {
			t.Fatalf("çift varlık: %s", e.ID)
		}
		byID[e.ID] = e
	}
	want := []string{
		"cluster:c-1", "node:c-1/w1", "node:c-1/w0", "ns:c-1/pay", "ns:c-1/kube-system",
		"wl:c-1/pay/Deployment/api", "wl:c-1/pay/StatefulSet/db", "wl:c-1/pay/DaemonSet/agent",
		"wl:c-1/pay/CronJob/eod", "wl:c-1/pay/ReplicaSet/rs-only",
		"pod:c-1/pay/api-7d9f-x1", "pod:c-1/pay/db-0", "pod:c-1/kube-system/static-w1", "pod:c-1/pay/orphan",
		"ctr:c-1/pay/api-7d9f-x1/app", "ctr:c-1/pay/api-7d9f-x1/sidecar",
	}
	for _, id := range want {
		if _, ok := byID[id]; !ok {
			t.Errorf("varlık eksik: %s", id)
		}
	}
	// Alanlar: pod uid, node etiket alanları, parent.
	p := byID["pod:c-1/pay/api-7d9f-x1"]
	if p.UID != "u1" || p.Namespace != "pay" || p.Name != "api-7d9f-x1" || p.ClusterID != "c-1" || p.Type != TypePod {
		t.Fatalf("pod alanları: %+v", p)
	}
	if p.ParentID != "wl:c-1/pay/Deployment/api" {
		t.Fatalf("pod parent iş yükü olmalı: %q", p.ParentID)
	}
	if byID["pod:c-1/pay/orphan"].ParentID != "ns:c-1/pay" {
		t.Fatalf("iş yükü olmayan pod'un parent'ı namespace: %q", byID["pod:c-1/pay/orphan"].ParentID)
	}
	if n := byID["node:c-1/w1"]; n.UID != "uuid-w1" || n.Labels["internal_ip"] != "10.0.0.1" {
		t.Fatalf("node alanları: %+v", n)
	}
	if c := byID["ctr:c-1/pay/api-7d9f-x1/app"]; c.Labels["image"] != "reg/api:1.2" || c.ParentID != "pod:c-1/pay/api-7d9f-x1" {
		t.Fatalf("container alanları: %+v", c)
	}
	// İlişkiler.
	has := func(typ, parent, child string) bool {
		for _, r := range rels {
			if r.Type == typ && r.ParentID == parent && r.ChildID == child {
				return true
			}
		}
		return false
	}
	for _, r := range [][3]string{
		{RelParent, "cluster:c-1", "node:c-1/w1"},
		{RelParent, "cluster:c-1", "ns:c-1/pay"},
		{RelParent, "ns:c-1/pay", "wl:c-1/pay/Deployment/api"},
		{RelParent, "wl:c-1/pay/Deployment/api", "pod:c-1/pay/api-7d9f-x1"},
		{RelParent, "pod:c-1/pay/api-7d9f-x1", "ctr:c-1/pay/api-7d9f-x1/app"},
		{RelRunsOn, "pod:c-1/pay/api-7d9f-x1", "node:c-1/w1"},
		{RelParent, "ns:c-1/pay", "pod:c-1/pay/orphan"}, // iş yüksüz pod namespace'in altında
	} {
		if !has(r[0], r[1], r[2]) {
			t.Errorf("ilişki eksik: %s %s → %s", r[0], r[1], r[2])
		}
	}
	if has(RelRunsOn, "pod:c-1/pay/orphan", "node:c-1/") || has(RelRunsOn, "pod:c-1/pay/orphan", "node:c-1") {
		t.Fatal("node'suz pod runs_on üretmemeli")
	}
	// Deterministik sıra: iki koşum aynı.
	e2, r2 := Normalize("c-1", snap())
	if len(e2) != len(ents) || len(r2) != len(rels) {
		t.Fatal("iki koşum farklı boyut")
	}
	for i := range ents {
		if ents[i].ID != e2[i].ID {
			t.Fatalf("sıra deterministik değil: %d %s vs %s", i, ents[i].ID, e2[i].ID)
		}
	}
	ids := make([]string, 0, len(ents))
	for _, e := range ents {
		ids = append(ids, e.ID)
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatal("varlıklar id'ye göre sıralı olmalı")
	}
	// Kaynak damgası.
	for _, e := range ents {
		if e.Source != SourceThanos {
			t.Fatalf("Thanos kaynaklı varlık source=thanos taşımalı: %+v", e)
		}
	}
	// Aynı ad iki cluster'da farklı.
	e3, _ := Normalize("c-2", snap())
	if e3[0].ID == ents[0].ID || !strings.Contains(e3[len(e3)-1].ID, "c-2") {
		t.Fatal("cluster_id her id'ye girmeli")
	}
}

// Etiket allow-list: hassas/serbest etiketler bağlama TAŞINMAZ.
func TestAllowedLabels(t *testing.T) {
	in := map[string]string{
		"app": "api", "app.kubernetes.io/name": "api", "app.kubernetes.io/version": "1.2",
		"tier": "backend", "version": "v9",
		"secret-token": "xyz", "com.bank.customer-id": "123", "team": "core",
	}
	out := AllowedLabels(in)
	for _, k := range []string{"app", "app.kubernetes.io/name", "app.kubernetes.io/version", "tier", "version"} {
		if out[k] != in[k] {
			t.Errorf("%s korunmalı", k)
		}
	}
	for _, k := range []string{"secret-token", "com.bank.customer-id", "team"} {
		if _, ok := out[k]; ok {
			t.Errorf("%s taşınmamalı (allow-list dışı)", k)
		}
	}
}
