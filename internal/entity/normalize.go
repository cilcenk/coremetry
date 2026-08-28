package entity

import (
	"sort"
	"strings"
)

// normalize.go — METRİK ETİKET NORMALİZASYONU + OWNER ZİNCİRİ (saf).
//
// Bir cluster'ın Thanos anlık görüntüsü (kube_node_info, kube_pod_info,
// kube_pod_owner, kube_replicaset_owner, kube_job_owner,
// kube_pod_container_info) → varlıklar + ilişkiler. Deterministik sıra
// (id'ye göre) — diff ve yazım idempotent kalsın.
//
// Owner zinciri yalnız standart K8s tipleri: Pod → ReplicaSet → Deployment;
// StatefulSet / DaemonSet doğrudan; Job → CronJob. DeploymentConfig ve
// apps.openshift.io YOK (görev kısıtı). Sahipsiz ReplicaSet'in kendisi iş
// yükü; Node sahipli static pod ve <none> iş yükü üretmez (pod namespace'in
// altına bağlanır).

// Thanos etiket setleri (etiket adları = KSM'nin kendi adları).
type NodeInfo struct {
	Node, InternalIP, KernelVersion, OSImage, SystemUUID string
}
type PodInfo struct {
	Namespace, Pod, UID, Node, IP string
	CreatedByKind, CreatedByName  string
}
type PodOwner struct{ Namespace, Pod, OwnerKind, OwnerName string }
type RSOwner struct{ Namespace, ReplicaSet, OwnerKind, OwnerName string }
type JobOwner struct{ Namespace, Job, OwnerKind, OwnerName string }
type ContainerInfo struct{ Namespace, Pod, Container, Image string }

// Snapshot — bir cluster'ın tek tick'lik görüntüsü.
type Snapshot struct {
	Nodes      []NodeInfo
	Pods       []PodInfo
	PodOwners  []PodOwner
	RSOwners   []RSOwner
	JobOwners  []JobOwner
	Containers []ContainerInfo
}

// Entity — entities satırının yazım-öncesi hâli (ömür alanları diff'te).
type Entity struct {
	Type      string
	ClusterID string
	ID        string
	Namespace string
	Name      string
	UID       string
	ParentID  string
	Labels    map[string]string
	Source    string
}

// Relation — entity_relations satırı (ömür alanları diff'te).
type Relation struct {
	Type      string
	ClusterID string
	ParentID  string
	ChildID   string
	Source    string
}

// OwnerIndex — owner zinciri arama tabloları (namespace-kapsamlı).
type OwnerIndex struct {
	pod map[[2]string]PodOwner // ns/pod
	rs  map[[2]string]RSOwner  // ns/rs
	job map[[2]string]JobOwner // ns/job
}

// IndexOwners — anlık görüntüden arama tabloları.
func IndexOwners(s Snapshot) OwnerIndex {
	idx := OwnerIndex{
		pod: make(map[[2]string]PodOwner, len(s.PodOwners)),
		rs:  make(map[[2]string]RSOwner, len(s.RSOwners)),
		job: make(map[[2]string]JobOwner, len(s.JobOwners)),
	}
	for _, o := range s.PodOwners {
		idx.pod[[2]string{o.Namespace, o.Pod}] = o
	}
	for _, o := range s.RSOwners {
		idx.rs[[2]string{o.Namespace, o.ReplicaSet}] = o
	}
	for _, o := range s.JobOwners {
		idx.job[[2]string{o.Namespace, o.Job}] = o
	}
	return idx
}

func noOwner(kind, name string) bool {
	return kind == "" || kind == "<none>" || name == "" || name == "<none>"
}

// ResolveWorkload — pod'un iş yükü (tür, ad). ok=false: iş yükü yok
// (static pod, sahipsiz, bilinmeyen pod).
func ResolveWorkload(ns, pod string, idx OwnerIndex) (kind, name string, ok bool) {
	o, found := idx.pod[[2]string{ns, pod}]
	if !found || noOwner(o.OwnerKind, o.OwnerName) {
		return "", "", false
	}
	switch o.OwnerKind {
	case "ReplicaSet":
		if rs, ok := idx.rs[[2]string{ns, o.OwnerName}]; ok && !noOwner(rs.OwnerKind, rs.OwnerName) {
			return rs.OwnerKind, rs.OwnerName, true // Deployment
		}
		return "ReplicaSet", o.OwnerName, true
	case "Job":
		if j, ok := idx.job[[2]string{ns, o.OwnerName}]; ok && !noOwner(j.OwnerKind, j.OwnerName) {
			return j.OwnerKind, j.OwnerName, true // CronJob
		}
		return "Job", o.OwnerName, true
	case "StatefulSet", "DaemonSet":
		return o.OwnerKind, o.OwnerName, true
	case "Node":
		return "", "", false // static pod
	}
	// Bilinmeyen sahip türü (CRD): olduğu gibi taşınır — tip bilgisi kaybolmasın.
	return o.OwnerKind, o.OwnerName, true
}

// allowedLabelKeys — bağlama taşınan etiketler; geri kalanı YAZILMAZ
// (hassas etiket riski, görev kısıtı "maskeleme bozulmayacak").
var allowedLabelKeys = map[string]bool{
	"app": true, "tier": true, "version": true,
	"internal_ip": true, "kernel_version": true, "os_image": true, "image": true,
}

// AllowedLabels — allow-list süzgeci (+ app.kubernetes.io/* öneki).
func AllowedLabels(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if v == "" {
			continue
		}
		if allowedLabelKeys[k] || strings.HasPrefix(k, "app.kubernetes.io/") {
			out[k] = v
		}
	}
	return out
}

// Normalize — anlık görüntü → varlıklar + ilişkiler, id sırasında.
func Normalize(cid string, s Snapshot) ([]Entity, []Relation) {
	idx := IndexOwners(s)
	ents := map[string]Entity{}
	rels := map[string]Relation{}
	add := func(e Entity) {
		e.ClusterID, e.Source = cid, SourceThanos
		if e.Labels == nil {
			e.Labels = map[string]string{}
		}
		if _, dup := ents[e.ID]; !dup {
			ents[e.ID] = e
		}
	}
	rel := func(typ, parent, child string) {
		r := Relation{Type: typ, ClusterID: cid, ParentID: parent, ChildID: child, Source: SourceThanos}
		rels[typ+"|"+parent+"|"+child] = r
	}
	clusterID := ClusterID(cid)
	add(Entity{Type: TypeCluster, ID: clusterID, Name: cid})
	for _, n := range s.Nodes {
		if n.Node == "" {
			continue
		}
		id := NodeID(cid, n.Node)
		add(Entity{Type: TypeNode, ID: id, Name: n.Node, UID: n.SystemUUID, ParentID: clusterID,
			Labels: AllowedLabels(map[string]string{"internal_ip": n.InternalIP, "kernel_version": n.KernelVersion, "os_image": n.OSImage})})
		rel(RelParent, clusterID, id)
	}
	for _, p := range s.Pods {
		if p.Namespace == "" || p.Pod == "" {
			continue
		}
		nsID := NamespaceID(cid, p.Namespace)
		add(Entity{Type: TypeNamespace, ID: nsID, Name: p.Namespace, ParentID: clusterID})
		rel(RelParent, clusterID, nsID)
		parent := nsID
		if kind, name, ok := ResolveWorkload(p.Namespace, p.Pod, idx); ok {
			wlID := WorkloadID(cid, p.Namespace, kind, name)
			add(Entity{Type: TypeWorkload, ID: wlID, Namespace: p.Namespace, Name: name, ParentID: nsID,
				Labels: map[string]string{"kind": kind}})
			rel(RelParent, nsID, wlID)
			parent = wlID
		}
		podID := PodID(cid, p.Namespace, p.Pod)
		add(Entity{Type: TypePod, ID: podID, Namespace: p.Namespace, Name: p.Pod, UID: p.UID, ParentID: parent,
			Labels: AllowedLabels(map[string]string{"pod_ip": p.IP})})
		rel(RelParent, parent, podID)
		if p.Node != "" {
			nodeID := NodeID(cid, p.Node)
			if _, ok := ents[nodeID]; !ok {
				// kube_node_info eksik olsa da pod'un node'u varlık olur.
				add(Entity{Type: TypeNode, ID: nodeID, Name: p.Node, ParentID: clusterID})
				rel(RelParent, clusterID, nodeID)
			}
			rel(RelRunsOn, podID, nodeID)
		}
	}
	for _, c := range s.Containers {
		if c.Namespace == "" || c.Pod == "" || c.Container == "" {
			continue
		}
		podID := PodID(cid, c.Namespace, c.Pod)
		if _, ok := ents[podID]; !ok {
			continue // pod listesinde olmayan container: sahipsiz, atla
		}
		id := ContainerID(cid, c.Namespace, c.Pod, c.Container)
		add(Entity{Type: TypeContainer, ID: id, Namespace: c.Namespace, Name: c.Container, ParentID: podID,
			Labels: AllowedLabels(map[string]string{"image": c.Image})})
		rel(RelParent, podID, id)
	}
	outE := make([]Entity, 0, len(ents))
	for _, e := range ents {
		outE = append(outE, e)
	}
	sort.Slice(outE, func(i, j int) bool { return outE[i].ID < outE[j].ID })
	outR := make([]Relation, 0, len(rels))
	for _, r := range rels {
		outR = append(outR, r)
	}
	sort.Slice(outR, func(i, j int) bool {
		if outR[i].Type != outR[j].Type {
			return outR[i].Type < outR[j].Type
		}
		if outR[i].ParentID != outR[j].ParentID {
			return outR[i].ParentID < outR[j].ParentID
		}
		return outR[i].ChildID < outR[j].ChildID
	})
	return outE, outR
}
