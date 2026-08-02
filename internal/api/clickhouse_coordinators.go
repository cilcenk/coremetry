package api

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

// Koordinatör dağılımı — hangi CH node'u KAÇ sorgunun giriş noktası
// oldu. "Giriş noktası" = system.query_log'da is_initial_query = 1
// olan satır: Distributed bir sorguda yalnız koordinatör böyle
// loglar, shard'ların yaptığı alt-sorgular is_initial_query = 0'dır.
// Yani bu panel taramanın nerede yapıldığını DEĞİL, parse + fan-out +
// partial-state merge + final aggregation/sort/limit yükünün nerede
// biriktiğini ölçer.
//
// Neden ayrı bir uç (v0.9.494): /admin/clickhouse gövdesi 5s TTL ile
// dönüyor ve içindeki query_log okuması yalnız > 500ms yavaş sorguları
// süzüyor. Buradaki okuma PENCEREDEKİ TÜM sorguları grupluyor — çok
// daha geniş bir satır kümesi. 5s'te bir koşturmak admin sayfasına
// gereksiz maliyet bindirir; teşhis panelinin 5 saniyelik tazeliğe
// ihtiyacı da yok. Kendi ucu + 60s TTL.
//
// SELECT ve INSERT ayrı sayılıyor çünkü ikisi bugün FARKLI havuzlardan
// çıkıyor (internal/chstore/store.go:399-435):
//   - INSERT'ler ingest havuzunda, ConnOpenRoundRobin → 4 node'a yayılır
//     (v0.9.481'in kazanımı; bu panelde dengeli görünmeli)
//   - SELECT'ler ana bağlantıda, ConnOpenInOrder → hep ilk node
//     (açık kalem; okuma havuzu dilimi bunu düzeltecek)
// Operatör tek tabloda "insert'lerim dağıldı, select'lerim dağılmadı"
// ayrımını görebilsin diye ikisi yan yana.
type CHCoordinator struct {
	Host string `json:"host"`
	// Initial — bu node'un GİRİŞ NOKTASI olduğu sorgu sayısı. Panelin
	// asıl sinyali. query_log yolunda is_initial_query=1 sayımı,
	// events yolunda ProfileEvent `InitialQuery`. İkisi aynı şeyi sayar.
	Initial  uint64  `json:"initial"`
	Selects  uint64  `json:"selects"`
	Inserts  uint64  `json:"inserts"`
	Other    uint64  `json:"other"`
	ReadRows uint64  `json:"readRows"`
	MemoryMB float64 `json:"memoryMB"`
	// P50/P95 yalnız SELECT'ler üzerinden — INSERT süreleri karışırsa
	// okuma gecikmesi okunamaz hale gelir. events yolunda 0 (o kaynakta
	// süre dağılımı yok).
	P50Ms float64 `json:"p50Ms"`
	P95Ms float64 `json:"p95Ms"`
	// UptimeS — yalnız events yolunda anlamlı. Sayaçlar sunucu
	// açılışından beri biriktiği için, yeni restart etmiş bir node
	// DÜŞÜK okunur; operatör dengesizliği uptime'a bakarak yorumlar.
	UptimeS uint64 `json:"uptimeS,omitempty"`
}

type CHCoordinatorSpread struct {
	Nodes   []CHCoordinator `json:"nodes"`
	Mode    string          `json:"mode"`    // "cluster" | "standalone"
	WindowS int64           `json:"windowS"` // ölçüm penceresi
	// SelectImbalance / InsertImbalance = maxNode / ortalamaNode.
	// 1.00 = kusursuz dağılım. N node'da tek node her şeyi alıyorsa
	// değer N'e yaklaşır. Tek node'lu kurulumda 1.0 (anlamsız ama
	// yanıltıcı değil) ve Mode = "standalone" ile birlikte okunur.
	SelectImbalance float64 `json:"selectImbalance"`
	InsertImbalance float64 `json:"insertImbalance"`
	// InitialImbalance — panelin ASIL rakamı. Giriş-noktası sayısı en
	// temiz koordinatör sinyali: shard'ların alt-sorguları buna girmez.
	InitialImbalance float64 `json:"initialImbalance"`
	// Source — "query_log" (pencereli, zengin) | "events" (açılıştan
	// beri kümülatif, süre/satır dağılımı yok). Prod'da query_log
	// çoğu kurulumda KAPALI (log_queries=0) ya da tablo hiç oluşmamış
	// olur; panel o zaman sessizce boş kalmak yerine events'e düşer.
	Source string `json:"source"`
	// Note — panelin altında gösterilen insan-dili durum cümlesi.
	// Boş değilse UI onu aynen basar; ölçüm yapılamadıysa NEDEN
	// yapılamadığını söyler (boş panel + sessizlik yerine).
	Note        string `json:"note,omitempty"`
	GeneratedAt int64  `json:"generatedAt"`
}

// imbalance = max / mean. Boş veya tek elemanlı kümede 1.0.
func imbalance(counts []uint64) float64 {
	if len(counts) < 2 {
		return 1
	}
	var sum, max uint64
	for _, c := range counts {
		sum += c
		if c > max {
			max = c
		}
	}
	if sum == 0 {
		return 1
	}
	mean := float64(sum) / float64(len(counts))
	if mean == 0 {
		return 1
	}
	return math.Round(float64(max)/mean*100) / 100
}

// coordinatorQuery — saf SQL üreteci (test edilebilirlik için ayrı).
//
// clusterName boşsa yalnız bağlı olduğumuz node okunur; doluysa
// clusterAllReplicas ile HER node'a gidilir.
//
// **cluster() DEĞİL, clusterAllReplicas():** cluster() her shard'dan
// TEK replika okur → 2 shard × 2 replika = 4 node'lu kurulumda panel
// hep 2 node gösterir. Bu tam olarak v0.9.454'te disk panelinde
// yaşanan operatör bug'ı ("4 node'lu cluster'ımda yalnız 2 node'un
// diskini görüyorum"). query_log node-düzeyi bir kayıttır ve burada
// veri çift sayımı riski yoktur (parts'ın aksine), o yüzden tüm
// replikalara gitmek hem doğru hem güvenli.
//
// windowS çağıran tarafından [300, 86400] aralığına kelepçelenmiş
// olarak gelir — Sprintf ile gömmek güvenli.
func coordinatorQuery(clusterName string, windowS int64) string {
	source := "system.query_log"
	if clusterName != "" {
		source = "clusterAllReplicas('" + clusterName + "', system.query_log)"
	}
	return fmt.Sprintf(`
		SELECT hostName()                                                AS host,
		       count()                                                   AS initial,
		       countIf(query_kind = 'Select')                            AS selects,
		       countIf(query_kind = 'Insert')                            AS inserts,
		       countIf(query_kind NOT IN ('Select', 'Insert'))           AS other,
		       sum(read_rows)                                            AS read_rows,
		       sum(memory_usage) / 1048576.0                             AS memory_mb,
		       quantileTDigestIf(0.5)(query_duration_ms,  query_kind = 'Select') AS p50_ms,
		       quantileTDigestIf(0.95)(query_duration_ms, query_kind = 'Select') AS p95_ms
		FROM %s
		WHERE event_time >= now() - INTERVAL %d SECOND
		  AND type = 'QueryFinish'
		  AND is_initial_query = 1
		GROUP BY host
		ORDER BY selects DESC, host
		LIMIT 64
		SETTINGS max_execution_time = 5`, source, windowS)
}

// coordinatorEventsQuery — query_log YOKKEN kullanılan geri-düşüş.
//
// system.query_log çoğu sertleştirilmiş kurulumda yoktur: `log_queries=0`
// ise tablo hiç yaratılmaz ve okuma UNKNOWN_TABLE verir (operatör prod'da
// tam bunu gördü). system.events her zaman vardır ve içinde
// `InitialQuery` ProfileEvent'i query_log'daki is_initial_query=1'in
// birebir karşılığıdır — yani asıl sinyal kaybolmuyor.
//
// Farklar (yanıtta Source ile bildiriliyor, UI söylüyor):
//   - Sayaçlar SUNUCU AÇILIŞINDAN BERİ kümülatiftir, pencereli değil →
//     uptime da okunur, yeni restart etmiş node düşük görünür.
//   - SelectQuery/InsertQuery bu node'da ÇALIŞAN tüm sorguları sayar,
//     shard olarak yaptığı alt-sorgular DAHİL. Yani onlar bir üst sınır;
//     temiz koordinatör sinyali InitialQuery'dir.
//   - Süre/bellek/satır dağılımı yok.
func coordinatorEventsQuery(clusterName string) string {
	source := "system.events"
	if clusterName != "" {
		source = "clusterAllReplicas('" + clusterName + "', system.events)"
	}
	return fmt.Sprintf(`
		SELECT hostName()                                AS host,
		       toUInt64(any(uptime()))                   AS uptime_s,
		       sumIf(value, event = 'InitialQuery')      AS initial,
		       sumIf(value, event = 'SelectQuery')       AS selects,
		       sumIf(value, event = 'InsertQuery')       AS inserts
		FROM %s
		WHERE event IN ('InitialQuery', 'SelectQuery', 'InsertQuery')
		GROUP BY host
		ORDER BY initial DESC, host
		LIMIT 64
		SETTINGS max_execution_time = 5`, source)
}

// GET /api/admin/clickhouse/coordinators?windowS=3600
func (s *Server) getCHCoordinatorSpread(w http.ResponseWriter, r *http.Request) {
	windowS := int64(3600)
	if v := r.URL.Query().Get("windowS"); v != "" {
		// 5 dk .. 24 saat. Alt sınır: daha darı istatistiksel gürültü.
		// Üst sınır: query_log retention'ı tipik olarak birkaç gün ve
		// 24 saatten geniş bir GROUP BY prod'da pahalıya kaçar.
		if n := parseInt(v, 0); n >= 300 && n <= 86400 {
			windowS = int64(n)
		}
	}
	// Cache anahtarı TÜM girdileri taşır (v0.5.187 dersi) — burada tek
	// değişken girdi pencere.
	key := fmt.Sprintf("ch:coord:v1:window=%d", windowS)
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		out := CHCoordinatorSpread{
			Mode:            "standalone",
			WindowS:         windowS,
			SelectImbalance: 1,
			InsertImbalance: 1,
			GeneratedAt:     time.Now().UnixNano(),
			Nodes:           []CHCoordinator{},
		}

		cn := strings.TrimSpace(s.store.ClusterName())
		if cn != "" {
			out.Mode = "cluster"
		}

		out.Source = "query_log"
		rows, qerr := s.store.Conn().Query(ctx, coordinatorQuery(cn, windowS))
		if qerr == nil {
			var selects, inserts, initial []uint64
			for rows.Next() {
				var c CHCoordinator
				if err := rows.Scan(&c.Host, &c.Initial, &c.Selects, &c.Inserts, &c.Other,
					&c.ReadRows, &c.MemoryMB, &c.P50Ms, &c.P95Ms); err != nil {
					continue
				}
				selects = append(selects, c.Selects)
				inserts = append(inserts, c.Inserts)
				initial = append(initial, c.Initial)
				out.Nodes = append(out.Nodes, c)
			}
			rows.Close()
			out.SelectImbalance = imbalance(selects)
			out.InsertImbalance = imbalance(inserts)
			out.InitialImbalance = imbalance(initial)
			if len(out.Nodes) == 0 {
				out.Note = "Pencerede QueryFinish kaydı yok — query_log boş ya da pencere çok dar."
			}
			return out, nil
		}

		// query_log YOK ya da okunamıyor. Sertleştirilmiş kurulumlarda
		// olağan hal: log_queries=0 ise tablo hiç yaratılmaz ve okuma
		// UNKNOWN_TABLE verir (operatör prod'da tam bunu gördü). Panel
		// burada boş kalmak yerine system.events'e düşer — orada
		// `InitialQuery` ProfileEvent'i is_initial_query=1'in birebir
		// karşılığı, yani asıl sinyal korunuyor.
		out.Source = "events"
		erows, eerr := s.store.Conn().Query(ctx, coordinatorEventsQuery(cn))
		if eerr != nil {
			out.Source = "none"
			out.Note = "Ölçüm okunamadı. query_log: " + qerr.Error() +
				" · system.events: " + eerr.Error() +
				" — kimliğin system tablolarına erişimi yok gibi görünüyor."
			return out, nil
		}
		defer erows.Close()

		var einit, esel, eins []uint64
		for erows.Next() {
			var c CHCoordinator
			if err := erows.Scan(&c.Host, &c.UptimeS, &c.Initial, &c.Selects, &c.Inserts); err != nil {
				continue
			}
			einit = append(einit, c.Initial)
			esel = append(esel, c.Selects)
			eins = append(eins, c.Inserts)
			out.Nodes = append(out.Nodes, c)
		}
		out.InitialImbalance = imbalance(einit)
		out.SelectImbalance = imbalance(esel)
		out.InsertImbalance = imbalance(eins)
		out.Note = "query_log bu kümede yok (log_queries kapalı) — sayımlar " +
			"system.events'ten geliyor: SUNUCU AÇILIŞINDAN BERİ kümülatif, " +
			"pencere değil. Yeni restart etmiş node düşük okunur (uptime kolonu). " +
			"SELECT/INSERT sayıları shard alt-sorgularını da içerir; temiz " +
			"koordinatör sinyali 'Entry' kolonudur."
		if len(out.Nodes) == 0 {
			out.Note = "system.events boş döndü — küme adı doğru mu?"
		}
		return out, nil
	})
}
