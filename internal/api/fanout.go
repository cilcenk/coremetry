package api

// fanout.go — v0.10.439 (CoSRE router boşlukları D4, son dilim): "A'dan
// B'ye gidenlerin hepsi C'ye de gidiyor mu, istek başına ortalama kaç C
// çağrısı?" Örnek tabanlı (spec kararı): A ve B'yi birlikte içeren en yeni
// ≤200 trace (RequireServices) → spans kenar yürüyüşü (SpanEdgesForTraces)
// → A→B kenarı olan trace'lerde B→C kenarı olanların oranı + trace başına
// B→C çağrı sayısı (ort/en çok). C servis değilse (dış/DB düğüm) trace
// yürüyüşü onu göremez (child span yok) → topology_edges_5m oranı
// (B→C çağrı / A→B çağrı) dürüst notla. Kanıt "örnek" der, kesin sayım
// iddia etmez.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

const fanoutSample = 200

// hasFanoutSignal — "hepsi/tamamı … de gidiyor mu", "istek başına",
// "ortalama kaç", "fan-out", "all … also".
func hasFanoutSignal(toks []string) bool {
	return tokenHasPrefix(toks, "hepsi", "tamamı", "tamami", "fanout", "fan-out", "başına", "basina", "per") ||
		(tokenHasPrefix(toks, "ortalama", "average", "avg") && tokenHasPrefix(toks, "kaç", "kac", "how")) ||
		(tokenHasPrefix(toks, "all") && tokenHasPrefix(toks, "also", "too"))
}

// splitFanoutFragments — SAF: A, B (D2 bölücüsü) + B'den sonraki ilk
// yönelme ("C'ye", "C servisine", "to C") → C.
func splitFanoutFragments(raw string) (a, b, c string, ok bool) {
	a, b, ok = splitPairFragments(raw)
	if !ok {
		return "", "", "", false
	}
	words := strings.Fields(raw)
	seenB, expectC := false, false
	prev := ""
	for _, w := range words {
		base, hit := stripPairSuffix(w, pairDativeRe)
		lw := strings.ToLower(w)
		if !seenB {
			if (hit && strings.EqualFold(cleanPairFragment(base), b)) || strings.EqualFold(cleanPairFragment(w), b) || lw == "servisine" {
				seenB = true
			}
			prev = w
			continue
		}
		switch {
		case expectC:
			return a, b, cleanPairFragment(cutAtRequestWord([]string{w})[0]), cleanPairFragment(w) != ""
		case hit:
			if cc := cleanPairFragment(base); cc != "" && !strings.EqualFold(cc, b) {
				return a, b, cc, true
			}
		case lw == "servisine" || lw == "servise":
			if cc := cleanPairFragment(prev); cc != "" && !strings.EqualFold(cc, b) {
				return a, b, cc, true
			}
		case lw == "to":
			expectC = true
		}
		prev = w
	}
	return "", "", "", false
}

// fanoutStats — SAF.
type fanoutStats struct {
	Sampled  int // A ve B'yi içeren örnek trace
	WithAB   int // A→B kenarı olan
	WithABC  int // A→B VE B→C kenarı olan
	WithAC   int // A→C doğrudan kenarı olan (bilgi)
	SumBC    int // A→B'li trace'lerde B→C çağrı toplamı
	MaxBC    int
	NoABList int // A ve B var ama A→B kenarı yok (dolaylı)
}

func computeFanout(edges map[string]map[chstore.TraceEdge]int, a, b, c string) fanoutStats {
	var st fanoutStats
	for _, e := range edges {
		st.Sampled++
		ab := e[chstore.TraceEdge{Parent: a, Child: b}]
		if ab == 0 {
			st.NoABList++
			continue
		}
		st.WithAB++
		bc := e[chstore.TraceEdge{Parent: b, Child: c}]
		if bc > 0 {
			st.WithABC++
			st.SumBC += bc
			if bc > st.MaxBC {
				st.MaxBC = bc
			}
		}
		if e[chstore.TraceEdge{Parent: a, Child: c}] > 0 {
			st.WithAC++
		}
	}
	return st
}

func (st fanoutStats) avgBC() float64 {
	if st.WithAB == 0 {
		return 0
	}
	return float64(st.SumBC) / float64(st.WithAB)
}

func renderFanoutTR(route guidedRoute, st fanoutStats, rangeS int64) string {
	a, b, c := route.PairFrom, route.PairTo, route.FanoutTo
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s → %s → %s fan-out (örnek: %s ve %s'yi birlikte içeren en yeni %d trace, son %s):\n", a, b, c, a, b, st.Sampled, fmtAgoTR(rangeS))
	if st.Sampled == 0 {
		sb.WriteString("Örnekte trace yok — bunu dürüstçe söyle (pencere ya da adlar).\n")
		return sb.String()
	}
	fmt.Fprintf(&sb, "- A→B doğrudan kenarı olan trace: %d/%d", st.WithAB, st.Sampled)
	if st.NoABList > 0 {
		fmt.Fprintf(&sb, " (%d trace'te ikisi de var ama doğrudan %s→%s çağrısı yok — dolaylı)", st.NoABList, a, b)
	}
	sb.WriteString(".\n")
	if st.WithAB == 0 {
		sb.WriteString("A→B kenarı olan trace olmadığından fan-out hesaplanamadı.\n")
		return sb.String()
	}
	pct := float64(st.WithABC) / float64(st.WithAB) * 100
	fmt.Fprintf(&sb, "- Bunların %s'ye de gidenleri: %d/%d (%%%.0f). ", c, st.WithABC, st.WithAB, pct)
	switch {
	case st.WithABC == st.WithAB:
		sb.WriteString("Örnekte HEPSİ gidiyor.")
	case st.WithABC == 0:
		sb.WriteString("Örnekte HİÇBİRİ gitmiyor.")
	default:
		sb.WriteString("Örnekte bir kısmı gidiyor.")
	}
	sb.WriteString("\n")
	fmt.Fprintf(&sb, "- A→B'li istek başına %s→%s çağrısı: ortalama %.2f, en çok %d (giden trace'ler arasında ortalama %.2f).\n", b, c, st.avgBC(), st.MaxBC, func() float64 {
		if st.WithABC == 0 {
			return 0
		}
		return float64(st.SumBC) / float64(st.WithABC)
	}())
	if st.WithAC > 0 {
		fmt.Fprintf(&sb, "- Ayrıca %d trace'te %s doğrudan %s'yi de çağırıyor.\n", st.WithAC, a, c)
	}
	sb.WriteString("Yorum: 'hepsi mi' sorusuna örnek oranıyla cevap ver (kesin sayım değil, en yeni 200 trace); istek başına ortalamayı söyle; kanıtta olmayan servis/sayı uydurma.\n")
	return sb.String()
}

// guidedFanoutBundle — örnek + yürüyüş; C düğümse topology oranı.
func (s *Server) guidedFanoutBundle(ctx context.Context, emit func(string, any), route *guidedRoute, from, to time.Time, rangeS int64) (string, string, error) {
	a, b, c := route.PairFrom, route.PairTo, route.FanoutTo
	if route.FanoutToKind != "service" {
		// Dış/DB düğüm: trace yürüyüşü child span görmez → MV oranı.
		n := emitGuidedStep(emit, "topology_edges", fmt.Sprintf(`{"from":%q,"via":%q,"to":%q}`, a, b, c))
		edgesA, err := s.store.ReadServiceTopologyAggForFocus(ctx, from, to, a, 1, 20000)
		if err != nil {
			emitGuidedStepResult(emit, n, "topology_edges", "", err)
			return "", "", err
		}
		edgesB, err := s.store.ReadServiceTopologyAggForFocus(ctx, from, to, b, 1, 20000)
		if err != nil {
			emitGuidedStepResult(emit, n, "topology_edges", "", err)
			return "", "", err
		}
		ab, _ := matchPairEdges(edgesA, a, b, true)
		bc, _ := matchPairEdges(edgesB, b, c, false)
		var callsAB, callsBC uint64
		for _, e := range ab {
			callsAB += e.Calls
		}
		for _, e := range bc {
			callsBC += e.Calls
		}
		emitGuidedStepResult(emit, n, "topology_edges", fmt.Sprintf("A→B %d, B→C %d", callsAB, callsBC), nil)
		var sb strings.Builder
		fmt.Fprintf(&sb, "%s → %s → %s fan-out (%s bir servis değil, dış/DB düğüm: trace yürüyüşü child span görmez; topology_edges_5m kova toplamları, son %s):\n", a, b, c, c, fmtAgoTR(rangeS))
		fmt.Fprintf(&sb, "- %s→%s çağrı: %d; %s→%s çağrı: %d.\n", a, b, callsAB, b, c, callsBC)
		if callsAB > 0 {
			fmt.Fprintf(&sb, "- Oran: A→B çağrısı başına ~%.2f %s çağrısı (kova toplamı oranı — trace başına değil; %s'nin başka çağıranları da bu sayıya girer).\n", float64(callsBC)/float64(callsAB), c, b)
		} else {
			sb.WriteString("- A→B çağrısı yok; oran hesaplanamadı.\n")
		}
		sb.WriteString("Yorum: 'hepsi mi'ye kesin cevap verilemez (yönlü trace örneği yok); oranı ve sınırını söyle.\n")
		return sb.String(), fmt.Sprintf("topology_edges_5m (%s→%s, %s→%s, son %s)", a, b, b, c, fmtAgoTR(rangeS)), nil
	}
	n := emitGuidedStep(emit, "traces", fmt.Sprintf(`{"services":[%q,%q],"limit":%d}`, a, b, fanoutSample))
	rows, _, _, err := s.store.GetTraces(ctx, chstore.TraceFilter{Service: a, Env: route.Env, From: from, To: to, RequireServices: []string{a, b}, Limit: fanoutSample, CountMode: "skip"})
	if err != nil {
		emitGuidedStepResult(emit, n, "traces", "", err)
		return "", "", err
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.TraceID)
	}
	emitGuidedStepResult(emit, n, "traces", fmt.Sprintf("%d trace", len(ids)), nil)
	n2 := emitGuidedStep(emit, "trace_edges", fmt.Sprintf(`{"traces":%d}`, len(ids)))
	edges, err := s.store.SpanEdgesForTraces(ctx, ids, from, to)
	if err != nil {
		emitGuidedStepResult(emit, n2, "trace_edges", "", err)
		return "", "", err
	}
	st := computeFanout(edges, a, b, c)
	emitGuidedStepResult(emit, n2, "trace_edges", fmt.Sprintf("A→B %d, →C %d", st.WithAB, st.WithABC), nil)
	src := fmt.Sprintf("örnek %d trace (%s ve %s'yi birlikte içeren, en yeni; son %s) + span kenar yürüyüşü", st.Sampled, a, b, fmtAgoTR(rangeS))
	return renderFanoutTR(*route, st, rangeS), src, nil
}
