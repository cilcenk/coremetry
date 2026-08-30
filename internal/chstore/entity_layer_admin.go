package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/migrations"
)

// entity_layer_admin.go — 0011 ENTITY KATMANI ŞEMASI SİHİRBAZI (v0.10.134).
//
// Operator-reported (prod, 2026-08-28): "cluster eşleşme için sihirbaz —
// 0011 MV'yi görmedim". 0011 yalnız elle uygulanan bir dosyaydı; rollup
// (v0.9.770) ve 0009 (v0.9.1312) sihirbazlarının emsaliyle Admin →
// ClickHouse'a adım olarak girer:
//
//   Durum    host başına kolon / index / tablo / MV / Distributed var-yok
//            (clusterAllReplicas — dağıtık DDL'in yarım kaldığı host görünür)
//   Ön kontrol  system.clusters + önerilen ad, spans_local var mı, k8s
//            kapsama (son 15 dk k8s.pod.name taşıyan span oranı) ve
//            LowCardinality kapısı (son 1 saat uniq pod adı ≤ 100k)
//   Uygula   gömülü 0011, `uptrace_all` token'ı gerçek küme adıyla, ifade
//            ifade, İLK HATADA DUR (kaskad yarım kalmasın; IF NOT EXISTS
//            → yeniden basmak güvenli)
//   Geri al  YALNIZ iki MV + sarmalayıcıları — yazımı keser, kolon/tablo/
//            veri KALIR (rollup geri alma sözleşmesi)
//
// Boot'ta ASLA koşmaz; tek tetikleyici admin düğmesi (ev kuralı v0.9.613).
// Prod'da uygulama boot'ta kolon/tablo/MV'yi kendi de kurmayı DENER
// (cluster_name set); durum görünümü hangisinin gerçekten indiğini
// gösterir, "Uygula" eksik kalanı tamamlar.

// EntityLayerObject — durum probe'unun taradığı bir nesne.
type EntityLayerObject struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // column | index | table | mv | distributed
	// Table — kolon/index için sahibi (spans_local).
	Table string `json:"table,omitempty"`
}

// EntityLayerObjects — 0011'in yarattığı her nesne (test pinler).
func EntityLayerObjects() []EntityLayerObject {
	return []EntityLayerObject{
		{Name: "k8s_namespace", Kind: "column", Table: "spans_local"},
		{Name: "k8s_pod", Kind: "column", Table: "spans_local"},
		{Name: "k8s_node", Kind: "column", Table: "spans_local"},
		{Name: "idx_k8s_pod", Kind: "index", Table: "spans_local"},
		{Name: "idx_k8s_node", Kind: "index", Table: "spans_local"},
		{Name: "entities", Kind: "table"},
		{Name: "entity_relations", Kind: "table"},
		{Name: "entity_sync_runs", Kind: "table"},
		{Name: "entity_seen_1m_local", Kind: "mv"},
		{Name: "entity_seen_5m_local", Kind: "mv"},
		{Name: "entity_seen_1m", Kind: "distributed"},
		{Name: "entity_seen_5m", Kind: "distributed"},
	}
}

// EntityLayerObjectStatus — bir nesnenin host başına varlığı.
type EntityLayerObjectStatus struct {
	EntityLayerObject
	Hosts     int    `json:"hosts"`     // kümedeki host sayısı
	HaveHosts int    `json:"haveHosts"` // nesnenin var olduğu host sayısı
	State     string `json:"state"`     // ok | partial | missing | unknown
	Err       string `json:"err,omitempty"`
}

// entityLayerObjectState — saf sınıflandırma (testli).
func entityLayerObjectState(have, total int) string {
	switch {
	case total <= 0:
		return "unknown"
	case have >= total:
		return "ok"
	case have == 0:
		return "missing"
	default:
		return "partial"
	}
}

// EntityLayerStatusResult — sihirbaz kartı.
type EntityLayerStatusResult struct {
	Cluster   string                    `json:"cluster"`
	Objects   []EntityLayerObjectStatus `json:"objects"`
	SeenRows  uint64                    `json:"seenRows"` // entity_seen_5m son 15 dk (MV yazıyor mu kanıtı)
	Generated int64                     `json:"generated"`
}

// EntityLayerPreflightResult — "bu küme 0011'i kaldırır mı".
type EntityLayerPreflightResult struct {
	Clusters         []string `json:"clusters"`
	SuggestedCluster string   `json:"suggestedCluster,omitempty"`
	SpansLocal       bool     `json:"spansLocal"`
	// PodAttrCoverage — son 15 dk span'lerinde k8s.pod.name taşıyan oran
	// (0..1). 0 ise kolon boş dolar, MV hiç yazmaz — kurulum işe yaramaz.
	PodAttrCoverage float64 `json:"podAttrCoverage"`
	// UniqPods1h — son 1 saat uniq pod adı (LowCardinality kapısı: > 100k
	// ise kolon düz String olmalı — dosya başlığındaki uyarı).
	UniqPods1h  uint64   `json:"uniqPods1h"`
	ProbeErrors []string `json:"probeErrors,omitempty"`
	Supported   bool     `json:"supported"`
	Detail      string   `json:"detail"`
	Generated   int64    `json:"generated"`
}

const entityLayerFile = "0011_entity_layer.sql"
const entityLayerLCGate = 100_000

// entityLayerStatements — gömülü 0011, küme adıyla, ifadelere bölünmüş. Saf.
func entityLayerStatements(cluster string) ([]string, error) {
	raw, err := migrations.FS.ReadFile(entityLayerFile)
	if err != nil {
		return nil, fmt.Errorf("gömülü %s okunamadı: %w", entityLayerFile, err)
	}
	return SplitSQLStatements(AdaptRollupDDL(string(raw), cluster)), nil
}

// entityLayerRollbackStatements — yalnız MV + sarmalayıcı (saf, testli).
//
// SYNC ZORUNLU (lokalde ölçüldü): Atomic veritabanında DROP TABLE tembeldir
// (database_atomic_delay_before_drop_table_sec = 8 dk) — tablo ayrılır,
// Replicated znode'u (`/clickhouse/tables/{shard}/entity_seen_1m/replicas/…`)
// yerinde kalır; ardından gelen "Uygula" REPLICA_ALREADY_EXISTS (253) ile
// düşer. SYNC znode'u hemen temizler; geri al → uygula döngüsü çalışır.
func entityLayerRollbackStatements(cluster string) []string {
	return []string{
		"DROP TABLE IF EXISTS entity_seen_1m ON CLUSTER " + cluster + " SYNC",
		"DROP TABLE IF EXISTS entity_seen_1m_local ON CLUSTER " + cluster + " SYNC",
		"DROP TABLE IF EXISTS entity_seen_5m ON CLUSTER " + cluster + " SYNC",
		"DROP TABLE IF EXISTS entity_seen_5m_local ON CLUSTER " + cluster + " SYNC",
	}
}

// entityLayerCluster — durum probe'unun kümesi: cfg ya da spans'ın Distributed
// engine'inden; boşsa tek düğüm (clusterAllReplicas kullanılmaz).
func (s *Store) entityLayerCluster(ctx context.Context) string {
	if c := strings.TrimSpace(s.cfg.ClusterName); c != "" {
		return c
	}
	return s.discoverSpansCluster(ctx)
}

// EntityLayerStatus — host başına nesne varlığı.
func (s *Store) EntityLayerStatus(ctx context.Context) (EntityLayerStatusResult, error) {
	out := EntityLayerStatusResult{Generated: time.Now().Unix()}
	cluster := s.entityLayerCluster(ctx)
	out.Cluster = cluster
	hosts := 1
	colSrc, idxSrc, tblSrc := "system.columns", "system.data_skipping_indices", "system.tables"
	if cluster != "" {
		var n uint64
		if err := s.conn.QueryRow(ctx, `SELECT count() FROM system.clusters WHERE cluster = ?`, cluster).Scan(&n); err == nil && n > 0 {
			hosts = int(n)
		}
		q := func(t string) string { return fmt.Sprintf("clusterAllReplicas('%s', %s)", cluster, t) }
		colSrc, idxSrc, tblSrc = q("system.columns"), q("system.data_skipping_indices"), q("system.tables")
	}
	// Tek düğümde spans_local yok: kolon/index spans üzerinde.
	spansTable := "spans_local"
	if cluster == "" {
		spansTable = "spans"
	}
	count := func(sql string, args ...any) (int, error) {
		var n uint64
		if err := s.conn.QueryRow(ctx, sql+" SETTINGS max_execution_time = 10", args...).Scan(&n); err != nil {
			return 0, err
		}
		return int(n), nil
	}
	for _, o := range EntityLayerObjects() {
		st := EntityLayerObjectStatus{EntityLayerObject: o, Hosts: hosts}
		var have int
		var err error
		switch o.Kind {
		case "column":
			have, err = count(`SELECT count() FROM `+colSrc+` WHERE database = currentDatabase() AND table = ? AND name = ?`, spansTable, o.Name)
		case "index":
			have, err = count(`SELECT count() FROM `+idxSrc+` WHERE database = currentDatabase() AND table = ? AND name = ?`, spansTable, o.Name)
		case "table", "mv", "distributed":
			name := o.Name
			if cluster == "" && strings.HasSuffix(name, "_local") {
				// v0.10.208 — tek düğümde `_local` ile sarmalayıcı AYNI tabloya
				// eşlenir; aynı şeyi iki satır "VAR" göstermek yanıltıyordu →
				// `_local` satırı tek düğümde listelenmez (apply/rollback nesne
				// listesi ve küme kipi değişmedi).
				continue
			}
			have, err = count(`SELECT count() FROM `+tblSrc+` WHERE database = currentDatabase() AND name = ?`, name)
		}
		if err != nil {
			st.Err = err.Error()
			st.State = "unknown"
		} else {
			st.HaveHosts = have
			st.State = entityLayerObjectState(have, hosts)
		}
		out.Objects = append(out.Objects, st)
	}
	// MV gerçekten yazıyor mu: son 15 dk satır.
	var rows uint64
	if err := s.conn.QueryRow(ctx, `SELECT count() FROM entity_seen_5m WHERE time_bucket >= now() - INTERVAL 15 MINUTE SETTINGS max_execution_time = 10`).Scan(&rows); err == nil {
		out.SeenRows = rows
	}
	return out, nil
}

// EntityLayerPreflight — hiçbir şey yazmaz.
func (s *Store) EntityLayerPreflight(ctx context.Context) (EntityLayerPreflightResult, error) {
	out := EntityLayerPreflightResult{
		SuggestedCluster: strings.TrimSpace(s.cfg.ClusterName),
		Generated:        time.Now().Unix(),
	}
	if out.SuggestedCluster == "" {
		out.SuggestedCluster = s.discoverSpansCluster(ctx)
	}
	rows, err := s.conn.Query(ctx, `SELECT DISTINCT cluster FROM system.clusters ORDER BY cluster LIMIT 100`)
	if err != nil {
		out.ProbeErrors = append(out.ProbeErrors, "system.clusters: "+err.Error())
	} else {
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err == nil && c != "" {
				out.Clusters = append(out.Clusters, c)
			}
		}
		rows.Close()
	}
	if ok, err := s.tableExists(ctx, "spans_local"); err != nil {
		out.ProbeErrors = append(out.ProbeErrors, "spans_local: "+err.Error())
	} else {
		out.SpansLocal = ok
	}
	// Kapsama: son 15 dk, örneklemeli (LIMIT 200k) — prod'da 15 dk milyarlarca
	// satır olabilir; oran için örneklem yeter.
	var withPod, total uint64
	if err := s.conn.QueryRow(ctx, `
		SELECT countIf(has(res_keys, 'k8s.pod.name')), count()
		FROM (SELECT res_keys FROM spans WHERE time >= now() - INTERVAL 15 MINUTE LIMIT 200000)
		SETTINGS max_execution_time = 15`).Scan(&withPod, &total); err != nil {
		out.ProbeErrors = append(out.ProbeErrors, "kapsama: "+err.Error())
	} else if total > 0 {
		out.PodAttrCoverage = float64(withPod) / float64(total)
	}
	if err := s.conn.QueryRow(ctx, `
		SELECT uniq(res_values[indexOf(res_keys, 'k8s.pod.name')])
		FROM spans WHERE time >= now() - INTERVAL 1 HOUR AND has(res_keys, 'k8s.pod.name')
		SETTINGS max_execution_time = 20`).Scan(&out.UniqPods1h); err != nil {
		out.ProbeErrors = append(out.ProbeErrors, "uniq pod: "+err.Error())
	}
	switch {
	case len(out.ProbeErrors) > 0:
		out.Detail = "probe hatası — emin olamadığımız kümeye DDL basmıyoruz"
	case !out.SpansLocal:
		out.Detail = "spans_local yok — bu kurulum tek düğüm; 0011 dağıtık şema içindir (uygulama boot'ta kendi kurar)"
	case out.UniqPods1h > entityLayerLCGate:
		out.Detail = fmt.Sprintf("son 1 saatte %d farklı pod adı > %d — k8s_pod LowCardinality kapısı; dosyayı düz String'e çevirip elle uygula", out.UniqPods1h, entityLayerLCGate)
	default:
		out.Supported = true
		if out.PodAttrCoverage == 0 {
			out.Detail = "uygulanabilir — ama son 15 dk'da k8s.pod.name taşıyan span YOK: kolonlar boş dolar, entity_seen yazmaz (collector/downward API tarafı)"
		} else {
			out.Detail = fmt.Sprintf("uygulanabilir — span'lerin %%%.0f'i k8s.pod.name taşıyor, son 1 saat %d pod", out.PodAttrCoverage*100, out.UniqPods1h)
		}
	}
	return out, nil
}

// EntityLayerApply — gömülü 0011, ifade ifade; ilk hatada durur.
func (s *Store) EntityLayerApply(ctx context.Context, cluster string) []RollupStmtResult {
	c := strings.TrimSpace(cluster)
	if c == "" {
		return []RollupStmtResult{{Head: "ön koşul", Err: "cluster adı zorunlu — DDL `ON CLUSTER` yazıyor"}}
	}
	stmts, err := entityLayerStatements(c)
	if err != nil {
		return []RollupStmtResult{{Head: "ön koşul", Err: err.Error()}}
	}
	out := make([]RollupStmtResult, 0, len(stmts))
	for _, stmt := range stmts {
		r := RollupStmtResult{Head: stmtHead(stmt)}
		if err := s.conn.Exec(ctx, stmt); err != nil {
			r.Err = err.Error()
			out = append(out, r)
			return out
		}
		r.OK = true
		out = append(out, r)
	}
	return out
}

// EntityLayerRollback — yalnız MV'ler; ilk hatada DURMAZ (yazımı kes).
func (s *Store) EntityLayerRollback(ctx context.Context, cluster string) []RollupStmtResult {
	c := strings.TrimSpace(cluster)
	if c == "" {
		return []RollupStmtResult{{Head: "ön koşul", Err: "cluster adı zorunlu"}}
	}
	stmts := entityLayerRollbackStatements(c)
	out := make([]RollupStmtResult, 0, len(stmts))
	for _, stmt := range stmts {
		r := RollupStmtResult{Head: stmtHead(stmt)}
		if err := s.conn.Exec(ctx, stmt); err != nil {
			r.Err = err.Error()
		} else {
			r.OK = true
		}
		out = append(out, r)
	}
	return out
}
