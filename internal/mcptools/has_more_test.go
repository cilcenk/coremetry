package mcptools

// v0.10.407 — CoSRE denetimi M3: limit'li araçlar sessiz kırpıyordu; model
// count == limit iken "tam liste" sanıyordu. Her limit'li zarf has_more taşır
// (search_traces.go doktrini). Kaynak-pin: yeni bir limit'li araç bu listeye
// girmeden gemiye çıkamaz.

import (
	"os"
	"strings"
	"testing"
)

func TestLimitedToolEnvelopesCarryHasMore(t *testing.T) {
	for file, needles := range map[string][]string{
		"tools.go":             {`"services": rows, "count": len(rows), "source": src, "has_more"`, `"problems": rows, "count": len(rows), "has_more": hasMore`, `"anomalies": rows, "count": len(rows), "has_more"`},
		"list_slo_status.go":   {`"total": total, "has_more": total > len(out)`},
		"list_metric_names.go": {`"total": total, "has_more": total > len(names)`},
		"pivots.go":            {`"has_more": len(page.Logs) >= limit`, `"items": items, "count": len(items), "has_more"`},
		"team_ownership.go":    {`"has_more":         hasMore,`},
		"guided_parity.go":     {`"has_more":       data.HeapTruncated`},
		"analysis.go":          {`"has_more":`},
	} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range needles {
			if !strings.Contains(string(b), n) {
				t.Errorf("%s: has_more zarfı kayıp: %s", file, n)
			}
		}
	}
}
