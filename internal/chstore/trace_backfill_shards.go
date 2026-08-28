package chstore

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// trace_backfill_shards.go — SHARD-YEREL INSERT (v0.10.125, operatör:
// "Önerinin yapalım").
//
// ── ÖLÇÜM ────────────────────────────────────────────────────────────────
//
// Lokal 2-shard, 6 saatlik pencere (653k span), query_log: aynı GROUP BY
// `spans_local` üzerinde 219 ms, Distributed `spans` üzerinden 768 ms;
// initiator'a shard başına ~4.7 MiB kısmi state (15 B/span) gidip
// Distributed INSERT'le geri dağıtılıyor. Prod'da 15 dk'lık dilim ≈ 85 M
// span → dilim başına ≈ 1.2 GB initiator'a + ≈ 1.2 GB geri. Bu yol iki
// transferi de kaldırır: her shard KENDİ span'lerinden KENDİ MV'sine yazar.
//
// ── NEDEN DOĞRU ──────────────────────────────────────────────────────────
//
// spans `rand()` ile shard'lanır; canlı MV zaten her shard'da aynı trace
// için AYRI kısmi state satırı yazar ve okuyucu `-Merge` ile birleştirir.
// Shard-yerel backfill bire bir aynı şekli üretir (initiator yolu ise
// global birleştirip yeniden dağıtıyordu — daha "temiz" ama gereksiz).
// Doğrulama ölçütü: gün için uniqExact(trace_id) ve sum(countMerge) iki
// yolda eşit; satır SAYISI shard-yerelde daha çok olabilir (kısmi
// state'ler), o fark beklenen.
//
// ── DÜŞÜŞ ────────────────────────────────────────────────────────────────
//
// Küme adı (cfg ya da spans engine_full'dan) çözülemezse, system.clusters
// boşsa ya da bir shard host'una bağlanılamazsa → initiator yolu, not
// düşülür. Tek düğümde (Distributed yok) her zaman initiator yolu.

// backfillShard — bir shard'ın seçilen replikası.
type backfillShard struct {
	Num  int
	Addr string // host:port (native)
}

// clusterHostRow — system.clusters satırı (saf seçim için).
type clusterHostRow struct {
	Shard, Replica int
	Host           string
	Port           int
	Local          bool
}

// pickShardHosts — shard başına TEK host: is_local varsa o, yoksa en
// küçük replica_num. Saf; tablo-testli. Sıra shard_num'a göre.
func pickShardHosts(rows []clusterHostRow) []backfillShard {
	best := map[int]clusterHostRow{}
	for _, r := range rows {
		if r.Host == "" || r.Port <= 0 {
			continue
		}
		cur, ok := best[r.Shard]
		if !ok || (r.Local && !cur.Local) || (r.Local == cur.Local && r.Replica < cur.Replica) {
			best[r.Shard] = r
		}
	}
	out := make([]backfillShard, 0, len(best))
	for num, r := range best {
		out = append(out, backfillShard{Num: num, Addr: fmt.Sprintf("%s:%d", r.Host, r.Port)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Num < out[j].Num })
	return out
}

// backfillShards — kümenin shard'ları; küme yoksa (nil, "", nil).
func (s *Store) backfillShards(ctx context.Context) ([]backfillShard, string, error) {
	cluster := strings.TrimSpace(s.cfg.ClusterName)
	if cluster == "" {
		cluster = s.discoverSpansCluster(ctx)
	}
	if cluster == "" {
		return nil, "", nil
	}
	rows, err := s.conn.Query(ctx, `
		SELECT shard_num, replica_num, host_name, port, is_local
		FROM system.clusters WHERE cluster = ?
		ORDER BY shard_num, replica_num
		SETTINGS max_execution_time = 5`, cluster)
	if err != nil {
		return nil, cluster, err
	}
	defer rows.Close()
	var hosts []clusterHostRow
	for rows.Next() {
		var r clusterHostRow
		var shard, replica uint32
		var port uint16
		var local uint8
		if err := rows.Scan(&shard, &replica, &r.Host, &port, &local); err != nil {
			return nil, cluster, err
		}
		r.Shard, r.Replica, r.Port, r.Local = int(shard), int(replica), int(port), local == 1
		hosts = append(hosts, r)
	}
	return pickShardHosts(hosts), cluster, rows.Err()
}

// shardConn — host'a doğrudan bağlantı (önbellekli). Aynı kimlik/TLS/
// bellek ayarları (chOpts), yalnız Addr farklı.
func (s *Store) shardConn(ctx context.Context, addr string) (driver.Conn, error) {
	s.shardMu.Lock()
	defer s.shardMu.Unlock()
	if c, ok := s.shardConns[addr]; ok {
		return c, nil
	}
	if s.chOpts == nil {
		return nil, fmt.Errorf("bağlantı seçenekleri yok")
	}
	opts := s.chOpts()
	opts.Addr = []string{addr}
	c, err := clickhouse.Open(opts)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Ping(pctx); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("%s: %w", addr, err)
	}
	if s.shardConns == nil {
		s.shardConns = map[string]driver.Conn{}
	}
	s.shardConns[addr] = c
	return c, nil
}

// backfillLocalInsertSQL — shard-yerel INSERT; state listesi MV DDL'inin
// aynası (traceBackfillStateSQL), hedef ve kaynak `_local`. Saf.
func backfillLocalInsertSQL() string {
	return `
			INSERT INTO trace_summary_5m_local
			  (trace_id, time_bucket, root_service_state, root_name_state,
			   trace_start_state, trace_end_state, span_count_state,
			   error_count_state, entry_route_state, entry_service_state)
			SELECT trace_id, toStartOfInterval(time, INTERVAL 5 MINUTE),` +
		traceBackfillStateSQL + `
			FROM spans_local
			WHERE time >= toDateTime(?, 'UTC') AND time < toDateTime(?, 'UTC')
			GROUP BY trace_id, toStartOfInterval(time, INTERVAL 5 MINUTE)
			SETTINGS max_execution_time = 25,
			         max_bytes_before_external_group_by = 2000000000`
}

// backfillShardExec — dilim exec'i: her shard'da paralel yerel INSERT;
// ilk hata döner (kaynak hatası metni korunur → merdiven). Saf orkestrasyon:
// run enjekte edilir, tablo-testli.
func backfillShardExec(shards []backfillShard, run func(ctx context.Context, sh backfillShard, from, to time.Time) error) func(context.Context, time.Time, time.Time) error {
	return func(ctx context.Context, from, to time.Time) error {
		errs := make(chan error, len(shards))
		for _, sh := range shards {
			go func(sh backfillShard) {
				if err := run(ctx, sh, from, to); err != nil {
					errs <- fmt.Errorf("shard %d (%s): %w", sh.Num, sh.Addr, err)
					return
				}
				errs <- nil
			}(sh)
		}
		var first error
		for range shards {
			if err := <-errs; err != nil && first == nil {
				first = err
			}
		}
		return first
	}
}

// backfillShardMode — gün için exec seçimi + not. Shard-yerel yalnız
// küme çözülür ve HER shard'a bağlanılırsa; aksi hâlde initiator yolu.
func (s *Store) backfillShardMode(ctx context.Context) (exec func(context.Context, time.Time, time.Time) error, note string) {
	shards, cluster, err := s.backfillShards(ctx)
	if err != nil || len(shards) == 0 {
		why := "küme yok"
		if err != nil {
			why = "shard listesi okunamadı: " + shortErr(err)
		} else if cluster != "" {
			why = "system.clusters boş (" + cluster + ")"
		}
		return nil, "initiator yolu (" + why + ")"
	}
	conns := map[int]driver.Conn{}
	var hosts []string
	for _, sh := range shards {
		c, err := s.shardConn(ctx, sh.Addr)
		if err != nil {
			log.Printf("[trace-backfill] shard-yerel yol devre dışı: %v", err)
			return nil, "initiator yolu (shard'a bağlanılamadı: " + shortErr(err) + ")"
		}
		conns[sh.Num] = c
		hosts = append(hosts, sh.Addr)
	}
	sql := backfillLocalInsertSQL()
	exec = backfillShardExec(shards, func(ctx context.Context, sh backfillShard, from, to time.Time) error {
		return conns[sh.Num].Exec(ctx, sql,
			from.UTC().Format("2006-01-02 15:04:05"), to.UTC().Format("2006-01-02 15:04:05"))
	})
	return exec, fmt.Sprintf("shard-yerel yol: %d shard (%s), küme %s", len(shards), strings.Join(hosts, ", "), cluster)
}
