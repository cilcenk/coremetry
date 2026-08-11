package api

// Databases + messaging read handlers. Split out of api.go for
// code organisation (behaviour-preserving). All handlers hit the
// pre-aggregated db_*_5m / messaging MVs via chstore and front the
// read with s.serveCached. Shared helpers (parseFromTo, cacheBucket)
// stay in api.go because many other clusters use them too.

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func (s *Server) getDatabases(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, time.Hour)
	// v0.9.433 (desen paritesi, kuyruk #3a) — ?compare=prior: messaging
	// v0.8.364 sözleşmesinin birebiri. Opt-in (CH maliyeti ikiye katlar);
	// Prior hatası ölümcül değil: sayfa delta'sız gelir, 500 olmaz.
	compare := r.URL.Query().Get("compare") == "prior"
	// v0.9.821 — global Topbar env filtresi. Bugüne dek bu uç env'i
	// SESSİZCE YOK SAYIYORDU: /endpoints ve /services daralırken
	// /databases tüm ortamları göstermeye devam ediyor ve hiçbir şey
	// bunu söylemiyordu. db_summary_5m'de deploy_env boyutu olmadığı
	// için env okumayı ham span'lere düşürüyor (endpoints v0.8.385 ile
	// aynı forcesRaw takası) — zarf kaynağı ilan ediyor.
	env := strings.TrimSpace(r.URL.Query().Get("env"))
	// v0.9.821 — anahtar öneki v2'ye çıktı ÇÜNKÜ zarf değişti (çıplak
	// dizi → DatabasesOverview). Önek sürümlenmeseydi yuvarlanan deploy
	// sırasında eski dizi payload'ı 30 sn boyunca yeni SPA'ya servis
	// edilir ve sayfa boş açılırdı (v0.9.443/458 + messaging v0.9.813
	// dersi). Warm loop (api.go) aynı öneki kullanır — yoksa ısıtılan
	// slot hiç okunmaz. env de anahtarda: farklı env = farklı liste.
	key := fmt.Sprintf("databases:v2:%s:env=%s:cmp=%v", cacheBucket(from, to), env, compare)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		ov, err := s.store.GetDatabases(ctx, chstore.DatabasesQuery{
			From: from, To: to, Env: env,
			IncludeCallers: true, IncludeReceivers: true,
		})
		if err != nil {
			return nil, err
		}
		if !compare || len(ov.Rows) == 0 {
			return ov, nil
		}
		// Prior penceresi HAFİF ikizden (v0.9.821): receiver keşfini ve
		// çağıran turunu atlar — delta rozetleri yalnız sayaç + quantile
		// okuyor. Öncesinde prior çağrısı TAM okumayı koşuyordu, yani
		// her compare'li sayfa yüklemesinde dört katalog probu, dört
		// metric_points taraması ve bir tam çağıran taraması BOŞUNA
		// ödeniyordu.
		dur := to.Sub(from)
		priorRows, err := s.store.GetDatabasesRollup(ctx, from.Add(-dur), from, env)
		if err != nil {
			return ov, nil
		}
		mergeDBPrior(ov.Rows, priorRows)
		return ov, nil
	})
}

// mergeDBPrior — prior pencere sayaçlarını (system, instance, dbName)
// kimliğiyle mevcut satırlara kopyalar; mergeMessagingPrior'un ikizi.
// Prior'da olmayan satır alanları boş bırakır (omitempty → rozet gizli).
func mergeDBPrior(cur, prior []chstore.DBInstance) {
	type key struct{ system, instance, dbName string }
	idx := make(map[key]*chstore.DBInstance, len(prior))
	for i := range prior {
		idx[key{prior[i].System, prior[i].Instance, prior[i].DBName}] = &prior[i]
	}
	for i := range cur {
		p, ok := idx[key{cur[i].System, cur[i].Instance, cur[i].DBName}]
		if !ok {
			continue
		}
		cur[i].PriorSpanCount = p.SpanCount
		cur[i].PriorErrorCount = p.ErrorCount
		cur[i].PriorAvgMs = p.AvgMs
		cur[i].PriorP50Ms = p.P50Ms
		cur[i].PriorP99Ms = p.P99Ms
	}
}

// getDBTrends serves the per-row RED sparklines (#1) + latest-bucket
// health snapshot (#6) for the /databases + /messaging overview
// grid. One DBTrend per (db_system, instance, db_name) — keyed
// identically to the /api/databases rows so the frontend joins
// trends → rows by (system, instance, dbName). Read-only; no auth
// gate / audit. Cache key hashes the (minute-bucketed) window via
// the shared cacheBucket helper; 30s TTL matches the overview so a
// page load and its sparkline fetch share the same warm window.
func (s *Server) getDBTrends(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, time.Hour)
	key := "db-trends:" + cacheBucket(from, to)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetDBTrends(ctx, from, to)
	})
}

// getMessagingTrends (v0.9.434) — getDBTrends'in messaging ikizi:
// /messaging grid'inin satır-içi sparkline + sağlık chip kaynağı.
// Aynı 30s TTL — sayfa yüklemesiyle sparkline fetch'i sıcak pencereyi
// paylaşır; anahtar tüm girdileri (pencere) hashler.
func (s *Server) getMessagingTrends(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, time.Hour)
	key := "msg-trends:" + cacheBucket(from, to)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetMessagingTrends(ctx, from, to)
	})
}

// getDatabaseDetail returns the drawer payload for one
// (db_system, instance, db_name) TRIPLE — per-(service, pod) caller
// breakdown plus the top db_statement prefixes. Cached 30s.
//
// v0.9.821 — ?dbName= imzaya girdi. Öncesinde uç yalnız (system,
// instance) soruyordu, oysa TABLO SATIRI üçlü kimlikteydi: bir host'ta
// N veritabanı olan her kurulumda — Oracle SID'leri, PostgreSQL
// şemaları, MSSQL DB'leri — hangi satıra tıklanırsa tıklansın AYNI
// çekmece açılıyor ve o host'un TOPLAMINI gösteriyordu. Satır 4.200
// sorgu diyor, çekmece 31.000 gösteriyordu; ikisi de doğru görünüyordu.
//
// dbName CACHE ANAHTARINA da girer — girmeseydi ilk açılan
// veritabanının çekmecesi 30 sn boyunca diğerlerine de servis edilirdi,
// yani düzeltilen yalan cache katmanında yaşamaya devam ederdi.
func (s *Server) getDatabaseDetail(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	system := q.Get("system")
	instance := q.Get("instance")
	dbName := q.Get("dbName")
	if system == "" {
		http.Error(w, `{"error":"system required"}`, http.StatusBadRequest)
		return
	}
	from, to := parseFromTo(r, time.Hour)
	key := dbDetailKey(system, instance, dbName, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetDatabaseDetail(ctx, system, instance, dbName, from, to)
	})
}

// dbDetailKey — çekmece cache anahtarı. Alan sınırları FNV digest ile
// çözülüyor (endpointKeyDigest'in üç alanlı ikizi): instance ve dbName
// operatör verisi ve `:` ile birleştirilirse "a:b" + "c" ile "a" + "b:c"
// aynı anahtara düşerdi — v0.5.187 belirsizlik sınıfının string hâli.
// SAF; regresyon testi db_detail_key_test.go.
func dbDetailKey(system, instance, dbName, bucket string) string {
	h := fnv.New64a()
	h.Write([]byte(system))
	h.Write([]byte{0})
	h.Write([]byte(instance))
	h.Write([]byte{0})
	h.Write([]byte(dbName))
	return fmt.Sprintf("db-detail:v2:%x:%s", h.Sum64(), bucket)
}

// getOracleMetrics returns the OracleDB-receiver drill-down for
// one instance — sessions/processes utilisation, cumulative
// counter rates, tablespace usage. Falls back to deterministic
// synthetic data (flagged Synthetic=true in the payload) when
// no oracledb.* metric_points exist in the window so the panel
// still renders during integration setup.
func (s *Server) getOracleMetrics(w http.ResponseWriter, r *http.Request) {
	instance := r.URL.Query().Get("instance")
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("oracle:%s:%s", instance, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetOracleMetrics(ctx, instance, from, to)
	})
}

// getPostgresMetrics serves the Postgres receiver drill-down
// for the row-click drawer on /databases. Mirrors getOracleMetrics:
// 30s cache TTL bucketed to a 30s grid so morning-triage hits
// share one query trip even with rolling time windows.
func (s *Server) getPostgresMetrics(w http.ResponseWriter, r *http.Request) {
	instance := r.URL.Query().Get("instance")
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("postgres:%s:%s", instance, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetPostgresMetrics(ctx, instance, from, to)
	})
}

// getMySQLMetrics — MySQL receiver drill-down (buffer pool /
// threads / row-lock / slow queries / handlers / replica lag).
func (s *Server) getMySQLMetrics(w http.ResponseWriter, r *http.Request) {
	instance := r.URL.Query().Get("instance")
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("mysql:%s:%s", instance, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetMySQLMetrics(ctx, instance, from, to)
	})
}

// getRedisMetrics — Redis receiver drill-down (clients / memory /
// commands / hit rate / per-keyspace / replication / role).
func (s *Server) getRedisMetrics(w http.ResponseWriter, r *http.Request) {
	instance := r.URL.Query().Get("instance")
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("redis:%s:%s", instance, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetRedisMetrics(ctx, instance, from, to)
	})
}

// getMessaging is the parallel handler for queues / topics
// (Kafka / RabbitMQ / IBM MQ / etc.). Same caching semantics.
//
// v0.8.364 (Stage-2 M1) — optional ?compare=prior (the endpoints
// v0.5.404 pattern): a second scan of the SAME MVs over the
// immediately-preceding equal-length window, merged onto the
// current rows by (system, cluster, destination) identity. Opt-in
// because it doubles the CH cost.
func (s *Server) getMessaging(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, time.Hour)
	compare := r.URL.Query().Get("compare") == "prior"
	// Cache key hashes all inputs (window + compare). The default
	// read keeps the pre-M1 key byte-identical so the background
	// warm loop in api.go (warm("messaging", …)) still primes the
	// slot the page load hits; compare rides its own slot.
	// v0.9.813 — anahtar öneki v2'ye çıktı ÇÜNKÜ zarf değişti (çıplak
	// dizi → MessagingOverview). Önek sürümlenmeseydi rolling deploy
	// sırasında eski dizi payload'ı 30 sn boyunca yeni SPA'ya servis
	// edilir ve sayfa boş açılırdı (v0.9.443/458 dersi). Warm loop
	// (api.go) aynı öneki kullanır — yoksa ısıtılan slot hiç okunmaz.
	key := "messaging:v2:" + cacheBucket(from, to)
	if compare {
		key = "messaging:v2:cmp:" + cacheBucket(from, to)
	}
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		ov, err := s.store.GetMessaging(ctx, from, to)
		if err != nil {
			return nil, err
		}
		if !compare || len(ov.Rows) == 0 {
			return ov, nil
		}
		// Prior window: same length, shifted back by exactly the
		// window width so the comparison stays apples-to-apples.
		// Rollup variant skips the top-callers pass — the delta
		// merge only reads counts + quantiles. Prior failure is
		// non-fatal: return current rows without trends rather
		// than 500'ing the page.
		dur := to.Sub(from)
		priorRows, err := s.store.GetMessagingRollup(ctx, from.Add(-dur), from)
		if err != nil {
			return ov, nil
		}
		mergeMessagingPrior(ov.Rows, priorRows)
		return ov, nil
	})
}

// mergeMessagingPrior copies the prior-window counters onto the
// current rows by (system, cluster, destination) identity — the
// same key GetMessaging groups by, so a destination that moved
// rank between windows still matches. Rows absent from the prior
// window keep zero Prior* fields (omitempty → absent in JSON →
// the frontend renders no delta badge). Pure — table-driven
// tested in messaging_prior_test.go (v0.8.364).
func mergeMessagingPrior(cur, prior []chstore.MessagingInstance) {
	type key struct{ system, cluster, dest string }
	idx := make(map[key]*chstore.MessagingInstance, len(prior))
	for i := range prior {
		idx[key{prior[i].System, prior[i].Cluster, prior[i].Destination}] = &prior[i]
	}
	for i := range cur {
		p, ok := idx[key{cur[i].System, cur[i].Cluster, cur[i].Destination}]
		if !ok {
			continue
		}
		cur[i].PriorSpanCount = p.SpanCount
		cur[i].PriorErrorCount = p.ErrorCount
		cur[i].PriorProduceCount = p.ProduceCount
		cur[i].PriorConsumeCount = p.ConsumeCount
		cur[i].PriorAvgMs = p.AvgMs
		cur[i].PriorP50Ms = p.P50Ms
		cur[i].PriorP99Ms = p.P99Ms
	}
}

// getMessagingDetail is the parallel handler for queues /
// topics. Takes ?system=&cluster=&destination=&from=&to=.
//
// v0.9.973 — the empty-cluster default is no longer SILENT. It used to
// read "defaults to (default) for single-cluster deployments where the
// SPA hasn't been updated yet", which understated it: GetMessagingDetail
// filters cluster by EXACT EQUALITY, so on a multi-cluster install the
// assumption doesn't pick the wrong topic — it returns a ZEROED drawer
// for a topic that is very much alive. The operator then reads "no
// traffic" where the truth is "you didn't say which cluster".
//
// Kept as a default rather than a 400 on purpose: the SPA always sends
// all three fields (destinationParam.ts guards it), so a 400 would only
// ever break hand-built API calls, and there may be non-SPA consumers.
// The lie is what gets fixed, not the tolerance — the envelope now
// carries assumedCluster so the surface can say so.
func (s *Server) getMessagingDetail(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	system := q.Get("system")
	dest := q.Get("destination")
	cluster := q.Get("cluster")
	assumedCluster := false
	if cluster == "" {
		cluster = "(default)"
		assumedCluster = true
	}
	if system == "" {
		http.Error(w, `{"error":"system required"}`, http.StatusBadRequest)
		return
	}
	from, to := parseFromTo(r, time.Hour)
	// assumedCluster is IN THE KEY: an explicit ?cluster=(default) resolves
	// to the same cluster string but is NOT an assumption, so the two answers
	// differ by one field. Sharing a key would let whichever ran first decide
	// whether the other one admits the guess — the v0.5.187 cross-poisoning
	// class, one boolean wide.
	key := fmt.Sprintf("msg-detail:%s:%s:%s:%t:%s", system, cluster, dest, assumedCluster, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		d, err := s.store.GetMessagingDetail(ctx, system, cluster, dest, from, to)
		if err != nil || d == nil {
			return d, err
		}
		d.AssumedCluster = assumedCluster
		return d, nil
	})
}
