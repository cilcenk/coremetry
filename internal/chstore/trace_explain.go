package chstore

// trace_explain.go — v0.10.326: /api/traces?explain=1 (admin) teşhis kaydı.
// Operatör 2026-09-03: aynı servis+operasyon araması prod'da 15m/30m boş,
// 6h dolu; lokalde tekrar etmiyor, prod query_log'a erişim yok. Bu sınıf
// bir daha tahminle çözülmesin: liste isteği hangi yolu seçti (mv /
// error-first / probe / light / raw-list), hangi SQL hangi arg'larla
// koştu, kaç ms sürdü, kaç satır döndü, hata neydi — yanıtın içinde.
// nil-güvenli: Explain verilmediğinde sıfır maliyet (nil alıcı, erken dönüş).

import (
	"fmt"
	"strings"
	"time"
)

type TraceExplainStep struct {
	Name string   `json:"name"`
	SQL  string   `json:"sql,omitempty"`
	Args []string `json:"args,omitempty"`
	Ms   float64  `json:"ms"`
	Rows int      `json:"rows"`
	Err  string   `json:"err,omitempty"`
}

type TraceExplain struct {
	Notes []string           `json:"notes"`
	Steps []TraceExplainStep `json:"steps"`
}

// note — yol kararı / bağlam satırı ("path=raw-list", "window=…").
func (x *TraceExplain) note(format string, a ...any) {
	if x == nil {
		return
	}
	x.Notes = append(x.Notes, fmt.Sprintf(format, a...))
}

// step — bir CH sorgusunun kaydı; SQL boşlukları tek boşluğa iner, arg'lar
// metne çevrilir (time.Time UTC RFC3339Nano — pencere sınırı okunsun).
func (x *TraceExplain) step(name, sql string, args []any, start time.Time, rows int, err error) {
	if x == nil {
		return
	}
	st := TraceExplainStep{Name: name, SQL: strings.Join(strings.Fields(sql), " "), Ms: float64(time.Since(start).Microseconds()) / 1000, Rows: rows}
	for _, a := range args {
		switch v := a.(type) {
		case time.Time:
			st.Args = append(st.Args, v.UTC().Format(time.RFC3339Nano))
		default:
			s := fmt.Sprint(v)
			if len(s) > 120 {
				s = s[:120] + "…"
			}
			st.Args = append(st.Args, s)
		}
	}
	if err != nil {
		st.Err = err.Error()
	}
	x.Steps = append(x.Steps, st)
}
