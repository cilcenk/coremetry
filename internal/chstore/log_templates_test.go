package chstore

// v0.10.310 — /logs Şablonlar sekmesi: ListLogTemplates WHERE/ORDER kurucusu.
// Servis filtresi has(services, ?) ile; bilinmeyen sort eski davranışı korur.

import (
	"strings"
	"testing"
	"time"
)

func TestLogTemplatesWhere(t *testing.T) {
	for _, tc := range []struct {
		n     string
		f     ListLogTemplatesFilter
		want  []string
		no    []string
		nargs int
	}{
		{"varsayılan", ListLogTemplatesFilter{}, []string{"WHERE 1", "ORDER BY total_count DESC"}, []string{"last_seen >= ?", "has(services"}, 0},
		{"since", ListLogTemplatesFilter{SinceNs: time.Now().UnixNano(), SortBy: "last_seen"}, []string{"last_seen >= ?", "ORDER BY last_seen DESC"}, []string{"has(services"}, 1},
		{"servis", ListLogTemplatesFilter{Service: "api", SortBy: "first_seen"}, []string{"has(services, ?)", "ORDER BY first_seen DESC"}, []string{"last_seen >= ?"}, 1},
		{"ikisi", ListLogTemplatesFilter{SinceNs: 1, Service: "api", SortBy: "count"}, []string{"last_seen >= ?", "has(services, ?)", "ORDER BY total_count DESC"}, nil, 2},
		{"bilinmeyen sort", ListLogTemplatesFilter{SortBy: "spike"}, []string{"ORDER BY total_count DESC"}, nil, 0},
	} {
		got, args := logTemplatesWhere(tc.f)
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q yok:\n%s", tc.n, w, got)
			}
		}
		for _, w := range tc.no {
			if strings.Contains(got, w) {
				t.Errorf("%s: %q olmamalı:\n%s", tc.n, w, got)
			}
		}
		if len(args) != tc.nargs || strings.Count(got, "?") != tc.nargs {
			t.Errorf("%s: args %d, ? %d; want %d", tc.n, len(args), strings.Count(got, "?"), tc.nargs)
		}
	}
	if _, args := logTemplatesWhere(ListLogTemplatesFilter{Service: "api"}); args[0] != "api" {
		t.Errorf("servis bind'i = %v", args[0])
	}
}
