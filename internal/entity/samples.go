package entity

// samples.go — THANOS YANITI → SNAPSHOT (saf). Etiket adları KSM'nin
// kendi adları; matcher/namespace süzgeci sorguya doQuery'de eklenir.

// Sample — bir anlık-sorgu satırı (thanos.Sample'ın paket-içi aynası;
// thanos paketine bağımlılık taşımamak için kopya).
type Sample struct {
	Labels map[string]string
	Value  float64
}

// SampleSets — altı serinin yanıtları (kısmi olabilir).
type SampleSets struct {
	NodeInfo, PodInfo, PodOwner, RSOwner, JobOwner, ContainerInfo []Sample
}

// SnapshotQueries — seri adı → PromQL. Her sorgu seçici taşır (filtresiz
// seri taraması YOK); nsMatcher `,namespace=~"…"` biçiminde (thanos.
// nsMatcher çıktısı) ya da boş. kube_node_info namespace etiketi taşımaz.
func SnapshotQueries(nsMatcher string) map[string]string {
	return map[string]string{
		"node_info":      `kube_node_info{node!=""}`,
		"pod_info":       `kube_pod_info{pod!=""` + nsMatcher + `}`,
		"pod_owner":      `kube_pod_owner{pod!=""` + nsMatcher + `}`,
		"rs_owner":       `kube_replicaset_owner{replicaset!=""` + nsMatcher + `}`,
		"job_owner":      `kube_job_owner{job_name!=""` + nsMatcher + `}`,
		"container_info": `kube_pod_container_info{container!=""` + nsMatcher + `}`,
	}
}

// SnapshotFromSamples — etiket setleri → Snapshot; namespace'siz/adsız
// satır düşer, tekrarlar tekilleşir (ilk görülen kazanır).
func SnapshotFromSamples(ss SampleSets) Snapshot {
	var out Snapshot
	seen := map[string]bool{}
	first := func(kind string, keys ...string) bool {
		k := kind
		for _, x := range keys {
			k += "\x00" + x
		}
		if seen[k] {
			return false
		}
		seen[k] = true
		return true
	}
	for _, s := range ss.NodeInfo {
		l := s.Labels
		if l["node"] == "" || !first("node", l["node"]) {
			continue
		}
		out.Nodes = append(out.Nodes, NodeInfo{Node: l["node"], InternalIP: l["internal_ip"],
			KernelVersion: l["kernel_version"], OSImage: l["os_image"], SystemUUID: l["system_uuid"]})
	}
	for _, s := range ss.PodInfo {
		l := s.Labels
		if l["namespace"] == "" || l["pod"] == "" || !first("pod", l["namespace"], l["pod"]) {
			continue
		}
		out.Pods = append(out.Pods, PodInfo{Namespace: l["namespace"], Pod: l["pod"], UID: l["uid"], Node: l["node"],
			IP: l["pod_ip"], CreatedByKind: l["created_by_kind"], CreatedByName: l["created_by_name"]})
	}
	for _, s := range ss.PodOwner {
		l := s.Labels
		if l["namespace"] == "" || l["pod"] == "" || !first("po", l["namespace"], l["pod"]) {
			continue
		}
		out.PodOwners = append(out.PodOwners, PodOwner{Namespace: l["namespace"], Pod: l["pod"],
			OwnerKind: l["owner_kind"], OwnerName: l["owner_name"]})
	}
	for _, s := range ss.RSOwner {
		l := s.Labels
		if l["namespace"] == "" || l["replicaset"] == "" || !first("rs", l["namespace"], l["replicaset"]) {
			continue
		}
		out.RSOwners = append(out.RSOwners, RSOwner{Namespace: l["namespace"], ReplicaSet: l["replicaset"],
			OwnerKind: l["owner_kind"], OwnerName: l["owner_name"]})
	}
	for _, s := range ss.JobOwner {
		l := s.Labels
		if l["namespace"] == "" || l["job_name"] == "" || !first("job", l["namespace"], l["job_name"]) {
			continue
		}
		out.JobOwners = append(out.JobOwners, JobOwner{Namespace: l["namespace"], Job: l["job_name"],
			OwnerKind: l["owner_kind"], OwnerName: l["owner_name"]})
	}
	for _, s := range ss.ContainerInfo {
		l := s.Labels
		if l["namespace"] == "" || l["pod"] == "" || l["container"] == "" || !first("ctr", l["namespace"], l["pod"], l["container"]) {
			continue
		}
		out.Containers = append(out.Containers, ContainerInfo{Namespace: l["namespace"], Pod: l["pod"],
			Container: l["container"], Image: l["image"]})
	}
	return out
}
