package api

// admin_trace_backfill.go — /traces tarihçe geri doldurma sihirbazı
// (v0.10.103, operatör isteği: "Sihirbaz ile yapalım" — elle SQL yerine
// üründe adım adım).
//
// Emsal: admin_state_repartition.go (0010). Aynı duruş: ince HTTP +
// kalın store (chstore/trace_backfill.go), tek-uçuş kapısı, arka planda
// koşan apply + yoklanan durum, serveCached YOK (kontrol yüzeyi),
// BOOT'TA ASLA KOŞMAZ — yalnız admin tıklamasıyla.
//
// Yıkıcılık dürüstçe: her seçili gün için MV'nin O GÜNKÜ partition'ı
// düşer ve ham spans'ten yeniden kurulur (idempotens gerekçesi store
// dosyasının başında — AggregatingMergeTree'de çifte-insert sayı
// şişirir). Span'lere DOKUNULMAZ. Yine de gövde açık onay ister.

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

type traceBackfillRun struct {
	Running   bool     `json:"running"`
	StartedBy string   `json:"startedBy"`
	StartedAt int64    `json:"startedAt"`
	DoneAt    int64    `json:"doneAt,omitempty"`
	Days      []string `json:"days"`
	Done      int      `json:"done"`
	Current   string   `json:"current,omitempty"`
	Errors    []string `json:"errors,omitempty"`
}

type traceBackfillFlight struct {
	mu  sync.Mutex
	run traceBackfillRun
}

var traceBackfill traceBackfillFlight

func (s *Server) registerTraceBackfillRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/admin/clickhouse/trace-backfill/preflight",
		auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.getTraceBackfillPreflight)))
	mux.Handle("GET /api/admin/clickhouse/trace-backfill/status",
		auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.getTraceBackfillStatus)))
	mux.Handle("POST /api/admin/clickhouse/trace-backfill/apply",
		auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.postTraceBackfillApply)))
}

func (s *Server) getTraceBackfillPreflight(w http.ResponseWriter, r *http.Request) {
	days, err := s.store.TraceBackfillPreflight(r.Context(), 30)
	if err != nil {
		writeErr(w, err)
		return
	}
	if days == nil {
		days = []chstore.TraceBackfillDay{}
	}
	writeJSON(w, map[string]any{"days": days})
}

func (s *Server) getTraceBackfillStatus(w http.ResponseWriter, r *http.Request) {
	traceBackfill.mu.Lock()
	run := traceBackfill.run
	traceBackfill.mu.Unlock()
	writeJSON(w, run)
}

func (s *Server) postTraceBackfillApply(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Days    []string `json:"days"`
		Confirm string   `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return
	}
	// Açık onay: düğme hükmü veremez — operatör ne yaptığını yazar.
	if in.Confirm != "BACKFILL" {
		writeJSONError(w, http.StatusBadRequest,
			`onay gerekli: {"confirm":"BACKFILL"} — seçili günlerin MV partition'ları düşürülüp ham spans'ten yeniden kurulacak`)
		return
	}
	if len(in.Days) == 0 || len(in.Days) > 31 {
		writeJSONError(w, http.StatusBadRequest, "1-31 gün seçilmeli")
		return
	}
	user := ""
	if c := auth.FromContext(r.Context()); c != nil {
		user = c.Email
	}
	traceBackfill.mu.Lock()
	if traceBackfill.run.Running {
		traceBackfill.mu.Unlock()
		writeJSONError(w, http.StatusConflict, "bir geri doldurma zaten koşuyor")
		return
	}
	traceBackfill.run = traceBackfillRun{
		Running: true, StartedBy: user,
		StartedAt: time.Now().UnixMilli(), Days: in.Days,
	}
	traceBackfill.mu.Unlock()

	details, _ := json.Marshal(map[string]any{"days": in.Days})
	s.audit(r, "admin.trace_backfill.apply", "clickhouse", "trace_summary_5m", string(details))

	go s.runTraceBackfill(in.Days)
	writeJSON(w, map[string]any{"ok": true, "days": len(in.Days)})
}

// runTraceBackfill — kopuk goroutine: istek ctx'inden BİLİNÇLİ bağımsız
// (WithoutCancel sınıfı — tarayıcı kapansa da gün yarım kalmasın; her
// günün kendi 600s tavanı var). Panik guard'ı şart: api'de recover
// middleware yok, kopuk goroutine'de panik süreci öldürür.
func (s *Server) runTraceBackfill(days []string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[trace-backfill] panik: %v", r)
			traceBackfill.mu.Lock()
			traceBackfill.run.Running = false
			traceBackfill.run.Errors = append(traceBackfill.run.Errors, "panik — log'a bakın")
			traceBackfill.mu.Unlock()
		}
	}()
	ctx := context.Background()
	for _, day := range days {
		traceBackfill.mu.Lock()
		traceBackfill.run.Current = day
		traceBackfill.mu.Unlock()

		dayCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		err := s.store.TraceBackfillDayRun(dayCtx, day)
		cancel()

		traceBackfill.mu.Lock()
		traceBackfill.run.Done++
		if err != nil {
			// Hata GÜNÜ atlatmaz, koşuyu DURDURUR: yarım bir gün yok
			// (partition ya düştü-yeniden kuruldu ya hiç dokunulmadı),
			// ama sıradaki günlere hatalı zeminde devam etmek teşhisi
			// bulandırır. Operatör hatayı görüp yeniden başlatır —
			// idempotens tekrarı güvenli kılıyor.
			traceBackfill.run.Errors = append(traceBackfill.run.Errors, day+": "+err.Error())
			traceBackfill.run.Running = false
			traceBackfill.run.DoneAt = time.Now().UnixMilli()
			traceBackfill.mu.Unlock()
			log.Printf("[trace-backfill] gün %s: %v — koşu durdu", day, err)
			return
		}
		traceBackfill.mu.Unlock()
		log.Printf("[trace-backfill] gün %s tamam", day)
	}
	traceBackfill.mu.Lock()
	traceBackfill.run.Running = false
	traceBackfill.run.Current = ""
	traceBackfill.run.DoneAt = time.Now().UnixMilli()
	traceBackfill.mu.Unlock()
}
