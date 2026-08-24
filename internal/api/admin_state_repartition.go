package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

// 0010 partition sökme sihirbazı — /system/clickhouse altındaki operatör
// yüzeyi. migrations/0010_state_repartition.sql'in yordamını koşar.
//
// Emsal: admin_state_unify.go (0009). Aynı şekil: ince HTTP + kalın
// store, arka planda koşan apply, ayrı bir uçtan yoklanan ilerleme.
// serveCached YOK — bu bir kontrol yüzeyi; 30 saniyelik bir önbellek
// "Uygula"dan hemen sonra "Durum"un yalan söylemesi demektir.
//
// v0.9.1335'in yazarı bu sihirbazı BİLİNÇLİ atlamıştı (2 tablo × 4
// ifade vs 0009'un 37 × 3). O gerekçe GEREKLİLİK üzerineydi; operatörün
// tercihi ayrı ve yeterli bir sebep: SQL konsolu bu işi zaten yapamaz
// (sql_playground.go yalnız SELECT/SHOW/DESCRIBE/EXPLAIN/WITH kabul
// eder ve readonly=2 ile koşar — o kapı GEVŞETİLMEZ).
//
// ÜÇ AYRI EYLEM, ÜÇ AYRI KAPI:
//   apply    → AŞAMA A. Hiçbir şey silmez, `_old` yedeği bırakır.
//   finalize → ADIM 5 (DROP `_old`) + AŞAMA B. YIKICI. Tetiği bir HÜKÜM
//              ("7 gün geçti mi, doğrulama hâlâ yeşil mi") — düğme bunu
//              veremez, o yüzden gövde açık onay taşır.
//   cleanup  → AŞAMA B'nin `_pathfix_old` yedeklerini düşürür.
//
// BOOT'TA ASLA KOŞMAZ (v0.9.613) — yalnız admin tıklamasıyla.

// stateRepartRun — koşan ya da bitmiş göçün anlık hâli.
type stateRepartRun struct {
	Running   bool                             `json:"running"`
	Phase     string                           `json:"phase"`
	Cluster   string                           `json:"cluster"`
	StartedBy string                           `json:"startedBy"`
	StartedAt int64                            `json:"startedAt"`
	DoneAt    int64                            `json:"doneAt"`
	Total     int                              `json:"total"`
	Done      int                              `json:"done"`
	Current   string                           `json:"current"`
	Results   []chstore.StateRepartTableResult `json:"results"`
	Error     string                           `json:"error,omitempty"`
}

// stateRepartFlight — tek-uçuş kapısı. İki operatör aynı anda göç
// başlatırsa ikinci istek 409 alır; ON CLUSTER RENAME'lerin yarışması
// dağıtık DDL kuyruğunu tıkardı.
type stateRepartFlight struct {
	mu  sync.Mutex
	run stateRepartRun
}

func (f *stateRepartFlight) begin(phase, cluster, by string, total int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.run.Running {
		return false
	}
	f.run = stateRepartRun{
		Running:   true,
		Phase:     phase,
		Cluster:   cluster,
		StartedBy: by,
		StartedAt: time.Now().UnixMilli(),
		Total:     total,
		Results:   []chstore.StateRepartTableResult{},
	}
	return true
}

func (f *stateRepartFlight) startTable(name string) {
	f.mu.Lock()
	f.run.Current = name
	f.mu.Unlock()
}

func (f *stateRepartFlight) recordTable(r chstore.StateRepartTableResult) {
	f.mu.Lock()
	f.run.Results = append(f.run.Results, r)
	f.run.Done++
	f.run.Current = ""
	f.mu.Unlock()
}

func (f *stateRepartFlight) finish(errMsg string) {
	f.mu.Lock()
	f.run.Running = false
	f.run.DoneAt = time.Now().UnixMilli()
	f.run.Current = ""
	if errMsg != "" {
		f.run.Error = errMsg
	}
	f.mu.Unlock()
}

func (f *stateRepartFlight) snapshot() stateRepartRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.run
	// append([]T(nil), boş...) NIL döner — ilk sayfa açılışında `results`
	// JSON'da `null` gidip FE'de `.length` patlatıyordu (v0.9.1315).
	out.Results = make([]chstore.StateRepartTableResult, 0, len(f.run.Results))
	out.Results = append(out.Results, f.run.Results...)
	return out
}

func (s *Server) registerStateRepartRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/admin/state-repart/preflight",
		auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.getStateRepartPreflight)))
	mux.Handle("GET /api/admin/state-repart/status",
		auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.getStateRepartStatus)))
	mux.Handle("POST /api/admin/state-repart/apply",
		auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.postStateRepartApply)))
	mux.Handle("POST /api/admin/state-repart/finalize",
		auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.postStateRepartFinalize)))
	mux.Handle("POST /api/admin/state-repart/cleanup",
		auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.postStateRepartCleanup)))
}

func (s *Server) getStateRepartPreflight(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	res, err := s.store.StateRepartPreflight(ctx)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Nil dilim JSON'da `null` olur ve FE `.map()` üstünde patlar.
	res.Normalize()
	writeJSON(w, res)
}

func (s *Server) getStateRepartStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.stateRepart.snapshot())
}

type stateRepartBody struct {
	Cluster string   `json:"cluster"`
	Tables  []string `json:"tables"`
	// Acknowledged — YIKICI adımların açık onayı. Sunucu tarafında
	// duruyor ki bir "curl -XPOST" ya da yanlış bağlanmış bir düğme
	// `_old` yedeklerini düşüremesin.
	Acknowledged bool `json:"acknowledged"`
}

// freshPreflight — HER eylem ön kontrolü YENİDEN koşar. Tarayıcıdaki
// fotoğraf bayat olabilir; kapılar TAZE ölçüme dayanmalı, kullanıcının
// gönderdiğine değil.
func (s *Server) freshPreflight(w http.ResponseWriter, r *http.Request, b *stateRepartBody) (chstore.StateRepartPreflightResult, bool) {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(b); err != nil {
		writeJSONError(w, http.StatusBadRequest, "gövde ayrıştırılamadı: "+err.Error())
		return chstore.StateRepartPreflightResult{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	pre, err := s.store.StateRepartPreflight(ctx)
	cancel()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return pre, false
	}
	if b.Cluster != "" && b.Cluster != pre.Cluster {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf(
			"küme uyuşmuyor: istek '%s', kurulum '%s'", b.Cluster, pre.Cluster))
		return pre, false
	}
	return pre, true
}

// selectTables — istenen tabloları TAZE ön kontrolden süzer. `want` boşsa
// aşamaya uygun tümü seçilir.
func selectTables(pre chstore.StateRepartPreflightResult, want []string, stage string) []chstore.StateRepartTable {
	set := map[string]bool{}
	for _, t := range want {
		set[t] = true
	}
	var plan []chstore.StateRepartTable
	for _, t := range pre.Tables {
		if t.Stage != stage || t.Blocked != "" {
			continue
		}
		if len(set) > 0 && !set[t.Name] {
			continue
		}
		plan = append(plan, t)
	}
	return plan
}

func (s *Server) postStateRepartApply(w http.ResponseWriter, r *http.Request) {
	var b stateRepartBody
	pre, ok := s.freshPreflight(w, r, &b)
	if !ok {
		return
	}
	if !pre.Supported {
		writeJSONError(w, http.StatusPreconditionFailed, "ön kontrol geçmedi: "+pre.Detail)
		return
	}
	plan := selectTables(pre, b.Tables, "A")
	if len(plan) == 0 {
		writeJSONError(w, http.StatusPreconditionFailed, "AŞAMA A gerektiren tablo yok")
		return
	}

	by := ""
	if claims := auth.FromContext(r.Context()); claims != nil {
		by = claims.Email
	}
	if !s.stateRepart.begin("A", pre.Cluster, by, len(plan)) {
		w.WriteHeader(http.StatusConflict)
		writeJSON(w, s.stateRepart.snapshot())
		return
	}
	s.audit(r, "state_repart.apply", "clickhouse", pre.Cluster,
		fmt.Sprintf(`{"cluster":%q,"phase":"A","tables":%d}`, pre.Cluster, len(plan)))

	s.runStateRepart(plan, pre.Cluster, "A")
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, s.stateRepart.snapshot())
}

func (s *Server) postStateRepartFinalize(w http.ResponseWriter, r *http.Request) {
	var b stateRepartBody
	pre, ok := s.freshPreflight(w, r, &b)
	if !ok {
		return
	}
	if !b.Acknowledged {
		writeJSONError(w, http.StatusPreconditionFailed,
			"bu adım `_old` yedeklerini KALICI olarak düşürür — açık onay şart")
		return
	}
	if !pre.FinalizeReady {
		writeJSONError(w, http.StatusPreconditionFailed, "ön kontrol geçmedi: "+pre.Detail)
		return
	}
	plan := selectTables(pre, b.Tables, "B")
	if len(plan) == 0 {
		writeJSONError(w, http.StatusPreconditionFailed, "AŞAMA B gerektiren tablo yok")
		return
	}

	by := ""
	if claims := auth.FromContext(r.Context()); claims != nil {
		by = claims.Email
	}
	if !s.stateRepart.begin("B", pre.Cluster, by, len(plan)) {
		w.WriteHeader(http.StatusConflict)
		writeJSON(w, s.stateRepart.snapshot())
		return
	}
	s.audit(r, "state_repart.finalize", "clickhouse", pre.Cluster,
		fmt.Sprintf(`{"cluster":%q,"phase":"B","tables":%d,"dropsBackups":true}`, pre.Cluster, len(plan)))

	s.runStateRepart(plan, pre.Cluster, "B")
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, s.stateRepart.snapshot())
}

// runStateRepart — arka plan koşucusu. İstek bağlamı DEĞİL: tarayıcı
// kapansa da ON CLUSTER RENAME zinciri yarıda kesilmemeli.
func (s *Server) runStateRepart(plan []chstore.StateRepartTable, cluster, phase string) {
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
		defer cancel()
		failed := ""
		for _, t := range plan {
			s.stateRepart.startTable(t.Name)
			var res chstore.StateRepartTableResult
			if phase == "A" {
				res = s.store.StateRepartMigrateTable(bg, cluster, t)
			} else {
				res = s.store.StateRepartFinalizeTable(bg, cluster, t)
			}
			s.stateRepart.recordTable(res)
			if !res.OK {
				// İLK HATADA DUR. Yarım bir göç, yanlış bir göçten kolay
				// geri alınır.
				failed = t.Name + ": " + res.Err
				break
			}
		}
		s.stateRepart.finish(failed)
	}()
}

func (s *Server) postStateRepartCleanup(w http.ResponseWriter, r *http.Request) {
	var b stateRepartBody
	pre, ok := s.freshPreflight(w, r, &b)
	if !ok {
		return
	}
	if !b.Acknowledged {
		writeJSONError(w, http.StatusPreconditionFailed, "yedek silme geri alınamaz — açık onay şart")
		return
	}
	if len(b.Tables) == 0 {
		writeJSONError(w, http.StatusBadRequest, "silinecek yedek seçilmedi")
		return
	}
	if !pre.CleanupReady {
		writeJSONError(w, http.StatusPreconditionFailed, "ön kontrol geçmedi: "+pre.Detail)
		return
	}

	bg, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Minute)
	defer cancel()
	steps := s.store.StateRepartDropBackups(bg, pre.Cluster, b.Tables)

	okN := 0
	for _, st := range steps {
		if st.OK {
			okN++
		}
	}
	s.audit(r, "state_repart.cleanup", "clickhouse", pre.Cluster,
		fmt.Sprintf(`{"cluster":%q,"ok":%d,"total":%d}`, pre.Cluster, okN, len(steps)))

	writeJSON(w, map[string]any{"steps": steps, "ok": okN == len(steps)})
}
