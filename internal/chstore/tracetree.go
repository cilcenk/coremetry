package chstore

// tracetree.go — v0.10.275 (trace view Dilim 1b, docs/audit/trace-view.md §4).
// Span ağacı + kritik yol + öz süre + servis özeti TEK yerde, Go'da, saf:
// GetTrace'in döndürdüğü []SpanRow üstünde çalışır, CH'ye dokunmaz. Frontend
// ağacı iki kez kuruyor (TraceWaterfall + Trace.tsx), kritik yolu ayrıca DFS
// yapıyor ve İKİ farklı öz-süre tanımı taşıyordu (şeritte naif toplam, panelde
// aralık birleşimi) — tek tanım burada: öz süre = süre − doğrudan çocukların
// ARALIK BİRLEŞİMİ (ebeveyne kırpılmış), asla negatif (lib/selfTime.ts sözleşmesi).
// Kritik yol = lib/criticalPath.ts ile aynı kural: en uzun kök; her adımda
// ebeveynin bitişini AŞMAYAN en uzun zincirli çocuk; toplam = zincir sürelerinin
// toplamı. Özyineleme YOK (50k tavanı, derin trace): DFS yığınla, birikimler
// DFS sırasının tersiyle. Tablo-testli (tracetree_test.go).

import "sort"

// TraceNode — ağaç düğümü; frontend sıralamayı TEKRAR yapmaz (Order = DFS sırası).
type TraceNode struct {
	SpanID        string `json:"spanId"`
	ParentSpanID  string `json:"parentSpanId,omitempty"`
	Depth         uint16 `json:"depth"`
	Order         uint32 `json:"order"`
	ChildCount    uint32 `json:"childCount"`
	SubtreeCount  uint32 `json:"subtreeCount"`
	SubtreeErrors uint32 `json:"subtreeErrors"`
	SubtreeNs     int64  `json:"subtreeNs"` // alt ağacın duvar saati (min start → max end)
	SelfNs        int64  `json:"selfNs"`    // aralık birleşimli öz süre
	Critical      bool   `json:"critical,omitempty"`
}

// TraceServiceSummary — servis başına özet (şeridin naif hesabının yerine).
type TraceServiceSummary struct {
	Service    string  `json:"service"`
	SpanCount  uint32  `json:"spanCount"`
	ErrorCount uint32  `json:"errorCount"`
	SelfNs     int64   `json:"selfNs"`
	SelfPct    float64 `json:"selfPct"`
	EntryCount uint32  `json:"entryCount"` // servise giriş (ebeveyn başka servis ya da kök)
}

// TraceAnalysis — GET /api/traces/{id} yanıtına EK ALAN (`analysis`); eski
// istemciler alanı yok sayar. Version alan ANLAMI değişirse artar.
type TraceAnalysis struct {
	Version     int                   `json:"v"`
	Nodes       []TraceNode           `json:"nodes"`
	CriticalNs  int64                 `json:"criticalNs"`
	CriticalIDs []string              `json:"criticalIds"`
	Services    []TraceServiceSummary `json:"services"`
	RootSpanID  string                `json:"rootSpanId"`
	OrphanCount uint32                `json:"orphanCount"`
	Truncated   bool                  `json:"truncated"`
}

const traceAnalysisVersion = 1

type ttNode struct {
	idx      int
	children []int
	depth    uint16
	// biriktirilenler
	subCount, subErr uint32
	subMin, subMax   int64
	selfNs           int64
	chainNs          int64 // kritik zincir toplamı (bu düğümden aşağı)
	chainNext        int   // -1 = yaprak
}

func spanDur(s *SpanRow) int64 {
	if s.EndTime <= s.StartTime {
		return 0
	}
	return s.EndTime - s.StartTime
}

// BuildTraceAnalysis — SAF. spans GetTrace sırasıyla (time ASC) gelir; ağaç
// sırası kökler başlangıç zamanına, çocuklar giriş sırasına göre.
func BuildTraceAnalysis(spans []SpanRow, truncated bool) TraceAnalysis {
	out := TraceAnalysis{Version: traceAnalysisVersion, Truncated: truncated, Nodes: []TraceNode{}, CriticalIDs: []string{}, Services: []TraceServiceSummary{}}
	n := len(spans)
	if n == 0 {
		return out
	}
	byID := make(map[string]int, n)
	for i := range spans {
		if spans[i].SpanID == "" {
			continue
		}
		if _, dup := byID[spans[i].SpanID]; !dup {
			byID[spans[i].SpanID] = i
		}
	}
	nodes := make([]ttNode, n)
	var roots []int
	for i := range spans {
		nodes[i] = ttNode{idx: i, chainNext: -1}
		s := &spans[i]
		if s.SpanID == "" || byID[s.SpanID] != i {
			continue // boş ya da kopya id: ağaca girmez
		}
		if p, ok := byID[s.ParentSpanID]; ok && s.ParentSpanID != "" && p != i {
			nodes[p].children = append(nodes[p].children, i)
			continue
		}
		if s.ParentSpanID != "" {
			out.OrphanCount++
		}
		roots = append(roots, i)
	}
	sort.SliceStable(roots, func(a, b int) bool { return spans[roots[a]].StartTime < spans[roots[b]].StartTime })

	// DFS (yığın) — sıra + derinlik.
	order := make([]int, 0, n)
	type frame struct {
		idx   int
		depth uint16
	}
	stack := make([]frame, 0, 64)
	for r := len(roots) - 1; r >= 0; r-- {
		stack = append(stack, frame{roots[r], 0})
	}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		nodes[f.idx].depth = f.depth
		order = append(order, f.idx)
		ch := nodes[f.idx].children
		for c := len(ch) - 1; c >= 0; c-- {
			stack = append(stack, frame{ch[c], f.depth + 1})
		}
	}

	// Birikimler DFS sırasının TERSİYLE (çocuklar ebeveynden önce).
	for k := len(order) - 1; k >= 0; k-- {
		i := order[k]
		s := &spans[i]
		nd := &nodes[i]
		dur := spanDur(s)
		nd.subCount = 1
		if s.StatusCode == "error" {
			nd.subErr = 1
		}
		nd.subMin, nd.subMax = s.StartTime, s.StartTime+dur
		bestChain := int64(-1)
		nd.chainNext = -1
		parentEnd := s.StartTime + dur
		ivs := make([][2]int64, 0, len(nd.children))
		for _, c := range nd.children {
			cn := &nodes[c]
			nd.subCount += cn.subCount
			nd.subErr += cn.subErr
			if cn.subMin < nd.subMin {
				nd.subMin = cn.subMin
			}
			if cn.subMax > nd.subMax {
				nd.subMax = cn.subMax
			}
			cs := &spans[c]
			cd := spanDur(cs)
			a, b := cs.StartTime, cs.StartTime+cd
			if a < s.StartTime {
				a = s.StartTime
			}
			if b > parentEnd {
				b = parentEnd
			}
			if b > a {
				ivs = append(ivs, [2]int64{a, b})
			}
			// kritik zincir: ebeveynin bitişini aşan çocuk zincire giremez
			if cs.StartTime+cd <= parentEnd && cn.chainNs > bestChain {
				bestChain = cn.chainNs
				nd.chainNext = c
			}
		}
		sort.Slice(ivs, func(x, y int) bool { return ivs[x][0] < ivs[y][0] })
		var covered int64
		curA, curB := int64(0), int64(-1)
		started := false
		for _, iv := range ivs {
			if !started || iv[0] > curB {
				if started && curB > curA {
					covered += curB - curA
				}
				curA, curB, started = iv[0], iv[1], true
			} else if iv[1] > curB {
				curB = iv[1]
			}
		}
		if started && curB > curA {
			covered += curB - curA
		}
		nd.selfNs = dur - covered
		if nd.selfNs < 0 {
			nd.selfNs = 0
		}
		nd.chainNs = dur
		if bestChain >= 0 {
			nd.chainNs += bestChain
		}
	}

	// Kritik yol: en uzun kökten zincir.
	critical := map[int]bool{}
	if len(roots) > 0 {
		best := roots[0]
		for _, r := range roots[1:] {
			if spanDur(&spans[r]) > spanDur(&spans[best]) {
				best = r
			}
		}
		out.RootSpanID = spans[best].SpanID
		out.CriticalNs = nodes[best].chainNs
		for cur := best; cur >= 0; cur = nodes[cur].chainNext {
			critical[cur] = true
			out.CriticalIDs = append(out.CriticalIDs, spans[cur].SpanID)
		}
	}

	// Düğüm listesi (DFS sırası) + servis özeti.
	out.Nodes = make([]TraceNode, 0, len(order))
	svcIdx := map[string]int{}
	var totalSelf int64
	for k, i := range order {
		s := &spans[i]
		nd := &nodes[i]
		out.Nodes = append(out.Nodes, TraceNode{
			SpanID: s.SpanID, ParentSpanID: s.ParentSpanID, Depth: nd.depth, Order: uint32(k),
			ChildCount: uint32(len(nd.children)), SubtreeCount: nd.subCount, SubtreeErrors: nd.subErr,
			SubtreeNs: nd.subMax - nd.subMin, SelfNs: nd.selfNs, Critical: critical[i],
		})
		si, ok := svcIdx[s.ServiceName]
		if !ok {
			si = len(out.Services)
			svcIdx[s.ServiceName] = si
			out.Services = append(out.Services, TraceServiceSummary{Service: s.ServiceName})
		}
		sv := &out.Services[si]
		sv.SpanCount++
		if s.StatusCode == "error" {
			sv.ErrorCount++
		}
		sv.SelfNs += nd.selfNs
		totalSelf += nd.selfNs
		if p, ok := byID[s.ParentSpanID]; !ok || s.ParentSpanID == "" || spans[p].ServiceName != s.ServiceName {
			sv.EntryCount++
		}
	}
	for i := range out.Services {
		if totalSelf > 0 {
			out.Services[i].SelfPct = float64(out.Services[i].SelfNs) / float64(totalSelf) * 100
		}
	}
	sort.SliceStable(out.Services, func(a, b int) bool { return out.Services[a].SelfNs > out.Services[b].SelfNs })
	return out
}
