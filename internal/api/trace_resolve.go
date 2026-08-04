// v0.9.632 — trace çözümlemesi TEK YERDE.
//
// Operator-reported (prod, v0.9.631): Tempo fallback bir trace bulup
// waterfall'ı çizdiği hâlde "Explain trace" HTTP 404 "trace not found"
// veriyor.
//
// Sebep bugünün tekrar eden sınıfı: kural iki yere bölünmüş ve ayrışmış.
// /api/traces/{id} handler'ı ClickHouse ıskalayınca Tempo'ya düşüyordu
// (api.go, "CH miss → Tempo fallback"); explain girdisini kuran
// buildTraceExplainInput ise DOĞRUDAN s.store.GetTrace çağırıyordu.
// Coremetry trace'i örneklemeyle dışarıda bıraktıysa CH'de sıfır span
// var — detay sayfası Tempo'dan okuyup 62 span gösteriyor, explain aynı
// trace için "yok" diyor.
//
// Aynı boşluk BEŞ çağrı yerinde vardı: explain, trace paylaşım linki
// üretimi, paylaşılan snapshot görüntüleyici, span-detay ve trace
// karşılaştırma. Hepsi Tempo-only bir trace'te 404 veriyordu.
//
// NEREYE UYGULANMAZ: internal/api/tempo.go'daki iki handler. Onlar
// Grafana'nın Tempo datasource'una Tempo API'si SUNUYOR — oradan yine
// Tempo'ya düşmek döngü olurdu.
package api

import (
	"context"
	"log"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// Trace kaynağı — UI "source" rozetinde ve loglarda görünen değerler.
const (
	traceSourceCH    = "clickhouse"
	traceSourceTempo = "tempo"
)

// resolveTraceSpans — bir trace'in span'lerini ClickHouse'tan, orada
// yoksa harici Tempo'dan getirir.
//
// Dönen source çağıranın UI'a taşıdığı rozet. Boş dilim + nil hata
// "trace gerçekten yok" demek; çağıran 404'ünü ondan sonra verir.
//
// Tempo hatası isteği DÜŞÜRMEZ: operatör fallback olmasaydı ne
// görecekse onu görür (boş sonuç) ve bir [tempo] satırı yanlış
// yapılandırmayı işaret eder. Bu davranış api.go'daki özgün fallback'in
// aynısı — çıkarma işlemi, davranış değişikliği değil.
// tempoFirstBudget — Tempo'ya ÖNCE bakarken tanınan süre.
//
// Operatör kararı (v0.9.632): "bir trace'i trace_id ile sorduğumda önce
// Tempo'ya baksın". Gerekçesi sağlam — Coremetry örneklemeyle trace'leri
// dışarıda bırakabiliyor, Tempo ise tam saklıyor; elinde bir trace_id
// varken TAM trace'i istersin, örneklemeden sağ kalan parçasını değil.
//
// Bedeli: artık HER trace açılışı harici bir HTTP turuna bağlı. Bütçe
// bunu sınırlıyor — Tempo bu süre içinde cevap vermezse ClickHouse'a
// düşülür ve operatör bugünkü davranışı görür. Yani Tempo yavaşladığında
// trace görüntüleme YAVAŞLAR ama DURMAZ.
//
// 3sn: iç ağdaki bir Tempo için bolca yeterli, insan algısının
// "takıldı" eşiğinin altında.
const tempoFirstBudget = 3 * time.Second

// resolveTraceSpans — bir trace'in span'lerini getirir.
//
// SIRA (v0.9.632, operatör kararı): önce Tempo, sonra ClickHouse.
// Tempo yapılandırılmamışsa, bütçeyi aşarsa, hata verirse ya da trace'i
// bulamazsa ClickHouse'a düşülür.
//
// Dönen source çağıranın UI'a taşıdığı rozet. Boş dilim + nil hata
// "trace hiçbir yerde yok" demek; çağıran 404'ünü ondan sonra verir.
//
// Tempo hatası isteği DÜŞÜRMEZ: operatör fallback olmasaydı ne
// görecekse onu görür ve bir [tempo] satırı yanlış yapılandırmayı
// işaret eder.
func (s *Server) resolveTraceSpans(ctx context.Context, id string) ([]chstore.SpanRow, string, error) {
	if s.tempo != nil && s.tempo.Configured() {
		tctx, cancel := context.WithTimeout(ctx, tempoFirstBudget)
		tspans, terr := s.tempo.LookupTrace(tctx, id)
		cancel()
		switch {
		case terr != nil:
			// Bütçe aşımı da buraya düşer — ClickHouse'a devam.
			log.Printf("[tempo] lookup %q: %v — ClickHouse'a düşülüyor", id, terr)
		case len(tspans) > 0:
			return tspans, traceSourceTempo, nil
		}
	}
	spans, err := s.store.GetTrace(ctx, id)
	if err != nil {
		return nil, "", err
	}
	return spans, traceSourceCH, nil
}
