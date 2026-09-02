package chstore

// function_id_admin.go — 0013 attr_function_id TERFİ KOLONU SİHİRBAZI
// (v0.10.252; operatör: "0013 migration nedir sihirbazı var mı" →
// "Kuyruğa al"). 0012 (rollout_layer_admin.go) aynası, tek kolon + index.
//
// Neden sihirbaz: prod dış Distributed'da boot ON CLUSTER DDL koşmaz
// (repairPromotedAttrCols atlar); dosya gömülüydü ama yalnız test okuyordu,
// operatör clickhouse-client ile ifade ifade koşmak zorundaydı.
//
// Ön kontrol (audit v0.9.626 dersi: "kolon var" ≠ "kolon dolu" ≠ "anahtar
// bu yazımla geliyor"): son 10 dk'da function_id / FUNCTION_ID anahtar
// sayımı (arrayJoin, örneklemli), kolon var mı (host başına), DOLU mu
// (countIf != ''), index var mı. Uygula = ADIM 1-2-4 (kolon spans_local +
// Distributed sarmalayıcı + set index); MATERIALIZE COLUMN AYRI eylem
// (eski part'ları yazar — disk + merge, mesai dışı); geri alma = INDEX →
// COLUMN sırası (Code 47). Pod haritaya kolonu restart'ta alır (küme
// kipinde iki-restart tuzağı) — sonuçta açıkça söylenir.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/migrations"
)

const (
	functionIDFile         = "0013_function_id.sql"
	functionIDRollbackFile = "0013_function_id_rollback.sql"
	functionIDCol          = "attr_function_id"
	functionIDIdx          = "idx_attr_function_id"
)

// FunctionIDColumnObjects — host başına varlığı izlenen nesneler.
func FunctionIDColumnObjects() []EntityLayerObject {
	return []EntityLayerObject{
		{Name: functionIDCol, Kind: "column", Table: "spans_local"},
		{Name: functionIDCol, Kind: "distributed", Table: "spans"},
		{Name: functionIDIdx, Kind: "index", Table: "spans_local"},
	}
}

type FunctionIDColumnStatusResult struct {
	Cluster string                    `json:"cluster"`
	Objects []EntityLayerObjectStatus `json:"objects"`
	// BootManaged — spans uygulama yönetimli (dış Distributed DEĞİL): boot
	// kolonu kendisi ekler, sihirbaz gereksiz (bilgi).
	BootManaged bool `json:"bootManaged"`
	// Filled/Total — son 10 dk: kolon dolu mu ("kolon var" ≠ "dolu").
	Filled    uint64 `json:"filled"`
	Total     uint64 `json:"total"`
	Generated int64  `json:"generated"`
}

type FunctionIDKeyCount struct {
	Key   string `json:"key"`
	Count uint64 `json:"count"`
}

type FunctionIDColumnPreflightResult struct {
	Clusters         []string `json:"clusters"`
	SuggestedCluster string   `json:"suggestedCluster,omitempty"`
	SpansLocal       bool     `json:"spansLocal"`
	BootManaged      bool     `json:"bootManaged"`
	// KeyCounts — son 10 dk'da hangi yazım geliyor (function_id / FUNCTION_ID / hiçbiri).
	KeyCounts    []FunctionIDKeyCount `json:"keyCounts"`
	ColumnExists bool                 `json:"columnExists"`
	IndexExists  bool                 `json:"indexExists"`
	Filled       uint64               `json:"filled"`
	Total        uint64               `json:"total"`
	ProbeErrors  []string             `json:"probeErrors,omitempty"`
	Supported    bool                 `json:"supported"`
	Detail       string               `json:"detail"`
	Generated    int64                `json:"generated"`
}

// functionIDStatements — gömülü 0013, küme adıyla, ifadelere bölünmüş
// (yorumlu ADIM 3/5 satırları düşer → tam 3 ifade). SAF; testli.
func functionIDStatements(cluster string) ([]string, error) {
	raw, err := migrations.FS.ReadFile(functionIDFile)
	if err != nil {
		return nil, fmt.Errorf("gömülü %s okunamadı: %w", functionIDFile, err)
	}
	return SplitSQLStatements(AdaptRollupDDL(string(raw), cluster)), nil
}

// functionIDRollbackStatements — gömülü rollback (INDEX → COLUMN sırası). SAF.
func functionIDRollbackStatements(cluster string) ([]string, error) {
	raw, err := migrations.FS.ReadFile(functionIDRollbackFile)
	if err != nil {
		return nil, fmt.Errorf("gömülü %s okunamadı: %w", functionIDRollbackFile, err)
	}
	return SplitSQLStatements(AdaptRollupDDL(string(raw), cluster)), nil
}

// functionIDMaterializeStatement — ADIM 5 (opsiyonel, ayrı eylem). SAF.
func functionIDMaterializeStatement(cluster string) string {
	return "ALTER TABLE spans_local ON CLUSTER " + cluster + " MATERIALIZE COLUMN " + functionIDCol
}

func (s *Store) functionIDFillProbe(ctx context.Context) (filled, total uint64, err error) {
	err = s.conn.QueryRow(ctx, `
		SELECT countIf(`+functionIDCol+` != ''), count() FROM spans
		WHERE time >= now() - INTERVAL 10 MINUTE AND time <= now()
		SETTINGS max_execution_time = 10, max_rows_to_read = 50000000, read_overflow_mode = 'break'`).Scan(&filled, &total)
	return
}

// FunctionIDColumnStatus — host başına nesne varlığı + doluluk.
func (s *Store) FunctionIDColumnStatus(ctx context.Context) (FunctionIDColumnStatusResult, error) {
	out := FunctionIDColumnStatusResult{Generated: time.Now().Unix(), Objects: []EntityLayerObjectStatus{}}
	cluster := s.entityLayerCluster(ctx)
	out.Cluster = cluster
	out.BootManaged = !s.spansIsExternalDistributed(ctx)
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
	for _, o := range FunctionIDColumnObjects() {
		st := EntityLayerObjectStatus{EntityLayerObject: o, Hosts: hosts}
		table := o.Table
		if cluster == "" {
			if o.Kind == "distributed" {
				continue // tek düğümde sarmalayıcı yok
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
	if _, colOK := s.spansColumnExpr(ctx, functionIDCol); colOK {
		if f, t, err := s.functionIDFillProbe(ctx); err == nil {
			out.Filled, out.Total = f, t
		}
	}
	return out, nil
}

// FunctionIDColumnPreflight — küme, anahtar yazımı, kolon/index/doluluk.
func (s *Store) FunctionIDColumnPreflight(ctx context.Context) (FunctionIDColumnPreflightResult, error) {
	out := FunctionIDColumnPreflightResult{
		SuggestedCluster: strings.TrimSpace(s.cfg.ClusterName),
		Generated:        time.Now().Unix(),
		Clusters:         []string{},
		KeyCounts:        []FunctionIDKeyCount{},
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
	// Anahtar yazımı (v0.9.626 dersi): iki yazımı ayrı say, örneklemli.
	kRows, err := s.conn.Query(ctx, `
		SELECT k, count() AS c FROM spans ARRAY JOIN attr_keys AS k
		WHERE time >= now() - INTERVAL 10 MINUTE AND time <= now()
		  AND lower(k) = 'function_id' AND cityHash64(trace_id) % 20 = 0
		GROUP BY k ORDER BY c DESC LIMIT 5
		SETTINGS max_execution_time = 15, max_rows_to_read = 50000000, read_overflow_mode = 'break'`)
	if err != nil {
		out.ProbeErrors = append(out.ProbeErrors, "anahtar yazımı: "+err.Error())
	} else {
		for kRows.Next() {
			var kc FunctionIDKeyCount
			if err := kRows.Scan(&kc.Key, &kc.Count); err == nil {
				kc.Count *= 20 // örneklem payı
				out.KeyCounts = append(out.KeyCounts, kc)
			}
		}
		kRows.Close()
	}
	_, out.ColumnExists = s.spansColumnExpr(ctx, functionIDCol)
	out.IndexExists = s.spansIndexExists(ctx, functionIDIdx)
	if out.ColumnExists {
		if f, t, err := s.functionIDFillProbe(ctx); err != nil {
			out.ProbeErrors = append(out.ProbeErrors, "doluluk: "+err.Error())
		} else {
			out.Filled, out.Total = f, t
		}
	}
	switch {
	case out.BootManaged:
		out.Supported = false
		out.Detail = "spans uygulama yönetimli — boot kolonu kendisi ekler (promoted_attr.go); sihirbaz gerekmez"
	case !out.SpansLocal:
		out.Supported = false
		out.Detail = "spans_local yok — dış Distributed kurulum bekleniyordu"
	case len(out.Clusters) == 0:
		out.Supported = false
		out.Detail = "system.clusters boş — ON CLUSTER DDL hedefi yok"
	case len(out.KeyCounts) == 0:
		out.Supported = true
		out.Detail = "son 10 dk'da function_id anahtarı GÖRÜLMEDİ — kolon eklenir ama boş kalır (ifade iki yazımı da okur); önce collector'ı doğrula"
	default:
		out.Supported = true
		out.Detail = "uygulanabilir: ADIM 1-2-4 (kolon + sarmalayıcı + set index; IF NOT EXISTS); MATERIALIZE COLUMN ayrı eylem"
	}
	return out, nil
}

// FunctionIDColumnApply — ADIM 1-2-4; ilk hatada durur.
func (s *Store) FunctionIDColumnApply(ctx context.Context, cluster string) []RollupStmtResult {
	c := strings.TrimSpace(cluster)
	if c == "" || !validRolloutLayerCluster(c) {
		return []RollupStmtResult{{Head: "ön koşul", Err: "cluster adı zorunlu/geçersiz — yalnız harf/rakam/_ . - (≤64)"}}
	}
	stmts, err := functionIDStatements(c)
	if err != nil {
		return []RollupStmtResult{{Head: "ön koşul", Err: err.Error()}}
	}
	return s.execStmtsStopOnError(ctx, stmts)
}

// FunctionIDColumnMaterialize — ADIM 5 (eski part'ları yazar; system.mutations'tan izlenir).
func (s *Store) FunctionIDColumnMaterialize(ctx context.Context, cluster string) []RollupStmtResult {
	c := strings.TrimSpace(cluster)
	if c == "" || !validRolloutLayerCluster(c) {
		return []RollupStmtResult{{Head: "ön koşul", Err: "cluster adı zorunlu/geçersiz"}}
	}
	return s.execStmtsStopOnError(ctx, []string{functionIDMaterializeStatement(c)})
}

// FunctionIDColumnRollback — INDEX → COLUMN; her ifade denenir.
func (s *Store) FunctionIDColumnRollback(ctx context.Context, cluster string) []RollupStmtResult {
	c := strings.TrimSpace(cluster)
	if c == "" || !validRolloutLayerCluster(c) {
		return []RollupStmtResult{{Head: "ön koşul", Err: "cluster adı zorunlu/geçersiz"}}
	}
	stmts, err := functionIDRollbackStatements(c)
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

func (s *Store) execStmtsStopOnError(ctx context.Context, stmts []string) []RollupStmtResult {
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
