// Package entity — K8s ENTITY KATMANI (v0.10.129, AŞAMA 3 adım 3;
// docs/plans/entity-layer-design-2026-08-28.md).
//
// cluster > node > namespace > workload > pod > container hiyerarşisi +
// ayrı service ekseni. Kök kimlik Remote Cluster kaydının id'si
// (thanos.ClusterConfig.EffectiveID) — keşfedilmez, konfigürasyondan
// gelir. Bu paket saf çekirdekleri (kimlik, normalizasyon, ömür farkı) ve
// senkronizasyon işçisini taşır; CH okuma/yazma chstore'da.
package entity

import "strings"

// Varlık tipleri (entities.entity_type).
const (
	TypeCluster   = "cluster"
	TypeNode      = "node"
	TypeNamespace = "namespace"
	TypeWorkload  = "workload"
	TypePod       = "pod"
	TypeContainer = "container"
	TypeService   = "service"
)

// İlişki tipleri (entity_relations.rel_type).
const (
	RelParent = "parent"  // cluster→node, cluster→ns, ns→wl, wl→pod, ns→pod (iş yüksüz), pod→ctr
	RelRunsOn = "runs_on" // pod→node
	RelRuns   = "runs"    // pod→service
)

// Kaynak damgaları (entities.source).
const (
	SourceThanos = "thanos"
	SourceSpan   = "span"
)

// id önekleri — TopologyNodeIdentity emsali: tip önekten okunur.
var typePrefix = map[string]string{
	TypeCluster: "cluster", TypeNode: "node", TypeNamespace: "ns",
	TypeWorkload: "wl", TypePod: "pod", TypeContainer: "ctr", TypeService: "svc",
}

var prefixType = func() map[string]string {
	m := map[string]string{}
	for t, p := range typePrefix {
		m[p] = t
	}
	return m
}()

func ClusterID(cid string) string       { return "cluster:" + cid }
func NodeID(cid, node string) string    { return "node:" + cid + "/" + node }
func NamespaceID(cid, ns string) string { return "ns:" + cid + "/" + ns }
func PodID(cid, ns, pod string) string  { return "pod:" + cid + "/" + ns + "/" + pod }
func ServiceID(name string) string      { return "svc:" + name }
func WorkloadID(cid, ns, kind, name string) string {
	return "wl:" + cid + "/" + ns + "/" + kind + "/" + name
}
func ContainerID(cid, ns, pod, ctr string) string {
	return "ctr:" + cid + "/" + ns + "/" + pod + "/" + ctr
}

// Ref — çözülmüş entity_id.
type Ref struct {
	Type      string
	ClusterID string
	Namespace string
	Kind      string // workload türü (Deployment/StatefulSet/…)
	Pod       string // container için sahibi
	Name      string
}

// String — gidiş-dönüş.
func (r Ref) String() string {
	switch r.Type {
	case TypeCluster:
		return ClusterID(r.ClusterID)
	case TypeNode:
		return NodeID(r.ClusterID, r.Name)
	case TypeNamespace:
		return NamespaceID(r.ClusterID, r.Name)
	case TypeWorkload:
		return WorkloadID(r.ClusterID, r.Namespace, r.Kind, r.Name)
	case TypePod:
		return PodID(r.ClusterID, r.Namespace, r.Name)
	case TypeContainer:
		return ContainerID(r.ClusterID, r.Namespace, r.Pod, r.Name)
	case TypeService:
		return ServiceID(r.Name)
	}
	return ""
}

// ParseID — "<önek>:<cid>/<…>" → Ref. Bilinmeyen önek / eksik bileşen →
// ok=false. Fazla '/' SON bileşende kalır (ad içinde '/' olmaz ama
// çözümleyici kırılmaz).
func ParseID(id string) (Ref, bool) {
	i := strings.IndexByte(id, ':')
	if i <= 0 || i == len(id)-1 {
		return Ref{}, false
	}
	typ, ok := prefixType[id[:i]]
	if !ok {
		return Ref{}, false
	}
	rest := id[i+1:]
	r := Ref{Type: typ}
	switch typ {
	case TypeService:
		r.Name = rest
		return r, rest != ""
	case TypeCluster:
		r.ClusterID, r.Name = rest, rest
		return r, rest != ""
	}
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" {
		return Ref{}, false
	}
	r.ClusterID = parts[0]
	tail := parts[1]
	switch typ {
	case TypeNode, TypeNamespace:
		r.Name = tail
		return r, tail != ""
	case TypePod:
		p := strings.SplitN(tail, "/", 2)
		if len(p) != 2 || p[0] == "" || p[1] == "" {
			return Ref{}, false
		}
		r.Namespace, r.Name = p[0], p[1]
		return r, true
	case TypeWorkload:
		p := strings.SplitN(tail, "/", 3)
		if len(p) != 3 || p[0] == "" || p[1] == "" || p[2] == "" {
			return Ref{}, false
		}
		r.Namespace, r.Kind, r.Name = p[0], p[1], p[2]
		return r, true
	case TypeContainer:
		p := strings.SplitN(tail, "/", 3)
		if len(p) != 3 || p[0] == "" || p[1] == "" || p[2] == "" {
			return Ref{}, false
		}
		r.Namespace, r.Pod, r.Name = p[0], p[1], p[2]
		return r, true
	}
	return Ref{}, false
}
