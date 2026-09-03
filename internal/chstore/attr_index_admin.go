package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/migrations"
)

// attr_index_admin.go — 0014 ATTRIBUTE HASH İNDEKSİ SİHİRBAZI (v0.10.306;
// operatör: "14 için sihirbaz göremedim"). function_id_admin.go (0013)
// aynası: 2 kolon (attr_kvh / res_kvh, spans_local + Distributed
// sarmalayıcı) + 4 bloom indeks.
//
// Uygulama yönetimli kurulumda (prod dahil, ekran görüntüsü "BOOT
// YÖNETİYOR") boot kolonları kendisi ekler — sihirbaz yalnız DURUM gösterir
// ve "Uygula" reddedilir (0013 ile aynı sözleşme). Dış Distributed'da
// Uygula = 0014 ADIM 1-2-4 (kolonlar + sarmalayıcı + indeksler; IF NOT
// EXISTS), MATERIALIZE = ADIM 6 (eski part'lar; disk + merge, mesai dışı),
// geri alma = INDEX → COLUMN sırası (Code 47).
//
// Doluluk (son 10 dk): attr_kvh uzunluğu attr_keys uzunluğuna eşit olan
// satır payı — MATERIALIZED ifade her satırda hesaplandığından beklenen
// %100; eksik/bozuk part yazımı burada görünür. Pod'lar kolonu probe ile
// (attr_index.go) restart'ta ya da ertelenmiş DDL inince alır.

const (
	attrIndexFile         = "0014_attr_kvh.sql"
	attrIndexRollbackFile = "0014_attr_kvh_rollback.sql"
)

// AttrIndexObjects — host başına varlığı izlenen nesneler.
func AttrIndexObjects() []EntityLayerObject {
	out := []EntityLayerObject{}
	for _, c := range attrIndexCols {
		out = append(out,
			EntityLayerObject{Name: c.col, Kind: "column", Table: "spans_local"},
			EntityLayerObject{Name: c.col, Kind: "distributed", Table: "spans"},
			EntityLayerObject{Name: c.idx, Kind: "index", Table: "spans_local"},
			EntityLayerObject{Name: c.keysIdx, Kind: "index", Table: "spans_local"},
		)
	}
	return out
}

type AttrIndexStatusResult struct {
	Cluster     string                    `json:"cluster"`
	Objects     []EntityLayerObjectStatus `json:"objects"`
	BootManaged bool                      `json:"bootManaged"`
	// Ready — bu pod'un derleyicisi bloom yolunda mı (AttrIndexAvailable).
	Ready bool `json:"ready"`
	// Used — bu pod'un ürettiği bloom yüklemi sayısı (boot'tan beri).
	Used uint64 `json:"used"`
	// Filled/Total — son 10 dk: attr_kvh uzunluğu attr_keys ile eşit satır / toplam.
	Filled    uint64 `json:"filled"`
	Total     uint64 `json:"total"`
	Generated int64  `json:"generated"`
}

type AttrIndexPreflightResult struct {
	Clusters         []string `json:"clusters"`
	SuggestedCluster string   `json:"suggestedCluster,omitempty"`
	SpansLocal       bool     `json:"spansLocal"`
	BootManaged      bool     `json:"bootManaged"`
	ColumnsExist     bool     `json:"columnsExist"`
	IndexesExist     bool     `json:"indexesExist"`
	Filled           uint64   `json:"filled"`
	Total            uint64   `json:"total"`
	ProbeErrors      []string `json:"probeErrors,omitempty"`
	Supported        bool     `json:"supported"`
	Detail           string   `json:"detail"`
	Generated        int64    `json:"generated"`
}

func attrIndexStatements(cluster string) ([]string, error) {
	raw, err := migrations.FS.ReadFile(attrIndexFile)
	if err != nil {
		return nil, fmt.Errorf("gömülü %s okunamadı: %w", attrIndexFile, err)
	}
	return SplitSQLStatements(AdaptRollupDDL(string(raw), cluster)), nil
}

func attrIndexRollbackStatements(cluster string) ([]string, error) {
	raw, err := migrations.FS.ReadFile(attrIndexRollbackFile)
	if err != nil {
		return nil, fmt.Errorf("gömülü %s okunamadı: %w", attrIndexRollbackFile, err)
	}
	return SplitSQLStatements(AdaptRollupDDL(string(raw), cluster)), nil
}

// attrIndexMaterializeStatements — ADIM 6: eski part'lar (kolon + indeks).
func attrIndexMaterializeStatements(cluster string) []string {
	var out []string
	for _, c := range attrIndexCols {
		out = append(out, "ALTER TABLE spans_local ON CLUSTER "+cluster+" MATERIALIZE COLUMN "+c.col)
	}
	for _, c := range attrIndexCols {
		out = append(out, "ALTER TABLE spans_local ON CLUSTER "+cluster+" MATERIALIZE INDEX "+c.idx)
	}
	return out
}

func (s *Store) attrIndexFillProbe(ctx context.Context) (filled, total uint64, err error) {
	err = s.conn.QueryRow(ctx, `
		SELECT countIf(length(attr_kvh) = length(attr_keys)), count() FROM spans
		WHERE time >= now() - INTERVAL 10 MINUTE AND time <= now()
		SETTINGS max_execution_time = 10, max_rows_to_read = 50000000, read_overflow_mode = 'break'`).Scan(&filled, &total)
	return
}

// attrIndexDecision — SAF karar (0013 tablosunun aynısı, anahtar-yazımı
// koşulu yok: ifade her anahtarı kapsar).
func attrIndexDecision(bootManaged, spansLocal bool, clusters int) (supported bool, detail string) {
	switch {
	case bootManaged:
		return false, "spans uygulama yönetimli — boot kolon ve indeksleri kendisi ekler (attr_index.go); sihirbaz gerekmez"
	case !spansLocal:
		return false, "spans_local yok — dış Distributed kurulum bekleniyordu"
	case clusters == 0:
		return false, "system.clusters boş — ON CLUSTER DDL hedefi yok"
	}
	return true, "uygulanabilir: ADIM 1-2-4 (attr_kvh + res_kvh kolonları, sarmalayıcı, 4 bloom indeks; IF NOT EXISTS); MATERIALIZE ayrı eylem; sonra pod restart"
}

func (s *Store) AttrIndexStatus(ctx context.Context) (AttrIndexStatusResult, error) {
	out := AttrIndexStatusResult{Generated: time.Now().Unix(), Objects: []EntityLayerObjectStatus{}}
	cluster := s.entityLayerCluster(ctx)
	out.Cluster = cluster
	out.BootManaged = !s.spansIsExternalDistributed(ctx)
	out.Used, out.Ready = AttrIndexStats()
	hosts := 1
	colSrc, idxSrc := "system.columns", "system.data_skipping_indices"
	if cluster != "" {
		var n uint64
		if err := s.conn.QueryRow(ctx, `SELECT count() FROM system.clusters WHERE cluster = ?`, cluster).Scan(&n); err == nil && n > 0 {
			hosts = int(n)
		}
		colSrc = fmt.Sprintf("clusterAllReplicas('%s', system.columns)", cluster)
		idxSrc = fmt.Sprintf("clusterAllReplicas('%s', system.data_skipping_indices)", cluster)
	}
	count := func(sql string, args ...any) (int, error) {
		var n uint64
		if err := s.conn.QueryRow(ctx, sql+" SETTINGS max_execution_time = 10", args...).Scan(&n); err != nil {
			return 0, err
		}
		return int(n), nil
	}
	for _, o := range AttrIndexObjects() {
		st := EntityLayerObjectStatus{EntityLayerObject: o, Hosts: hosts}
		table := o.Table
		if cluster == "" {
			if o.Kind == "distributed" {
				continue
			}
			table = "spans"
		}
		var have int
		var err error
		switch o.Kind {
		case "column", "distributed":
			have, err = count(`SELECT count() FROM `+colSrc+` WHERE database = currentDatabase() AND table = ? AND name = ?`, table, o.Name)
		case "index":
			have, err = count(`SELECT count() FROM `+idxSrc+` WHERE database = currentDatabase() AND table = ? AND name = ?`, table, o.Name)
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
	if s.probeAttrIndex(ctx) {
		if f, t, err := s.attrIndexFillProbe(ctx); err == nil {
			out.Filled, out.Total = f, t
		}
	}
	return out, nil
}

func (s *Store) AttrIndexPreflight(ctx context.Context) (AttrIndexPreflightResult, error) {
	out := AttrIndexPreflightResult{
		SuggestedCluster: strings.TrimSpace(s.cfg.ClusterName),
		Generated:        time.Now().Unix(),
		Clusters:         []string{},
	}
	if out.SuggestedCluster == "" {
		out.SuggestedCluster = s.discoverSpansCluster(ctx)
	}
	out.BootManaged = !s.spansIsExternalDistributed(ctx)
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
	out.ColumnsExist = s.probeAttrIndex(ctx)
	out.IndexesExist = true
	for _, c := range attrIndexCols {
		if !s.spansIndexExists(ctx, c.idx) || !s.spansIndexExists(ctx, c.keysIdx) {
			out.IndexesExist = false
		}
	}
	if out.ColumnsExist {
		if f, t, err := s.attrIndexFillProbe(ctx); err != nil {
			out.ProbeErrors = append(out.ProbeErrors, "doluluk: "+err.Error())
		} else {
			out.Filled, out.Total = f, t
		}
	}
	out.Supported, out.Detail = attrIndexDecision(out.BootManaged, out.SpansLocal, len(out.Clusters))
	return out, nil
}

func (s *Store) AttrIndexApply(ctx context.Context, cluster string) []RollupStmtResult {
	c := strings.TrimSpace(cluster)
	if c == "" || !validRolloutLayerCluster(c) {
		return []RollupStmtResult{{Head: "ön koşul", Err: "cluster adı zorunlu/geçersiz — yalnız harf/rakam/_ . - (≤64)"}}
	}
	stmts, err := attrIndexStatements(c)
	if err != nil {
		return []RollupStmtResult{{Head: "ön koşul", Err: err.Error()}}
	}
	return s.execStmtsStopOnError(ctx, stmts)
}

func (s *Store) AttrIndexMaterialize(ctx context.Context, cluster string) []RollupStmtResult {
	c := strings.TrimSpace(cluster)
	if c == "" || !validRolloutLayerCluster(c) {
		return []RollupStmtResult{{Head: "ön koşul", Err: "cluster adı zorunlu/geçersiz"}}
	}
	return s.execStmtsStopOnError(ctx, attrIndexMaterializeStatements(c))
}

func (s *Store) AttrIndexRollback(ctx context.Context, cluster string) []RollupStmtResult {
	c := strings.TrimSpace(cluster)
	if c == "" || !validRolloutLayerCluster(c) {
		return []RollupStmtResult{{Head: "ön koşul", Err: "cluster adı zorunlu/geçersiz"}}
	}
	stmts, err := attrIndexRollbackStatements(c)
	if err != nil {
		return []RollupStmtResult{{Head: "ön koşul", Err: err.Error()}}
	}
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
