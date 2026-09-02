package api

// rollout_problems.go — v0.10.244 Problem↔Rollout korelasyonu D4: rollout
// akışı "problemler" rozeti. Rota YOK (listRollouts içinde çağrılır);
// api.go büyümez.
//
// Sayım = çekmecenin sayımı (rollout_detail.go buildRolloutDetail):
// rollout başladığından beri AÇIK problemler ∩ rollout'un taşıdığı
// servisler (workload_revision_activity_1m). Fark: çekmece rollout başına
// bir MV sorgusu atar; liste 200 satır için TEK toplu sorgu
// (RolloutServicesBatch) + tek açık-problem anlık görüntüsü. Şema yok
// (audit K5). Küme kaydı registry'de yoksa o satır 0 kalır (çekmece de
// "servisler çözülemez" der).

import (
	"context"
	"log"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// rolloutRowServices — satır → span cluster değerleri (EffectiveID'nin
// SpanClusterKeys'i); registry'de yoksa boş.
type rolloutRowServices struct {
	clusters []string
}

// countProblemsCaused — SAF: her satır için başlangıcından beri açık ve
// satırın servislerinde olan problem sayısı. svcByKey RolloutServicesBatch
// çıktısı; clustersByRow satırın span cluster değerleri (registry
// çevirisi). Test: rollout_problems_test.go.
func countProblemsCaused(rows []chstore.RolloutRow, clustersByRow [][]string, svcByKey map[chstore.RolloutServiceKey][]string, open []*chstore.Problem) []int {
	out := make([]int, len(rows))
	for i, row := range rows {
		inSet := map[string]bool{}
		for _, c := range clustersByRow[i] {
			for _, svc := range svcByKey[chstore.RolloutServiceKey{Cluster: c, Namespace: row.Namespace, Workload: row.Workload, Revision: row.Revision}] {
				inSet[svc] = true
			}
		}
		if len(inSet) == 0 {
			continue
		}
		sinceNs := row.StartedAt.UnixNano()
		n := 0
		for _, p := range open {
			if p.StartedAt >= sinceNs && inSet[p.Service] {
				n++
			}
		}
		out[i] = n
	}
	return out
}

// attachProblemsCaused — listRollouts yükleyicisinde; hata = rozet yok
// (log), liste yine döner.
func (s *Server) attachProblemsCaused(ctx context.Context, rows []chstore.RolloutRow) {
	if len(rows) == 0 {
		return
	}
	clustersByRow := make([][]string, len(rows))
	var keys []chstore.RolloutServiceKey
	seen := map[chstore.RolloutServiceKey]bool{}
	since := rows[0].StartedAt
	for i, row := range rows {
		if c, ok := s.resolveCluster(row.ClusterID); ok {
			clustersByRow[i] = c.SpanClusterKeys()
		}
		for _, cv := range clustersByRow[i] {
			k := chstore.RolloutServiceKey{Cluster: cv, Namespace: row.Namespace, Workload: row.Workload, Revision: row.Revision}
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
		if !row.StartedAt.IsZero() && row.StartedAt.Before(since) {
			since = row.StartedAt
		}
	}
	if len(keys) == 0 {
		return
	}
	svcByKey, err := s.store.RolloutServicesBatch(ctx, keys, since.Add(-time.Hour))
	if err != nil {
		log.Printf("[rollouts] problemsCaused services: %v", err)
		return
	}
	snapshot, err := s.store.OpenProblemsSnapshot(ctx)
	if err != nil {
		log.Printf("[rollouts] problemsCaused snapshot: %v", err)
		return
	}
	counts := countProblemsCaused(rows, clustersByRow, svcByKey, snapshot.All())
	for i := range rows {
		rows[i].ProblemsCaused = counts[i]
	}
}
