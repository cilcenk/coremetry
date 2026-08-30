package chstore

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/migrations"
)

// rollout_layer_admin.go — 0012 ROLLOUTS KATMANI ŞEMASI SİHİRBAZI (v0.10.197,
// rollouts audit §5(j); entity_layer_admin.go'nun aynası).
//
//   Durum     host başına kolon / index / tablo / MV / Distributed var-yok
//             (clusterAllReplicas — dağıtık DDL'in yarım kaldığı host görünür)
//   Ön kontrol system.clusters + önerilen ad, spans_local var mı, 0011 kolonları
//             (cluster/k8s_namespace) var mı, replicaset + image KAPSAMA (son
//             15 dk, cluster kırılımlı — 2026-08-30 dersi: bir cluster tam set,
//             öteki namespace bile yok) ve LowCardinality kapısı (uniq RS adı /
//             imaj adı ≤ 100k)
//   Uygula    gömülü 0012, `uptrace_all` token'ı gerçek küme adıyla, ifade
//             ifade, İLK HATADA DUR (IF NOT EXISTS → yeniden basmak güvenli).
//             withMV=false → ADIM 6 (MV + sarmalayıcı) ATLANIR: kapsama kapısı
//             geçmeden MV'yi prod'a alma (audit R1) — Faz 1a/1b ayrımı.
//   Geri al   YALNIZ MV + sarmalayıcı — yazımı keser, kolon/tablo/veri KALIR.
//
// Boot'ta ASLA koşmaz; tek tetikleyici admin düğmesi (ev kuralı v0.9.613).

type RolloutLayerObject = EntityLayerObject
type RolloutLayerObjectStatus = EntityLayerObjectStatus

// RolloutLayerObjects — 0012'nin yarattığı her nesne (test pinler).
func RolloutLayerObjects() []RolloutLayerObject {
	return []RolloutLayerObject{
		{Name: "k8s_deployment", Kind: "column", Table: "spans_local"},
		{Name: "k8s_statefulset", Kind: "column", Table: "spans_local"},
		{Name: "k8s_daemonset", Kind: "column", Table: "spans_local"},
		{Name: "k8s_replicaset", Kind: "column", Table: "spans_local"},
		{Name: "container_image", Kind: "column", Table: "spans_local"},
		{Name: "container_image_tag", Kind: "column", Table: "spans_local"},
		{Name: "idx_k8s_namespace", Kind: "index", Table: "spans_local"},
		{Name: "idx_k8s_deployment", Kind: "index", Table: "spans_local"},
		{Name: "idx_k8s_statefulset", Kind: "index", Table: "spans_local"},
		{Name: "idx_k8s_daemonset", Kind: "index", Table: "spans_local"},
		{Name: "idx_k8s_replicaset", Kind: "index", Table: "spans_local"},
		{Name: "idx_container_image", Kind: "index", Table: "spans_local"},
		{Name: "idx_container_image_tag", Kind: "index", Table: "spans_local"},
		{Name: "workload_rollouts", Kind: "table"},
		{Name: "rollout_reconcile_runs", Kind: "table"},
		{Name: "workload_revision_activity_1m_local", Kind: "mv"},
		{Name: "workload_revision_activity_1m", Kind: "distributed"},
	}
}

// RolloutLayerStatusResult — sihirbaz kartı.
type RolloutLayerStatusResult struct {
	Cluster      string                     `json:"cluster"`
	Objects      []RolloutLayerObjectStatus `json:"objects"`
	ActivityRows uint64                     `json:"activityRows"` // MV son 15 dk (yazıyor mu kanıtı)
	Generated    int64                      `json:"generated"`
}

// RolloutLayerClusterCoverage — bir span cluster değerinin kapsaması (son 15 dk örneklem).
type RolloutLayerClusterCoverage struct {
	Cluster string `json:"cluster"`
	// Total — tam pencere sayımı (LC cluster kolonu, ucuz); Sampled — hash
	// örnekleminde görülen (kapsama oranlarının paydası). Total>0 && Sampled==0
	// = "bu cluster'ı ölçemedim" → kapı KAPALI (inceleme B2).
	Total      uint64  `json:"total"`
	Sampled    uint64  `json:"sampled"`
	ReplicaSet float64 `json:"replicaset"` // 0..1
	Image      float64 `json:"image"`      // 0..1
	Namespace  float64 `json:"namespace"`  // 0..1
}

// RolloutLayerPreflightResult — "bu küme 0012'yi kaldırır mı".
type RolloutLayerPreflightResult struct {
	Clusters         []string `json:"clusters"`
	SuggestedCluster string   `json:"suggestedCluster,omitempty"`
	SpansLocal       bool     `json:"spansLocal"`
	// Layer0011 — cluster + k8s_namespace kolonları var mı (MV onları okur).
	Layer0011 bool `json:"layer0011"`
	// Coverage — span cluster değeri başına (2026-08-30 dersi).
	Coverage []RolloutLayerClusterCoverage `json:"coverage"`
	// MVGate — her cluster'da replicaset kapsaması ≥ %95 → ADIM 6 uygulanabilir.
	MVGate      bool     `json:"mvGate"`
	UniqRS1h    uint64   `json:"uniqRs1h"`
	UniqImage1h uint64   `json:"uniqImage1h"`
	ProbeErrors []string `json:"probeErrors,omitempty"`
	Supported   bool     `json:"supported"`
	Detail      string   `json:"detail"`
	Generated   int64    `json:"generated"`
}

const (
	rolloutLayerFile   = "0012_rollout_layer.sql"
	rolloutLayerLCGate = 100_000
	// rolloutLayerMVGate — MV kapısı: her cluster'da k8s.replicaset.name
	// kapsaması (audit R1, §12 Faz 1b).
	rolloutLayerMVGate = 0.95
)

// rolloutLayerStatements — gömülü 0012, küme adıyla, ifadelere bölünmüş;
// withMV=false → MV + Distributed sarmalayıcı ifadeleri düşer (Faz 1a). Saf.
func rolloutLayerStatements(cluster string, withMV bool) ([]string, error) {
	raw, err := migrations.FS.ReadFile(rolloutLayerFile)
	if err != nil {
		return nil, fmt.Errorf("gömülü %s okunamadı: %w", rolloutLayerFile, err)
	}
	stmts := SplitSQLStatements(AdaptRollupDDL(string(raw), cluster))
	if withMV {
		return stmts, nil
	}
	out := stmts[:0:0]
	for _, s := range stmts {
		if strings.Contains(s, "workload_revision_activity_1m") {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// rolloutLayerRollbackStatements — yalnız MV + sarmalayıcı (saf, testli); SYNC şart.
func rolloutLayerRollbackStatements(cluster string) []string {
	return []string{
		"DROP TABLE IF EXISTS workload_revision_activity_1m ON CLUSTER " + cluster + " SYNC",
		"DROP TABLE IF EXISTS workload_revision_activity_1m_local ON CLUSTER " + cluster + " SYNC",
	}
}

// rolloutLayerMVGateOK — saf: adı olan HER cluster'ın örneklemi var VE RS
// kapsaması eşiğin üstünde. Sampled=0 "bu cluster'ı ölçemedim" demektir ve
// kapıyı KAPATIR (inceleme B2: eski hâli atlıyordu — 2026-08-30 olayının
// ta kendisi); ” satırı (cluster'sız, k8s dışı trafik) kapıya girmez; adı
// olan hiç cluster yoksa false.
func rolloutLayerMVGateOK(cov []RolloutLayerClusterCoverage, gate float64) bool {
	n := 0
	for _, c := range cov {
		if c.Cluster == "" {
			continue
		}
		n++
		if c.Sampled == 0 || c.ReplicaSet < gate {
			return false
		}
	}
	return n > 0
}

// rolloutLayerClusterRe — cluster adı DDL'e (`ON CLUSTER x`) ham giriyor ve
// AdaptRollupDDL ifade bölmeden ÖNCE koşuyor: ';' içeren ad ifade üretirdi
// (inceleme S5). system.clusters adları bu alfabede.
var rolloutLayerClusterRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func validRolloutLayerCluster(c string) bool { return rolloutLayerClusterRe.MatchString(c) }

// RolloutLayerStatus — host başına nesne varlığı (EntityLayerStatus aynası).
func (s *Store) RolloutLayerStatus(ctx context.Context) (RolloutLayerStatusResult, error) {
	out := RolloutLayerStatusResult{Generated: time.Now().Unix(), Objects: []RolloutLayerObjectStatus{}} // [] değil null: FE .map()
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
	for _, o := range RolloutLayerObjects() {
		st := RolloutLayerObjectStatus{EntityLayerObject: o, Hosts: hosts}
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
	var rows uint64
	if err := s.conn.QueryRow(ctx, `SELECT count() FROM workload_revision_activity_1m WHERE bucket >= now() - INTERVAL 15 MINUTE SETTINGS max_execution_time = 10`).Scan(&rows); err == nil {
		out.ActivityRows = rows
	}
	return out, nil
}

// RolloutLayerPreflight — hiçbir şey yazmaz.
func (s *Store) RolloutLayerPreflight(ctx context.Context) (RolloutLayerPreflightResult, error) {
	out := RolloutLayerPreflightResult{
		SuggestedCluster: strings.TrimSpace(s.cfg.ClusterName),
		Generated:        time.Now().Unix(),
		Clusters:         []string{}, // null değil (FE .includes)
		Coverage:         []RolloutLayerClusterCoverage{},
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
	// 0011 kolonları (MV cluster + k8s_namespace okur): spans üzerinde probe.
	_, hasCluster := s.spansColumnExpr(ctx, "cluster")
	_, hasNS := s.spansColumnExpr(ctx, "k8s_namespace")
	out.Layer0011 = hasCluster && hasNS
	// Kapsama — span cluster değeri başına, İKİ sorgu (inceleme B1/B2):
	//   (1) tam pencere sayımı yalnız LC `cluster` kolonuyla (ucuz: 15 dk ×
	//       1 bayt/satır) → hangi cluster'lar VAR (Total);
	//   (2) deterministik hash örneklemi (cityHash64(trace_id) % 50 = 0, %2)
	//       → res_keys kapsaması. Eski hâli baş-örneklemiydi (LIMIT 200000):
	//       (service_name, time) anahtarının ÖNEKİ = alfabetik ilk servisler
	//       → koca bir cluster örnekleme hiç girmez ve kapı AÇILIRDI
	//       ([[feedback-limit-by-is-prefix-sampling]]); üstelik `cluster`
	//       kolonu alt sorguda projekte edilmediği için kolonlu (= hedef)
	//       kurulumda Code 47 ile hep patlıyordu.
	// (1)'de olup (2)'de görünmeyen cluster Sampled=0 ile listelenir ve
	// kapıyı KAPATIR; '' (cluster'sız, k8s dışı) satırı görünür, kapıya
	// girmez. Alias anahtarlar (k8s.container.image.name /
	// kubernetes.namespace.name) terfi kolonuyla aynı coalesce (S9).
	// cluster kolonu yoksa kapsama ölçülmez — Layer0011=false zaten
	// Supported=false.
	if hasCluster {
		type covAgg struct{ sampled, rs, img, ns uint64 }
		samples := map[string]covAgg{}
		covRows, err := s.conn.Query(ctx, `
			SELECT cluster AS c, count() AS sampled,
			       countIf(has(res_keys, 'k8s.replicaset.name')) AS rs,
			       countIf(has(res_keys, 'container.image.name') OR has(res_keys, 'k8s.container.image.name')) AS img,
			       countIf(has(res_keys, 'k8s.namespace.name') OR has(res_keys, 'kubernetes.namespace.name')) AS ns
			FROM spans
			WHERE time >= now() - INTERVAL 15 MINUTE AND time <= now() AND cityHash64(trace_id) % 50 = 0
			GROUP BY c ORDER BY sampled DESC LIMIT 50
			SETTINGS max_execution_time = 15, max_rows_to_read = 50000000, read_overflow_mode = 'break'`)
		if err != nil {
			out.ProbeErrors = append(out.ProbeErrors, "kapsama: "+err.Error())
		} else {
			for covRows.Next() {
				var c string
				var a covAgg
				if err := covRows.Scan(&c, &a.sampled, &a.rs, &a.img, &a.ns); err == nil {
					samples[c] = a
				}
			}
			covRows.Close()
		}
		totRows, err := s.conn.Query(ctx, `
			SELECT cluster AS c, count() AS total FROM spans
			WHERE time >= now() - INTERVAL 15 MINUTE AND time <= now()
			GROUP BY c ORDER BY total DESC LIMIT 50
			SETTINGS max_execution_time = 15`)
		if err != nil {
			out.ProbeErrors = append(out.ProbeErrors, "cluster sayımı: "+err.Error())
		} else {
			for totRows.Next() {
				var c string
				var total uint64
				if err := totRows.Scan(&c, &total); err != nil || total == 0 {
					continue
				}
				a := samples[c]
				row := RolloutLayerClusterCoverage{Cluster: c, Total: total, Sampled: a.sampled}
				if a.sampled > 0 {
					row.ReplicaSet = float64(a.rs) / float64(a.sampled)
					row.Image = float64(a.img) / float64(a.sampled)
					row.Namespace = float64(a.ns) / float64(a.sampled)
				}
				out.Coverage = append(out.Coverage, row)
			}
			totRows.Close()
		}
	}
	out.MVGate = out.Layer0011 && rolloutLayerMVGateOK(out.Coverage, rolloutLayerMVGate)
	// LC kapısı: 1 saatlik pencerede %5 hash örneklemi + okuma tavanı (S6 —
	// sınırsız tarama 20 s'de hata verip sihirbazı kapatıyordu). Örneklem
	// kardinaliteyi hafif küçümser; saatte ≥20 span basan her RS/imaj adı
	// yine görülür, eşik 100k.
	if err := s.conn.QueryRow(ctx, `
		SELECT uniq(res_values[indexOf(res_keys, 'k8s.replicaset.name')]), uniq(res_values[indexOf(res_keys, 'container.image.name')])
		FROM spans WHERE time >= now() - INTERVAL 1 HOUR AND time <= now()
		  AND has(res_keys, 'k8s.replicaset.name') AND cityHash64(trace_id) % 20 = 0
		SETTINGS max_execution_time = 20, max_rows_to_read = 100000000, read_overflow_mode = 'break'`).Scan(&out.UniqRS1h, &out.UniqImage1h); err != nil {
		out.ProbeErrors = append(out.ProbeErrors, "uniq rs/image: "+err.Error())
	}
	switch {
	case len(out.ProbeErrors) > 0:
		out.Detail = "probe hatası — emin olamadığımız kümeye DDL basmıyoruz"
	case !out.SpansLocal:
		out.Detail = "spans_local yok — bu kurulum tek düğüm; 0012 dağıtık şema içindir (uygulama boot'ta kendi kurar)"
	case !out.Layer0011:
		out.Detail = "0011 kolonları (cluster / k8s_namespace) yok — önce K8s entity katmanı (0011)"
	case out.UniqRS1h > rolloutLayerLCGate || out.UniqImage1h > rolloutLayerLCGate:
		out.Detail = fmt.Sprintf("son 1 saatte %d RS adı / %d imaj adı > %d — LowCardinality kapısı; dosyayı düz String'e çevirip elle uygula", out.UniqRS1h, out.UniqImage1h, rolloutLayerLCGate)
	default:
		out.Supported = true
		if out.MVGate {
			out.Detail = fmt.Sprintf("uygulanabilir — her cluster'da replicaset kapsaması ≥ %%%.0f; MV (ADIM 6) dahil", rolloutLayerMVGate*100)
		} else {
			out.Detail = "uygulanabilir (kolon + index + tablolar) — MV kapısı KAPALI: en az bir cluster'da k8s.replicaset.name kapsaması eşiğin altında (collector) — ADIM 6 atlanır"
		}
	}
	return out, nil
}

// RolloutLayerApply — gömülü 0012, ifade ifade; ilk hatada durur. withMV
// yalnız kapı açıkken (çağıran preflight'a bakar).
func (s *Store) RolloutLayerApply(ctx context.Context, cluster string, withMV bool) []RollupStmtResult {
	c := strings.TrimSpace(cluster)
	if c == "" {
		return []RollupStmtResult{{Head: "ön koşul", Err: "cluster adı zorunlu — DDL `ON CLUSTER` yazıyor"}}
	}
	if !validRolloutLayerCluster(c) {
		return []RollupStmtResult{{Head: "ön koşul", Err: "cluster adı geçersiz — yalnız harf/rakam/_ . - (≤64)"}}
	}
	stmts, err := rolloutLayerStatements(c, withMV)
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

// RolloutLayerRollback — yalnız MV; ilk hatada DURMAZ.
func (s *Store) RolloutLayerRollback(ctx context.Context, cluster string) []RollupStmtResult {
	c := strings.TrimSpace(cluster)
	if c == "" {
		return []RollupStmtResult{{Head: "ön koşul", Err: "cluster adı zorunlu"}}
	}
	if !validRolloutLayerCluster(c) {
		return []RollupStmtResult{{Head: "ön koşul", Err: "cluster adı geçersiz — yalnız harf/rakam/_ . - (≤64)"}}
	}
	stmts := rolloutLayerRollbackStatements(c)
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
