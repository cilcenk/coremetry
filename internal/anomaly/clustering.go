package anomaly

import (
	"sort"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/correlator"
)

// clustering.go — anomali kümeleme (v0.9.1069, F1.6-R2; spec:
// docs/plans/spec-anomaly-clustering.md, operatör-onaylı seçenekler).
// Paylaşılan bir olay (Oracle, ağ) N servisi vurduğunda N bağımsız
// Problem yerine: propagation-sıralı KAYNAK serviste tek Problem +
// üyeler bağlı kanıt. Bu dosya SAF tespit — hangi adaylar bir küme
// oluşturur; açma/bildirim (R3) scan tarafında.

// clusterMinMembers — kaynak DAHİL en az üye. Spec kararı: ikili
// vakalarda yanlış birleştirme, ayrı kalmaktan pahalı.
const clusterMinMembers = 3

// openCandidate — bu tikte "open" kararı almış bir (servis, metrik).
type openCandidate struct {
	Service string
	Metric  string
	Outcome anomalyOutcome
}

// anomalyCluster — tespit edilen bir küme. Source aday kümesinin
// İÇİNDEN, üyelerinin propagation-suçladığı servis; Members kaynak
// HARİÇ, suçlayanlar (deterministik sıralı).
type anomalyCluster struct {
	Source      string
	SourceScore float64 // suçlayanların kaynak-skorları toplamı (rapor için)
	Members     []openCandidate
}

// detectAnomalyClusters — saf, tablo-testli. Kural:
//   - Aday servislerden her X için RankRootCausesFromEdges(adj, X)
//     koşar; X'in skorlu (>0) nedenlerinden aday kümesinde OLANLAR
//     "X, S'yi suçluyor" kenarı üretir (en yüksek skorlu S seçilir —
//     bir aday tek kümeye girer).
//   - Kaynak S: kendisi de ADAY olan ve kaynak dahil ≥minMembers üye
//     toplayan servis. Spec: skorsuz/kaynaksız vakalarda kümeleme YOK —
//     bugünkü bağımsız davranış sürer.
//   - Determinizm: kaynaklar üye-sayısı desc, sonra skor desc, sonra ad;
//     üyeler servis/metrik sıralı. Aynı girdi aynı çıktı.
func detectAnomalyClusters(cands []openCandidate, weightedAdj []chstore.ServiceEdgePair, minMembers int) []anomalyCluster {
	if minMembers < 2 || len(cands) < minMembers {
		return nil
	}
	inSet := map[string]bool{}
	for _, c := range cands {
		inSet[c.Service] = true
	}

	// Her aday SERVİS için en güçlü aday-kümesi-içi suçlu.
	type blame struct {
		source string
		score  float64
	}
	blames := map[string]blame{} // service -> suçladığı kaynak
	for svc := range inSet {
		best := blame{}
		for _, sc := range correlator.RankRootCausesFromEdges(weightedAdj, svc) {
			if sc.Score <= 0 || sc.Service == svc || !inSet[sc.Service] {
				continue
			}
			if sc.Score > best.score {
				best = blame{source: sc.Service, score: sc.Score}
			}
		}
		if best.source != "" {
			blames[svc] = best
		}
	}

	// Kaynak başına suçlayan servisleri topla.
	bySource := map[string][]string{}
	scoreSum := map[string]float64{}
	for svc, b := range blames {
		bySource[b.source] = append(bySource[b.source], svc)
		scoreSum[b.source] += b.score
	}

	// Kaynak adayları: kendisi de aday + (kaynak dahil) ≥minMembers.
	type srcCand struct {
		source string
		count  int
		score  float64
	}
	var sources []srcCand
	for src, blamers := range bySource {
		if !inSet[src] {
			continue
		}
		if 1+len(blamers) < minMembers {
			continue
		}
		sources = append(sources, srcCand{source: src, count: len(blamers), score: scoreSum[src]})
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].count != sources[j].count {
			return sources[i].count > sources[j].count
		}
		if sources[i].score != sources[j].score {
			return sources[i].score > sources[j].score
		}
		return sources[i].source < sources[j].source
	})

	candsByService := map[string][]openCandidate{}
	for _, c := range cands {
		candsByService[c.Service] = append(candsByService[c.Service], c)
	}

	assigned := map[string]bool{} // servis tek kümeye girer
	var out []anomalyCluster
	for _, sc := range sources {
		if assigned[sc.source] {
			continue
		}
		var members []openCandidate
		for _, blamer := range bySource[sc.source] {
			if assigned[blamer] || blames[blamer].source != sc.source {
				continue
			}
			members = append(members, candsByService[blamer]...)
		}
		memberServices := map[string]bool{}
		for _, m := range members {
			memberServices[m.Service] = true
		}
		if 1+len(memberServices) < minMembers {
			continue // atamalar sonrası küme eridi
		}
		sort.Slice(members, func(i, j int) bool {
			if members[i].Service != members[j].Service {
				return members[i].Service < members[j].Service
			}
			return members[i].Metric < members[j].Metric
		})
		assigned[sc.source] = true
		for s := range memberServices {
			assigned[s] = true
		}
		out = append(out, anomalyCluster{Source: sc.source, SourceScore: sc.score, Members: members})
	}
	return out
}
