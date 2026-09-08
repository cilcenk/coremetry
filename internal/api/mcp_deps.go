// v0.9.1147 (AI Faz 3.4) — api'nin mcptools'a bakan TEK kapısı.
//
// Neden ayrı bir yardımcı: mcptools artık iki farklı işle çağrılıyor ve
// ikisi de aynı iki handle'ı istiyor —
//
//	(a) tool KATALOĞU: in-app sohbetin function-calling spec listesi
//	    (copilot_chat.go, ToolList),
//	(b) ORTAK VERİ KATMANI: guided kanıt paketlerinin okumaları
//	    (mcptools.ReadDBHealth / ReadMessagingHealth / ReadPodHealth /
//	    ReadProblemWindowEvents — Faz 3.4 ile guided ve MCP aynı
//	    okumadan besleniyor, D6).
//
// Deps'i her çağrı yerinde elle kurmak, yeni bir bağımlılık (ör. Tempo)
// eklendiğinde bazı yolların onu taşımadığı bir ayrışma üretir; tek
// kurucu bunu yapısal olarak imkânsız kılıyor.
//
// İMPORT YÖNÜ: api → mcptools, hep. mcptools api'yi import ETMEZ (aksi
// halde döngü olur ve /topology'nin gizli-kalıp matcher'ı bu yüzden
// mcptools'a taşınamadı — analysis.go'daki bilinçli sapma).
package api

import (
	"context"
	"fmt"
	"time"

	"github.com/cilcenk/coremetry/internal/mcptools"
	"github.com/cilcenk/coremetry/internal/thanos"
)

// mcpDeps — tool kataloğunun ve ortak veri katmanının kapandığı
// handle'lar. Ucuz: yalnız üç işaretçi kopyalar, her çağrıda kurulabilir.
//
// v0.9.1150 — Metrics: metrik okuma ROUTER'ı (CH ya da VictoriaMetrics).
// Bu TEK kurucunun varlık sebebinin somut örneği: query_metric ile
// list_metric_names farklı yerlerden kurulsaydı biri VM'den ad alıp
// öbürü CH'ye sorabilirdi. mcptools tarafındaki nil-fallback CH'dir,
// yani buradaki atamayı unutmak operatörün seçimini SESSİZCE iptal
// eder — mcp_deps_test.go tam olarak bunu ısırıyor.
func (s *Server) mcpDeps() mcptools.Deps {
	return mcptools.Deps{
		Store: s.store, LogStore: s.logs, Metrics: s.metricSource(),
		// v0.10.468 (Faz 2, F2-1) — varlık kataloğu tool'ları: etkin Remote
		// Cluster'lar + entity_layer bayrağı (nil-güvenli; her ikisi de
		// yoksa tool'lar dürüst disabled/boş döner).
		Clusters:      s.mcpClusterRefs,
		EntityEnabled: func() bool { return s.entitySettings != nil && s.entitySettings.Resolved().Enabled },
		// v0.10.478 (Faz 4, F4-1) — sohbet bağlamı (chat_context.go); ctx'te state yoksa tool dürüst hata.
		CtxGet: s.chatContextGet, CtxSet: s.chatContextSet, CtxClear: s.chatContextClear,
		// v0.10.555 (Faz 4a) — get_capabilities probları.
		RolloutsEnabled: func() bool { return s.rolloutCfg != nil && s.rolloutCfg.Resolved().Enabled },
		MetricsName:     func() string { return s.metricSource().Name() },
		RAGReady:        func() bool { return s.rag != nil && s.rag.Ready() },
		ClusterMetrics:  s.mcpClusterMetricsOrNil(), // v0.10.556
		CopilotModel: func() string {
			if s.copilot == nil || !s.copilot.Configured() {
				return ""
			}
			return s.copilot.ActiveModel()
		},
	}
}

// mcpClusterRefs — thanos.ClusterConfig → mcptools.ClusterRef (yalnız etkin).
func (s *Server) mcpClusterRefs() []mcptools.ClusterRef {
	if s.thanos == nil {
		return nil
	}
	cfg := s.thanos.CurrentSettings()
	out := make([]mcptools.ClusterRef, 0, len(cfg.Clusters))
	for _, c := range cfg.Clusters {
		if !c.Enabled {
			continue
		}
		out = append(out, mcptools.ClusterRef{ID: c.EffectiveID(), Name: c.Name, SpanValues: c.SpanClusterKeys()})
	}
	return out
}

// mcpClusterMetrics — v0.10.556: cluster_metric aracının Thanos adaptörü. Kalkanlar
// HTTP handler'larıyla aynı: cluster referansı çözümü (ClusterByRef), 30 g pencere
// tavanı (clampThanosWindow), 10 s zaman aşımı, nokta basamağı
// (TrendMaxDataPointsRung). Ham PromQL kabul etmez.
type mcpClusterMetrics struct{ s *Server }

func (s *Server) mcpClusterMetricsOrNil() mcptools.ClusterMetricReader {
	if s.thanos == nil {
		return nil
	}
	return mcpClusterMetrics{s}
}

func (m mcpClusterMetrics) cfg(ref string) (thanos.ClusterConfig, error) {
	if m.s.thanos == nil || !m.s.thanos.HasEnabledClusters() {
		return thanos.ClusterConfig{}, fmt.Errorf("no thanos clusters configured")
	}
	cfg, ok := m.s.thanos.ClusterByRef(ref)
	if !ok {
		return thanos.ClusterConfig{}, fmt.Errorf("unknown or disabled cluster %q", ref)
	}
	return cfg, nil
}

func mcpThanosCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 10*time.Second)
}

func (m mcpClusterMetrics) PodTrend(ctx context.Context, cluster, namespace, pod string, from, to time.Time) ([]thanos.TrendPoint, error) {
	cfg, err := m.cfg(cluster)
	if err != nil {
		return nil, err
	}
	from, to, _ = clampThanosWindow(from, to)
	qctx, cancel := mcpThanosCtx(ctx)
	defer cancel()
	return m.s.thanos.PodTrend(qctx, cfg, namespace, pod, from, to)
}

func (m mcpClusterMetrics) NamespaceTrend(ctx context.Context, cluster, namespace string, from, to time.Time) ([]thanos.TrendPoint, error) {
	cfg, err := m.cfg(cluster)
	if err != nil {
		return nil, err
	}
	from, to, _ = clampThanosWindow(from, to)
	qctx, cancel := mcpThanosCtx(ctx)
	defer cancel()
	return m.s.thanos.NamespaceTrend(qctx, cfg, namespace, from, to)
}

func (m mcpClusterMetrics) DeployTrend(ctx context.Context, cluster, namespace, deploy, metric string, byPod bool, from, to time.Time, mdp int) ([]thanos.NamedSeries, int, error) {
	cfg, err := m.cfg(cluster)
	if err != nil {
		return nil, 0, err
	}
	from, to, _ = clampThanosWindow(from, to)
	qctx, cancel := mcpThanosCtx(ctx)
	defer cancel()
	return m.s.thanos.DeployTrend(qctx, cfg, namespace, deploy, metric, byPod, from, to, thanos.TrendMaxDataPointsRung(mdp))
}

func (m mcpClusterMetrics) NetworkTrend(ctx context.Context, cluster string, from, to time.Time) ([]thanos.NetTrendPoint, error) {
	cfg, err := m.cfg(cluster)
	if err != nil {
		return nil, err
	}
	from, to, _ = clampThanosWindow(from, to)
	qctx, cancel := mcpThanosCtx(ctx)
	defer cancel()
	return m.s.thanos.NetworkTrend(qctx, cfg, from, to)
}
