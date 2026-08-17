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
// handle'lar. Ucuz: yalnız iki işaretçi kopyalar, her çağrıda kurulabilir.
func (s *Server) mcpDeps() mcptools.Deps {
	return mcptools.Deps{Store: s.store, LogStore: s.logs}
}
