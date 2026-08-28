package thanos

// pod_containers.go — v0.10.135 (DETAY SAYFALARI adım 1: pod detay).
// Pod'un konteyner durumları KSM'den anlık: ready, restart sayısı, bekleme
// sebebi, son sonlanma sebebi. Best-effort katmanlı: ready sorgusu hata
// verirse hata döner (Thanos yok/kapalı = panel "bilinmiyor" der); diğer
// üçü KSM sürümüne göre eksik kalabilir, alanlar boş kalır. Her sorgu
// doQuery'den geçer → cluster matcher enjeksiyonu (görev kısıtı) burada da
// geçerli.

import (
	"context"
	"sort"
)

type ContainerStatus struct {
	Name           string `json:"name"`
	Ready          bool   `json:"ready"`
	ReadyKnown     bool   `json:"readyKnown"`
	Restarts       int    `json:"restarts"`
	WaitingReason  string `json:"waitingReason,omitempty"`
	LastTermReason string `json:"lastTermReason,omitempty"`
}

func (s *Service) PodContainers(ctx context.Context, c ClusterConfig, ns, pod string) ([]ContainerStatus, error) {
	byName := map[string]*ContainerStatus{}
	get := func(smp Sample) *ContainerStatus {
		n := smp.Labels["container"]
		if n == "" {
			return nil
		}
		cs, ok := byName[n]
		if !ok {
			cs = &ContainerStatus{Name: n}
			byName[n] = cs
		}
		return cs
	}
	ready, err := s.InstantSamples(ctx, c, containerReadyQuery(ns, pod))
	if err != nil {
		return nil, err
	}
	for _, smp := range ready {
		if cs := get(smp); cs != nil {
			cs.ReadyKnown = true
			cs.Ready = smp.Value >= 1
		}
	}
	if rs, err := s.InstantSamples(ctx, c, containerRestartsQuery(ns, pod)); err == nil {
		for _, smp := range rs {
			if cs := get(smp); cs != nil {
				cs.Restarts = int(smp.Value)
			}
		}
	}
	if ws, err := s.InstantSamples(ctx, c, containerWaitingQuery(ns, pod)); err == nil {
		for _, smp := range ws {
			if cs := get(smp); cs != nil {
				cs.WaitingReason = smp.Labels["reason"]
			}
		}
	}
	if ls, err := s.InstantSamples(ctx, c, containerLastTermQuery(ns, pod)); err == nil {
		for _, smp := range ls {
			if cs := get(smp); cs != nil {
				cs.LastTermReason = smp.Labels["reason"]
			}
		}
	}
	out := make([]ContainerStatus, 0, len(byName))
	for _, cs := range byName {
		out = append(out, *cs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
