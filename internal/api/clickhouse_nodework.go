package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Node iş dağılımı — v0.9.543. "Hangi node ne yapıyor, ve neden CPU'su
// diğerlerinden yüksek?"
//
// Operatör soruşturması (2026-08-02, prod): dbp01'in CPU'su 6 saattir
// diğer üçünün 2-3 katı. Elle koşulan sorgularla dört hipotez öldü
// (bayat replika · veri dengesizliği · dış istemci · dış süreç) ve
// beşincisi — merge yükü — ORANSIZ çıktı: merge 1.9x iken CPU 2.76x.
// Asıl eşleşme insert'te: 116.7 mlr / 39.8 mlr = 2.93x, CPU oranını
// birebir izliyor. Bu panel o ölçümü kalıcı hale getiriyor.
//
// NEDEN system.events: prod'da query_log KAPALI (log_queries=0 →
// tablo hiç yaratılmaz; koordinatör paneli bu yüzden events'e düşüyor).
// system.events bellek-içidir, kapatılacak bir ayarı yoktur.
//
// NEDEN UNION yok: sinyal iki tablodan gelebilirdi (events + metrics)
// ama `Merge` adı İKİSİNDE DE var ve farklı şey ölçüyor (events:
// başlatılan merge sayısı, kümülatif; metrics: şu an koşan, anlık).
// Tek düz sorguda ikisi ayırt edilemezdi. Ayrıca UNION ALL'da `LIMIT`
// yalnız SON SELECT'e bağlanır — diğer dallar sınırsız kalırdı. İki
// ayrı sorgu + Go tarafında birleştirme her iki tuzağı da doğurmuyor.
//
// NEDEN ham kümülatif dönüyoruz: sayaçlar sunucu açılışından beri
// birikir. 5 günlük ortalama, son 6 saatteki rejim değişimini seyreltir
// (canlı ölçüm: ömür-boyu 0.30 çekirdek → son 6 saat 0.504, 1.7x fark).
// Pencereyi İSTEMCİ açar: sayfa açılışında taban alınır, sonraki
// yoklamalar farkı gösterir — koordinatör panelinin v0.9.506'da tam bu
// nedenle delta moduna geçmesinin aynısı.

type CHNodeWork struct {
	Host string `json:"host"`
	// Shard/Replica — system.clusters'tan, node'un KENDİ kaydı
	// (is_local). Dengesizliği doğru okumak için şart: aynı shard'ın
	// replikaları AYNI veriyi tutar, farklı shard'lar tanım gereği
	// farklı. Shard etiketi olmadan shard çarpıklığı "merge suçu" gibi
	// okunur. 0 = küme tanımlı değil ya da eşleşme bulunamadı.
	Shard   uint32 `json:"shard"`
	Replica uint32 `json:"replica"`
	// UptimeS — sayaçların kapsadığı süre. İstemci farkı bununla
	// doğrular: uptime GERİYE giderse node yeniden başlamıştır ve
	// taban geçersizdir (fark 0'a kelepçelenirse node "iş yapmıyor"
	// görünür — mümkün en yanıltıcı değer).
	UptimeS uint64 `json:"uptimeS"`

	// Ham kümülatif sayaçlar. İstemci fark alır.
	CPUMicros      uint64 `json:"cpuMicros"`      // OSCPUVirtualTimeMicroseconds
	MergeMillis    uint64 `json:"mergeMillis"`    // MergeExecuteMilliseconds
	InsertedRows   uint64 `json:"insertedRows"`   // InsertedRows
	PartFetches    uint64 `json:"partFetches"`    // ReplicatedPartFetches
	SelectedBytes  uint64 `json:"selectedBytes"`  // SelectedBytes
	MergesLaunched uint64 `json:"mergesLaunched"` // Merge (events → kümülatif)
}

type CHNodeWorkResponse struct {
	Nodes []CHNodeWork `json:"nodes"`
	Mode  string       `json:"mode"` // "cluster" | "standalone"
	// Source — "events" (okundu) | "none" (okunamadı). Ayrım şart:
	// boş tablo "her şey dengeli" diye okunmamalı.
	Source string `json:"source"`
	// ShardsKnown — shard etiketleri çözülebildi mi. False ise UI
	// dengesizliği shard-içi hesaplayamaz ve bunu SÖYLEMELİ.
	ShardsKnown bool `json:"shardsKnown"`
	// ExpectedNodes — system.clusters'ın bildirdiği node sayısı.
	// len(Nodes) bundan azsa bir node yanıt vermemiştir; eksik küme
	// "dengeli" okunursa tam da aradığımız node gözden kaçar.
	ExpectedNodes int    `json:"expectedNodes"`
	Note          string `json:"note,omitempty"`
	GeneratedAt   int64  `json:"generatedAt"`
}

// nodeWorkQuery — saf SQL üreteci (coordinatorQuery deseni: canlı CH
// olmadan test edilebilsin).
//
// sumIf her sayaç için ayrı: system.events'te satır başına bir event
// vardır, yani GROUP BY host + sumIf doğal şekil. Bilinmeyen ad hata
// değil 0 üretir — sürüm farkı paneli KIRMAZ, ama o sayaç için "sıfır"
// ile "yok" ayrımı da kaybolur; çağıran bunu Note ile telafi eder.
func nodeWorkQuery(clusterName string) string {
	source := "system.events"
	if cn := strings.TrimSpace(clusterName); cn != "" {
		source = "clusterAllReplicas('" + cn + "', system.events)"
	}
	return `
		SELECT hostName()                                            AS host,
		       any(uptime())                                         AS uptime_s,
		       sumIf(value, event = 'OSCPUVirtualTimeMicroseconds')   AS cpu_micros,
		       sumIf(value, event = 'MergeExecuteMilliseconds')       AS merge_millis,
		       sumIf(value, event = 'InsertedRows')                   AS inserted_rows,
		       sumIf(value, event = 'ReplicatedPartFetches')          AS part_fetches,
		       sumIf(value, event = 'SelectedBytes')                  AS selected_bytes,
		       sumIf(value, event = 'Merge')                          AS merges_launched
		FROM ` + source + `
		GROUP BY host
		ORDER BY host
		LIMIT 64
		SETTINGS max_execution_time = 5`
}

// nodeShardQuery — her node KENDİ shard/replika kaydını bildirir
// (is_local). clusterAllReplicas + is_local, IP↔hostname eşlemesi
// gerektirmeden hostName() ile doğrudan eşleşme verir.
func nodeShardQuery(clusterName string) string {
	cn := strings.TrimSpace(clusterName)
	if cn == "" {
		return ""
	}
	return `
		SELECT hostName() AS host, shard_num, replica_num
		FROM clusterAllReplicas('` + cn + `', system.clusters)
		WHERE cluster = '` + cn + `' AND is_local
		ORDER BY host
		LIMIT 64
		SETTINGS max_execution_time = 5`
}

// GET /api/admin/clickhouse/nodework
func (s *Server) getCHNodeWork(w http.ResponseWriter, r *http.Request) {
	// Girdi yok → sabit anahtar. TTL 30s: panel delta modunda çalışıyor
	// ve istemci 30s'de bir yokluyor; daha kısa TTL her yoklamayı
	// CH'ye indirirdi, daha uzunu farkı bayatlatırdı.
	s.serveCached(w, r, "ch:nodework:v1", 30*time.Second, func(ctx context.Context) (any, error) {
		cn := strings.TrimSpace(s.store.ClusterName())
		out := CHNodeWorkResponse{
			Mode:        "standalone",
			Source:      "events",
			Nodes:       []CHNodeWork{},
			GeneratedAt: time.Now().UnixNano(),
		}
		if cn != "" {
			out.Mode = "cluster"
		}

		rows, err := s.store.Conn().Query(ctx, nodeWorkQuery(cn))
		if err != nil {
			out.Source = "none"
			out.Note = "Ölçüm okunamadı — system.events: " + err.Error()
			return out, nil
		}
		for rows.Next() {
			var n CHNodeWork
			if err := rows.Scan(&n.Host, &n.UptimeS, &n.CPUMicros, &n.MergeMillis,
				&n.InsertedRows, &n.PartFetches, &n.SelectedBytes, &n.MergesLaunched); err != nil {
				continue
			}
			out.Nodes = append(out.Nodes, n)
		}
		rows.Close()

		if len(out.Nodes) == 0 {
			out.Note = "system.events okundu ama satır dönmedi — ölçüm YAPILAMADI, " +
				"bunu 'yük dengeli' diye okuma."
			return out, nil
		}

		// Shard etiketleri — best-effort. Okunamazsa panel çalışır ama
		// dengesizliği shard-içi hesaplayamaz ve UI bunu söyler.
		if q := nodeShardQuery(cn); q != "" {
			if srows, serr := s.store.Conn().Query(ctx, q); serr == nil {
				byHost := map[string][2]uint32{}
				for srows.Next() {
					var h string
					var sh, rp uint32
					if err := srows.Scan(&h, &sh, &rp); err == nil {
						byHost[h] = [2]uint32{sh, rp}
					}
				}
				srows.Close()
				for i := range out.Nodes {
					if v, ok := byHost[out.Nodes[i].Host]; ok {
						out.Nodes[i].Shard, out.Nodes[i].Replica = v[0], v[1]
						out.ShardsKnown = true
					}
				}
				out.ExpectedNodes = len(byHost)
			}
		}
		if out.ExpectedNodes == 0 {
			out.ExpectedNodes = len(out.Nodes)
		}
		if out.ExpectedNodes > len(out.Nodes) {
			out.Note = fmt.Sprintf(
				"%d node'dan %d'i yanıt verdi — EKSİK küme. Yanıt vermeyen node "+
					"tam da en yüklü olan olabilir; dengesizlik oranları bu hâlde güvenilmez.",
				out.ExpectedNodes, len(out.Nodes))
		}
		return out, nil
	})
}

