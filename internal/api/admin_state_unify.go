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

// 0009 state birleştirme sihirbazı — /system/clickhouse altındaki
// operatör yüzeyi.
//
// Emsal: admin_rollup.go (ince HTTP + kalın store) ve spool_actions.go
// (mutex korumalı tek-uçuş ilerleme kaydı). Rollup sihirbazı tek uzun
// senkron istekle çalışıyor; burada O YETMEZ — prod'da 37 tablo ve
// `problems` 675k satır, yani göç tarayıcı zaman aşımını kolayca aşar.
// Bu yüzden apply işi arka planda koşar ve ilerleme ayrı bir uçtan
// yoklanır.
//
// serveCached YOK: bu bir kontrol yüzeyi. 30 saniyelik bir önbellek,
// "Uygula"dan hemen sonra "Durum"un yalan söylemesi demektir.
//
// BOOT'TA ASLA KOŞMAZ (v0.9.613) — yalnız admin tıklamasıyla.

// stateUnifyRun — koşan ya da bitmiş göçün anlık hâli.
type stateUnifyRun struct {
	Running   bool                            `json:"running"`
	Cluster   string                          `json:"cluster"`
	StartedBy string                          `json:"startedBy"`
	StartedAt int64                           `json:"startedAt"`
	DoneAt    int64                           `json:"doneAt"`
	Total     int                             `json:"total"`
	Done      int                             `json:"done"`
	Current   string                          `json:"current"`
	Results   []chstore.StateUnifyTableResult `json:"results"`
	Error     string                          `json:"error,omitempty"`
}

// stateUnifyFlight — tek-uçuş kapısı. İki operatör aynı anda göç
// başlatırsa ikinci istek 409 alır; ON CLUSTER RENAME'lerin yarışması
// dağıtık DDL kuyruğunu tıkardı.
type stateUnifyFlight struct {
	mu  sync.Mutex
	run stateUnifyRun
}

func (f *stateUnifyFlight) begin(cluster, by string, total int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.run.Running {
		return false
	}
	f.run = stateUnifyRun{
		Running:   true,
		Cluster:   cluster,
		StartedBy: by,
		StartedAt: time.Now().UnixMilli(),
		Total:     total,
		Results:   []chstore.StateUnifyTableResult{},
	}
	return true
}

func (f *stateUnifyFlight) startTable(name string) {
	f.mu.Lock()
	f.run.Current = name
	f.mu.Unlock()
}

func (f *stateUnifyFlight) recordTable(r chstore.StateUnifyTableResult) {
	f.mu.Lock()
	f.run.Results = append(f.run.Results, r)
	f.run.Done++
	f.run.Current = ""
	f.mu.Unlock()
}

func (f *stateUnifyFlight) finish(errMsg string) {
	f.mu.Lock()
	f.run.Running = false
	f.run.DoneAt = time.Now().UnixMilli()
	f.run.Current = ""
	if errMsg != "" {
		f.run.Error = errMsg
	}
	f.mu.Unlock()
}

func (f *stateUnifyFlight) snapshot() stateUnifyRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.run
	// append([]T(nil), boş...) NIL döner — ilk sayfa açılışında `results`
	// JSON'da `null` gidip FE'de `.length` patlatıyordu (v0.9.1315).
	out.Results = make([]chstore.StateUnifyTableResult, 0, len(f.run.Results))
	out.Results = append(out.Results, f.run.Results...)
	return out
}

func (s *Server) registerStateUnifyRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/admin/state-unify/preflight",
		auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.getStateUnifyPreflight)))
	mux.Handle("GET /api/admin/state-unify/status",
		auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.getStateUnifyStatus)))
	mux.Handle("POST /api/admin/state-unify/apply",
		auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.postStateUnifyApply)))
	mux.Handle("POST /api/admin/state-unify/cleanup",
		auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.postStateUnifyCleanup)))
}

func (s *Server) getStateUnifyPreflight(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := s.store.StateUnifyPreflight(ctx)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Nil dilim JSON'da `null` olur ve FE `.length`/`.map()` üstünde patlar.
	res.Normalize()
	writeJSON(w, res)
}

func (s *Server) getStateUnifyStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.stateUnify.snapshot())
}

type stateUnifyApplyBody struct {
	Cluster string   `json:"cluster"`
	Tables  []string `json:"tables"`
}

func (s *Server) postStateUnifyApply(w http.ResponseWriter, r *http.Request) {
	var b stateUnifyApplyBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeJSONError(w, http.StatusBadRequest, "gövde ayrıştırılamadı: "+err.Error())
		return
	}

	// Ön kontrol HER ZAMAN yeniden koşar. Tarayıcıdaki fotoğraf bayat
	// olabilir; çift-sayım kapısı TAZE ölçüme dayanmalı, kullanıcının
	// gönderdiğine değil.
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	pre, err := s.store.StateUnifyPreflight(ctx)
	cancel()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !pre.Supported {
		writeJSONError(w, http.StatusPreconditionFailed, "ön kontrol geçmedi: "+pre.Detail)
		return
	}
	if b.Cluster != "" && b.Cluster != pre.Cluster {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf(
			"küme uyuşmuyor: istek '%s', kurulum '%s'", b.Cluster, pre.Cluster))
		return
	}

	want := map[string]bool{}
	for _, t := range b.Tables {
		want[t] = true
	}
	var plan []chstore.StateUnifyTable
	for _, t := range pre.Tables {
		if !t.Split || t.Blocked != "" {
			continue
		}
		if len(want) > 0 && !want[t.Name] {
			continue
		}
		plan = append(plan, t)
	}
	if len(plan) == 0 {
		writeJSONError(w, http.StatusPreconditionFailed, "birleştirilecek bölünmüş tablo yok")
		return
	}

	by := ""
	if claims := auth.FromContext(r.Context()); claims != nil {
		by = claims.Email
	}
	if !s.stateUnify.begin(pre.Cluster, by, len(plan)) {
		w.WriteHeader(http.StatusConflict)
		writeJSON(w, s.stateUnify.snapshot())
		return
	}

	s.audit(r, "state_unify.apply", "clickhouse", pre.Cluster,
		fmt.Sprintf(`{"cluster":%q,"tables":%d,"split":%d}`, pre.Cluster, len(plan), pre.SplitCount))

	// İstek bağlamı DEĞİL: tarayıcı kapansa da ON CLUSTER RENAME zinciri
	// yarıda kesilmemeli. 6 saat tavanı prod'daki `problems` için bol.
	go func(plan []chstore.StateUnifyTable, cluster string) {
		bg, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
		defer cancel()
		failed := ""
		for _, t := range plan {
			s.stateUnify.startTable(t.Name)
			res := s.store.StateUnifyMigrateTable(bg, cluster, t)
			s.stateUnify.recordTable(res)
			if !res.OK {
				// İLK HATADA DUR. Kalan tablolara dokunma: yarım bir göç,
				// yanlış bir göçten kolay geri alınır.
				failed = t.Name + ": " + res.Err
				break
			}
		}
		s.stateUnify.finish(failed)
	}(plan, pre.Cluster)

	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, s.stateUnify.snapshot())
}

func (s *Server) postStateUnifyCleanup(w http.ResponseWriter, r *http.Request) {
	var b stateUnifyApplyBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeJSONError(w, http.StatusBadRequest, "gövde ayrıştırılamadı: "+err.Error())
		return
	}
	if len(b.Tables) == 0 {
		writeJSONError(w, http.StatusBadRequest, "silinecek yedek seçilmedi")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	pre, err := s.store.StateUnifyPreflight(ctx)
	cancel()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if pre.Cluster == "" {
		writeJSONError(w, http.StatusPreconditionFailed, "küme adı ayarlı değil")
		return
	}
	if pre.SplitCount > 0 {
		writeJSONError(w, http.StatusPreconditionFailed, fmt.Sprintf(
			"%d tablo hâlâ bölünmüş — göç bitmeden yedek silinmez", pre.SplitCount))
		return
	}

	bg, cancel2 := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Minute)
	defer cancel2()
	steps := s.store.StateUnifyDropBackups(bg, pre.Cluster, b.Tables)

	okN := 0
	for _, st := range steps {
		if st.OK {
			okN++
		}
	}
	s.audit(r, "state_unify.cleanup", "clickhouse", pre.Cluster,
		fmt.Sprintf(`{"cluster":%q,"ok":%d,"total":%d}`, pre.Cluster, okN, len(steps)))

	writeJSON(w, map[string]any{"steps": steps, "ok": okN == len(steps)})
}
