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
	"fmt"
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
	// v0.10.119 — SAYILAR (operatör: "Çok yavaş" — süre/ETA görünmüyordu).
	Parallel     int    `json:"parallel"`
	SliceSize    string `json:"sliceSize,omitempty"`
	SliceDone    int    `json:"sliceDone"`
	SliceTotal   int    `json:"sliceTotal"`
	LastSliceMs  int64  `json:"lastSliceMs,omitempty"`
	AvgSliceMs   int64  `json:"avgSliceMs,omitempty"`
	DayEtaMs     int64  `json:"dayEtaMs,omitempty"`
	RunEtaMs     int64  `json:"runEtaMs,omitempty"`
	DayStartedAt int64  `json:"dayStartedAt,omitempty"`
	// Live (v0.10.120) — system.processes'taki koşan backfill sorguları;
	// durum çağrısında doldurulur, koşu kaydına yazılmaz.
	Live      []chstore.TraceBackfillProc `json:"live,omitempty"`
	LiveError string                      `json:"liveError,omitempty"`
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
	// v0.10.120 — canlı dilim (system.processes); okunamazsa durum yine
	// döner, hata ayrı alanda. Koşu yokken de sorulur: eski kodla başlamış
	// bir backfill'in sorgusu görünür olsun.
	if s.store != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		live, err := s.store.TraceBackfillLive(ctx)
		cancel()
		if err != nil {
			run.LiveError = err.Error()
		} else {
			run.Live = live
		}
	}
	writeJSON(w, run)
}

func (s *Server) postTraceBackfillApply(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Days    []string `json:"days"`
		Confirm string   `json:"confirm"`
		// Parallel (v0.10.119) — eşzamanlı dilim; 0/1 = ardışık (eski).
		Parallel int `json:"parallel"`
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
	// v0.10.119 — bugün/gelecek gün reddedilir (canlı MV ile çift sayım).
	for _, d := range in.Days {
		if err := chstore.BackfillDayAllowed(d, time.Now()); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	parallel := in.Parallel
	if parallel < 1 {
		parallel = 1
	}
	if parallel > 4 {
		parallel = 4
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
		StartedAt: time.Now().UnixMilli(), Days: in.Days, Parallel: parallel,
	}
	traceBackfill.mu.Unlock()

	details, _ := json.Marshal(map[string]any{"days": in.Days, "parallel": parallel})
	s.audit(r, "admin.trace_backfill.apply", "clickhouse", "trace_summary_5m", string(details))

	// v0.10.108 — gün hacimleri preflight'tan: hacme-göre başlangıç
	// basamağı mahkûm denemeleri atlar ("yavaş gidiyor"). Preflight
	// düşerse harita boş kalır ve günler tam merdivenle koşar.
	rowsByDay := map[string]uint64{}
	if pf, err := s.store.TraceBackfillPreflight(r.Context(), 30); err == nil {
		for _, d := range pf {
			rowsByDay[d.Day] = d.SpanTraces
		}
	}
	go s.runTraceBackfill(in.Days, rowsByDay, parallel)
	writeJSON(w, map[string]any{"ok": true, "days": len(in.Days), "parallel": parallel})
}

// backfillEta — kalan süre tahmini (ms). Saf; tablo-testli.
// dayEta = kalan dilim × ortalama / paralellik; runEta = dayEta + kalan
// gün × (bu günün projeksiyonu). Ortalama yoksa 0 (bilinmiyor).
func backfillEta(sliceDone, sliceTotal int, avgMs int64, parallel, daysLeftAfterThis int) (dayEta, runEta int64) {
	if avgMs <= 0 || sliceTotal <= 0 {
		return 0, 0
	}
	if parallel < 1 {
		parallel = 1
	}
	perSlice := avgMs / int64(parallel)
	dayEta = int64(sliceTotal-sliceDone) * perSlice
	if dayEta < 0 {
		dayEta = 0
	}
	runEta = dayEta + int64(daysLeftAfterThis)*int64(sliceTotal)*perSlice
	return dayEta, runEta
}

// runTraceBackfill — kopuk goroutine: istek ctx'inden BİLİNÇLİ bağımsız
// (WithoutCancel sınıfı — tarayıcı kapansa da gün yarım kalmasın; her
// günün kendi 600s tavanı var). Panik guard'ı şart: api'de recover
// middleware yok, kopuk goroutine'de panik süreci öldürür.
func (s *Server) runTraceBackfill(days []string, rowsByDay map[string]uint64, parallel int) {
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
	for di, day := range days {
		traceBackfill.mu.Lock()
		traceBackfill.run.Current = day
		traceBackfill.run.DayStartedAt = time.Now().UnixMilli()
		traceBackfill.run.SliceDone, traceBackfill.run.SliceTotal = 0, 0
		traceBackfill.run.LastSliceMs, traceBackfill.run.AvgSliceMs = 0, 0
		traceBackfill.run.DayEtaMs, traceBackfill.run.RunEtaMs = 0, 0
		traceBackfill.mu.Unlock()

		// v0.10.119 — ilerleme SAYI: dilim süresi, ortalama, ETA. "Yavaş"
		// hissinin öbür yarısı ölçüsüzlüktü; log satırı da prod'da
		// (query_log kapalı) dilim maliyetinin tek kaynağı.
		var sumMs int64
		var cnt int64
		daysLeft := len(days) - di - 1
		progress := func(p chstore.BackfillProgress) {
			traceBackfill.mu.Lock()
			defer traceBackfill.mu.Unlock()
			r := &traceBackfill.run
			r.SliceSize, r.SliceTotal = p.Slice.String(), p.Total
			if p.Finished {
				sumMs += p.LastMs
				cnt++
				r.SliceDone, r.LastSliceMs, r.AvgSliceMs = p.Done, p.LastMs, sumMs/cnt
				r.DayEtaMs, r.RunEtaMs = backfillEta(p.Done, p.Total, r.AvgSliceMs, parallel, daysLeft)
				log.Printf("[trace-backfill] %s dilim %d/%d (%s): %d ms · ort %d ms · gün ≈ %s kaldı · koşu ≈ %s",
					p.Day, p.Done, p.Total, p.Slice, p.LastMs, r.AvgSliceMs,
					(time.Duration(r.DayEtaMs) * time.Millisecond).Round(time.Second),
					(time.Duration(r.RunEtaMs) * time.Millisecond).Round(time.Second))
			}
			r.Current = fmt.Sprintf("%s · %s dilim %d/%d", p.Day, p.Slice, p.Index, p.Total)
		}
		// v0.10.108 gün tavanı 60 dk idi; v0.10.119 — 3 saat: 5 dk
		// merdiveni 288 dilim × 25 s tavan = 2 saat, 60 dk'da matematiksel
		// olarak bitemiyordu (gün bütçesi ladder'ı düşürüp yine bütçeye
		// çarpıyordu).
		dayCtx, cancel := context.WithTimeout(ctx, 3*time.Hour)
		err := s.store.TraceBackfillDayRun(dayCtx, day, rowsByDay[day], parallel, progress)
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
