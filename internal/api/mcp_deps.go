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

import "github.com/cilcenk/coremetry/internal/mcptools"

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
