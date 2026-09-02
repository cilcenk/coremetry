package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// logs_query_syntax.go — v0.10.280 (log-search audit Dilim 1b).
//
// v0.10.279 arama metnini ClickHouse'ta logql AST'sine derliyor ve
// ayrışmayan metinde eski alt-dize yoluna düşüyor. Düşüş derin katmanda
// sessizdir (yalnız log satırı); operatörün gördüğü tek şey "beklediğim
// sonuç değil" olurdu. Bu dosya hatayı yüzeye çıkarır: 400 + konumlu
// mesaj, /api/logs ve /api/logs/timeseries ikisinde de.
//
// YALNIZ clickhouse: ES'te gramer sahibi query_string'dir ve bizim alt
// kümemiz ES'in kabul ettiği yazımları (fuzzy `~`, boost `^`, regex `//`,
// alan grubu içinde alan) reddederdi — ES kurulumunda geçerli bir sorguyu
// 400'lemek regresyon olurdu. ES sözdizimi hatası bugün olduğu gibi ES'in
// kendi hatasıyla döner.

// logQuerySyntaxReject — kararın saf hâli (tablo-testli).
func logQuerySyntaxReject(backend, search string) (string, bool) {
	if backend != "clickhouse" || strings.TrimSpace(search) == "" {
		return "", false
	}
	if err := chstore.LogQuerySyntaxError(search); err != nil {
		return err.Error(), true
	}
	return "", false
}

func (s *Server) rejectLogQuerySyntax(w http.ResponseWriter, search string) bool {
	if s.logs == nil {
		return false
	}
	msg, bad := logQuerySyntaxReject(s.logs.Backend(), search)
	if !bad {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
	return true
}
