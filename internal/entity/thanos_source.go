package entity

import (
	"context"
	"fmt"

	"github.com/cilcenk/coremetry/internal/thanos"
)

// thanos_source.go — Source adaptörü: Remote Cluster listesi + altı
// seçicili anlık sorgu (cluster matcher thanos.doQuery'de). Her tick
// CurrentSettings() okunur → Settings'ten eklenen cluster restart'sız
// gelir, kaldırılan sync edilmez. Bir seri hatası cluster'ı "failed"
// yapar (kısmi anlık görüntüyle ömür kapatmak yanlış kapanış üretirdi).

type ThanosSource struct{ svc *thanos.Service }

func NewThanosSource(svc *thanos.Service) *ThanosSource { return &ThanosSource{svc: svc} }

func (t *ThanosSource) Clusters() []ClusterRef {
	if t == nil || t.svc == nil {
		return nil
	}
	cfg := t.svc.CurrentSettings()
	out := make([]ClusterRef, 0, len(cfg.Clusters))
	for _, c := range cfg.Clusters {
		if !c.Enabled || c.URL == "" {
			continue
		}
		out = append(out, ClusterRef{ID: c.EffectiveID(), Name: c.Name, NamespaceFilter: c.NamespaceFilter, SpanClusterValue: c.SpanClusterKey(), SpanClusterValues: c.SpanClusterKeys()})
	}
	return out
}

func (t *ThanosSource) Fetch(ctx context.Context, c ClusterRef, queries map[string]string) (SampleSets, error) {
	cfg, ok := t.svc.ClusterByID(c.ID)
	if !ok {
		return SampleSets{}, fmt.Errorf("cluster %s artık etkin değil", c.ID)
	}
	get := func(name string) ([]Sample, error) {
		raw, err := t.svc.InstantSamples(ctx, cfg, queries[name])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out := make([]Sample, len(raw))
		for i, s := range raw {
			out[i] = Sample{Labels: s.Labels, Value: s.Value}
		}
		return out, nil
	}
	var ss SampleSets
	var err error
	if ss.NodeInfo, err = get("node_info"); err != nil {
		return SampleSets{}, err
	}
	if ss.PodInfo, err = get("pod_info"); err != nil {
		return SampleSets{}, err
	}
	if ss.PodOwner, err = get("pod_owner"); err != nil {
		return SampleSets{}, err
	}
	if ss.RSOwner, err = get("rs_owner"); err != nil {
		return SampleSets{}, err
	}
	if ss.JobOwner, err = get("job_owner"); err != nil {
		return SampleSets{}, err
	}
	if ss.ContainerInfo, err = get("container_info"); err != nil {
		return SampleSets{}, err
	}
	return ss, nil
}
