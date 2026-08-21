package mcptools

// list_exception_groups (v0.9.1091, Faz 4) — exception triyajının MCP
// köprüsü. /problems (Exceptions) sayfasının okumasıyla aynı
// (ListExceptionGroups): parmak-izine gruplu istisnalar, oluş sayısı,
// ilk/son görülme, durum. Model "bu serviste hangi hatalar patlıyor"
// sorusuna artık log araması kurgulamadan cevap bulur; fingerprint'i
// search_logs/get_trace zincirine taşıyabilir.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/mcp"
)

type listExceptionGroupsArgs struct {
	Service        string `json:"service,omitempty"`
	Search         string `json:"search,omitempty"`
	MinOccurrences int    `json:"min_occurrences,omitempty"`
	RangeS         int    `json:"range_s,omitempty"`
	Limit          int    `json:"limit,omitempty"`
}

func listExceptionGroupsTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "list_exception_groups",
		ShortDescription: "Pencerede aktif, parmak izine gruplu istisnalar (tip, mesaj, servis, tekrar sayısı, ilk/son görülme, triyaj). 'X servisinde hangi hatalar patlıyor'.",
		Description: "List fingerprint-grouped exceptions active in the window " +
			"(type, message, service, occurrences, first/last seen, triage state) — the same read the Exceptions page uses. " +
			"Use this to answer 'which errors are firing in service X' before constructing log searches; " +
			"sorted by occurrences descending. min_occurrences hides one-off noise (0 = show all — " +
			"a rare exception can still be the important one). Bounded state read, cheap.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{
					"type":        "string",
					"description": "Only groups of this service (exact name). Empty = all.",
				},
				"search": map[string]any{
					"type":        "string",
					"description": "Case-insensitive substring over exception type/message. Empty = no text filter.",
				},
				"min_occurrences": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"description": "Hide groups that fired fewer times than this. Default 0 (show all).",
				},
				"range_s": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"maximum":     604800,
					"description": "Activity lookback seconds (groups seen in this window). Default 1800.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     200,
					"description": "Max groups to return. Default 50.",
				},
			},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a listExceptionGroupsArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			from, to := rangeWindow(a.RangeS)
			limit := clampLimit(a.Limit, 50, 200)
			minOcc := a.MinOccurrences
			if minOcc < 0 {
				minOcc = 0
			}
			groups, err := d.Store.ListExceptionGroups(ctx, chstore.ExceptionGroupFilter{
				Service:        a.Service,
				Search:         a.Search,
				MinOccurrences: uint64(minOcc),
				ActiveFromNs:   from.UnixNano(),
				ActiveToNs:     to.UnixNano(),
				Sort:           "occurrences",
				Dir:            "desc",
				Limit:          limit,
			})
			if err != nil {
				return nil, err
			}
			out := map[string]any{
				"groups":   groups,
				"count":    len(groups),
				"window_s": int(to.Sub(from).Seconds()),
			}
			if len(groups) >= limit {
				out["has_more"] = true
			}
			return out, nil
		},
	}
}
