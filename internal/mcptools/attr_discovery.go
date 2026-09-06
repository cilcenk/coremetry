package mcptools

// attr_discovery.go — v0.10.472 (CoSRE Telemetry Agent Faz 3, F3-1; audit
// G4/G5): describe_attributes + find_attribute_by_value. Kural (audit §6,
// Ek A adım 3): span attribute'una süzgeç koymadan ÖNCE anahtarı keşfet —
// model anahtar UYDURMAZ. "apigateway.example.com" server.address'te mi,
// http.host'ta mı, url.full'un içinde mi: örneklem söyler, prob doğrular.
//
// Dürüstlük: örneklem 5000 span (sample_relative:true, sayılar oran değil
// sıra bilgisi); kesin sayı yalnız kolon/kvh probu varken (basis). Kapsam
// zorunlu (servis / namespace / cluster) — filo geneli dizi taraması yok
// (Traces perf tuzağı). Pencere varsayılan 30 dk, prob ≤ 6 saat.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/mcp"
)

const (
	attrProbeMaxRangeS = 6 * 3600
	attrProbeMaxKeys   = 6
)

// valueKeyDictionary — bir değerin ŞEKLİNE göre aday anahtarlar (örneklemde
// görünmese de probe edilir): host → adres anahtarları, yol → route/path.
var valueKeyDictionary = []struct {
	shape string
	keys  []string
}{
	{"host", []string{"server.address", "http.host", "net.peer.name", "net.host.name", "url.full", "http.url", "peer.hostname", "server.socket.address"}},
	{"path", []string{"http.route", "url.path", "http.target", "url.full", "http.url"}},
}

// valueShape — "host" (nokta içeren ad, boşluksuz, / yok), "path" ("/" ile
// başlar), "other". Saf.
func valueShape(v string) string {
	v = strings.TrimSpace(v)
	switch {
	case strings.HasPrefix(v, "/"):
		return "path"
	case strings.Contains(v, ".") && !strings.ContainsAny(v, " /"):
		return "host"
	}
	return "other"
}

func dictionaryKeys(shape string) []string {
	for _, d := range valueKeyDictionary {
		if d.shape == shape {
			return d.keys
		}
	}
	return nil
}

// AttrKeyInfo — describe satırı.
type AttrKeyInfo struct {
	Key          string   `json:"key"`
	Scope        string   `json:"scope"` // span | resource
	Occurrences  uint64   `json:"occurrences"`
	SampleValues []string `json:"sample_values"`
	Column       string   `json:"column,omitempty"` // tipli/terfi kolon (kesin + hızlı süzgeç)
	Indexed      bool     `json:"indexed"`          // kolon ya da kvh bloom
}

func tagKeys(rows []chstore.ServiceAttrRow, kvh bool) []AttrKeyInfo {
	out := make([]AttrKeyInfo, 0, len(rows))
	for _, r := range rows {
		k := AttrKeyInfo{Key: r.Key, Scope: r.Scope, Occurrences: r.Occurrences, SampleValues: r.SampleValues}
		if col, _, ok := chstore.AttrKeyColumn(r.Key); ok {
			k.Column, k.Indexed = col, true
		} else {
			k.Indexed = kvh
		}
		out = append(out, k)
	}
	return out
}

// AttrValueMatch — find_attribute_by_value satırı.
type AttrValueMatch struct {
	Key         string `json:"key"`
	Scope       string `json:"scope"`
	Match       string `json:"match"` // exact | substring
	SampleHits  int    `json:"sample_hits"`
	Confirmed   bool   `json:"confirmed"`
	Count       uint64 `json:"count,omitempty"`
	Basis       string `json:"basis"` // sample | column | kvh
	Column      string `json:"column,omitempty"`
	FilterOp    string `json:"filter_op"` // = | LIKE
	FilterValue string `json:"filter_value"`
}

// MatchSamples — SAF: örneklem satırlarında değeri ara (tam / alt-dize).
func MatchSamples(rows []chstore.ServiceAttrRow, value string) []AttrValueMatch {
	lv := strings.ToLower(strings.TrimSpace(value))
	var out []AttrValueMatch
	for _, r := range rows {
		exact, sub := 0, 0
		for _, v := range r.SampleValues {
			x := strings.ToLower(v)
			switch {
			case x == lv:
				exact++
			case strings.Contains(x, lv):
				sub++
			}
		}
		if exact == 0 && sub == 0 {
			continue
		}
		m := AttrValueMatch{Key: r.Key, Scope: r.Scope, Basis: "sample", FilterValue: value}
		if exact > 0 {
			m.Match, m.SampleHits, m.FilterOp = "exact", exact, "="
		} else {
			m.Match, m.SampleHits, m.FilterOp, m.FilterValue = "substring", sub, "LIKE", value
		}
		if col, _, ok := chstore.AttrKeyColumn(r.Key); ok {
			m.Column = col
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Match != out[j].Match {
			return out[i].Match == "exact"
		}
		return out[i].SampleHits > out[j].SampleHits
	})
	return out
}

type attrScopeArgs struct {
	Service   string `json:"service,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Cluster   string `json:"cluster,omitempty"`
	RangeS    int    `json:"range_s,omitempty"`
}

func (a attrScopeArgs) scope(d Deps) (chstore.AttrScope, error) {
	sc := chstore.AttrScope{Service: strings.TrimSpace(a.Service), Namespace: strings.TrimSpace(a.Namespace)}
	if c := strings.TrimSpace(a.Cluster); c != "" {
		cs, ok := clustersFor(d, c)
		if !ok || len(cs) == 0 {
			return sc, unknownClusterErr(d, c)
		}
		sc.Clusters = cs[0].SpanValues
		if len(sc.Clusters) == 0 {
			sc.Clusters = []string{cs[0].Name}
		}
	}
	if sc.Service == "" && sc.Namespace == "" && len(sc.Clusters) == 0 {
		return sc, fmt.Errorf("kapsam zorunlu: service, namespace ya da cluster (filo geneli attribute taraması yok — önce resolve_entity)")
	}
	return sc, nil
}

func clampProbeRange(rangeS int) int {
	if rangeS <= 0 {
		return 1800
	}
	if rangeS > attrProbeMaxRangeS {
		return attrProbeMaxRangeS
	}
	return rangeS
}

var attrScopeProps = map[string]any{
	"service":   map[string]any{"type": "string", "description": "Exact service.name (from list_services / resolve_entity)."},
	"namespace": map[string]any{"type": "string", "description": "Exact Kubernetes namespace (k8s.namespace.name)."},
	"cluster":   map[string]any{"type": "string", "description": "Remote Cluster id / name / span value (from list_clusters)."},
	"range_s":   map[string]any{"type": "integer", "minimum": 60, "maximum": attrProbeMaxRangeS, "description": "Lookback in seconds. Default 1800, max 21600 (attribute scans are the slow path)."},
}

func withProps(extra map[string]any) map[string]any {
	m := map[string]any{}
	for k, v := range attrScopeProps {
		m[k] = v
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// ─── describe_attributes ───────────────────────────────────────

type describeAttrsArgs struct {
	attrScopeArgs
	Top     int `json:"top,omitempty"`
	Samples int `json:"samples,omitempty"`
}

func describeAttributesTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "describe_attributes",
		ShortDescription: "Kapsamdaki (servis/namespace/cluster) span+resource attribute anahtarları, örnek değerleri, hangisinin tipli/terfi kolonu ya da indeksi var. Süzgeçten ÖNCE zorunlu.",
		Description: "Discover which span and resource attribute keys a scope (service, namespace or cluster; at least one required) actually emits, with occurrence counts and a few sample " +
			"values per key, plus whether each key maps to a typed/promoted column or the attribute hash index (`indexed`). MANDATORY before filtering search_traces on any attribute " +
			"key: never invent a key — if it is not in this list, it does not exist in this scope's recent spans. Counts are SAMPLE-relative (5000 recent spans), for ranking only. " +
			"Prefer `column`/`indexed` keys for filters; a non-indexed key over a wide window is the known slow path.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": withProps(map[string]any{
				"top":     map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "description": "Keys per scope (span / resource). Default 50."},
				"samples": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "description": "Sample values per key. Default 10."},
			}),
		},
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a describeAttrsArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			sc, err := a.scope(d)
			if err != nil {
				return nil, err
			}
			from, to := rangeWindow(ctx, clampProbeRange(a.RangeS))
			rows, err := d.Store.GetScopedAttrs(ctx, sc, from, to, a.Top, a.Samples)
			if err != nil {
				return nil, err
			}
			keys := tagKeys(rows, chstore.AttrIndexAvailable())
			res := map[string]any{
				"scope": map[string]any{"service": sc.Service, "namespace": sc.Namespace, "clusters": sc.Clusters},
				"keys":  keys, "count": len(keys), "window_s": int(to.Sub(from).Seconds()),
				"sample_relative": true, "sample_spans": 5000, "attr_index": chstore.AttrIndexAvailable(),
			}
			if len(keys) == 0 {
				res["hint"] = "Bu kapsamda pencerede span yok ya da attribute taşımıyor — pencereyi büyüt (≤6 saat) ya da kapsamı doğrula (resolve_entity)."
			}
			return res, nil
		},
	}
}

// ─── find_attribute_by_value ───────────────────────────────────

type findAttrByValueArgs struct {
	attrScopeArgs
	Value string `json:"value"`
}

func findAttributeByValueTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "find_attribute_by_value",
		ShortDescription: "Bir değer (host, yol, kod) kapsamda HANGİ attribute anahtar(lar)ında geçiyor: örneklem + kolon/kvh probu; süzgeç için anahtar+op verir. Anahtar tahmin etme, bunu çağır.",
		Description: "Given a value the operator quoted (a host like apigateway.example.com, a path like /payment/3dsecure, a code), find which attribute keys carry it in the scope: " +
			"the scope's sampled keys are searched for exact and substring hits, keys typical for the value's shape (server.address / http.host / net.peer.name / url.full for hosts; " +
			"http.route / url.path / http.target for paths) are probed too, and every candidate with a typed/promoted column or the hash index gets an exact count (`confirmed`, `basis`). " +
			"Use the returned `filter_op` + `filter_value` per key in search_traces; when several keys match, search each key and merge — and say which keys you used. " +
			"Substring hits (a host inside url.full) are sample-only evidence: report them as such.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": withProps(map[string]any{"value": map[string]any{"type": "string", "description": "The value verbatim (case-insensitive)."}}),
			"required":   []string{"value"},
		},
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a findAttrByValueArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			value := strings.TrimSpace(a.Value)
			if len(value) < 2 {
				return nil, fmt.Errorf("value zorunlu (≥2 karakter)")
			}
			sc, err := a.scope(d)
			if err != nil {
				return nil, err
			}
			from, to := rangeWindow(ctx, clampProbeRange(a.RangeS))
			rows, err := d.Store.GetScopedAttrs(ctx, sc, from, to, 100, 20)
			if err != nil {
				return nil, err
			}
			matches := MatchSamples(rows, value)
			shape := valueShape(value)
			seen := map[string]bool{}
			for _, m := range matches {
				seen[m.Key] = true
			}
			for _, k := range dictionaryKeys(shape) {
				if !seen[k] {
					matches = append(matches, AttrValueMatch{Key: k, Scope: "span", Match: "dictionary", Basis: "sample", FilterOp: "=", FilterValue: value})
					seen[k] = true
				}
			}
			// Kesin prob: ilk N aday (kolon ya da kvh); yalnız tam eşleşme anlamlı.
			probed := 0
			for i := range matches {
				if probed >= attrProbeMaxKeys || matches[i].Match == "substring" {
					continue
				}
				count, basis, perr := d.Store.AttrValueProbe(ctx, sc, matches[i].Key, value, from, to)
				if perr != nil || basis == chstore.ProbeNone {
					continue
				}
				probed++
				matches[i].Basis, matches[i].Count, matches[i].Confirmed = basis, count, count > 0
				if col, _, ok := chstore.AttrKeyColumn(matches[i].Key); ok {
					matches[i].Column = col
				}
			}
			// Sözlük adayı hiçbir yerde görülmediyse (örneklem yok, prob 0/yok) düşür.
			kept := matches[:0]
			for _, m := range matches {
				if m.Match == "dictionary" && !m.Confirmed {
					continue
				}
				kept = append(kept, m)
			}
			res := map[string]any{
				"value": value, "shape": shape, "matches": kept, "count": len(kept),
				"window_s": int(to.Sub(from).Seconds()), "sample_spans": 5000, "attr_index": chstore.AttrIndexAvailable(),
			}
			if len(kept) == 0 {
				res["hint"] = "Değer bu kapsamın örnekleminde ve probe edilen anahtarlarda yok — kapsamı/pencereyi doğrula; değeri farklı bir yazımla (kısaltma, alt-dize) dene; anahtar UYDURMA."
			}
			return res, nil
		},
	}
}
