package api

// spool_actions.go — Distributed spool runbook'unun HTTP yüzeyi
// (v0.9.1191, operatör isteği: "Runbook sen otomatik ekle yeni versiyonda
// ben çalıştırayım").
//
// /admin/sql bilinçli salt-okunur; bu iki SYSTEM komutu o kararın İSTİSNASI
// değil, DIŞINDA duran adlandırılmış eylemler: serbest SQL değil, iki sabit
// komut × doğrulanmış tablo adı. İkisi de admin-kapılı ve audit'li — "kim,
// hangi tabloya, ne zaman flush bastı" sorusunun cevabı audit_log'da.
//
// FLUSH ASENKRON KOŞAR. 3M dosyalık senkron bir flush saatler sürer; onu
// HTTP isteğinde bekletmek hem isteği hem (pratikte) tarayıcıyı öldürür.
// Düğme işi BAŞLATIR; ilerlemenin gerçek göstergesi zaten 30 sn'de bir
// ölçülen spool derinliğidir (distribution_queue) — ayrı bir ilerleme
// mekanizması uydurmuyoruz.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
)

// spoolFlushState — bir tablonun flush uçuş kaydı. DoneAt==0 → koşuyor.
type spoolFlushState struct {
	Table     string `json:"table"`
	StartedBy string `json:"startedBy"`
	StartedAt int64  `json:"startedAt"` // unix ns
	DoneAt    int64  `json:"doneAt,omitempty"`
	Error     string `json:"error,omitempty"`
}

// spoolFlights — süreç-yerel uçuş defteri.
//
// BİLEREK süreç-yerel (Redis değil): FLUSH'ı çalıştıran bağlantı BU
// pod'da yaşıyor; başka pod'un "koşuyor" kaydını göstermek, o pod ölünce
// yalana dönerdi. Çok-pod kurulumda iki pod aynı tabloya flush basarsa
// CH tarafında ikisi aynı kuyruğu boşaltır — israf ama tehlikesiz
// (idempotent iş, aynı dosya iki kez gönderilmez).
type spoolFlights struct {
	mu sync.Mutex
	m  map[string]*spoolFlushState
}

// begin — tek-uçuş kapısı. Koşan varken ikinci flush REDDEDİLİR: aynı
// kuyruğa ikinci senkron flush CH'de ek iş açmaz ama düğmenin "bastım,
// bir şey olmadı" hissi vermesine ve audit'in şişmesine yol açar.
func (f *spoolFlights) begin(table, by string, nowNs int64) (*spoolFlushState, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[string]*spoolFlushState{}
	}
	if cur, ok := f.m[table]; ok && cur.DoneAt == 0 {
		return cur, false
	}
	st := &spoolFlushState{Table: table, StartedBy: by, StartedAt: nowNs}
	f.m[table] = st
	return st, true
}

func (f *spoolFlights) finish(table string, nowNs int64, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if st, ok := f.m[table]; ok {
		st.DoneAt = nowNs
		if err != nil {
			st.Error = err.Error()
		}
	}
}

func (f *spoolFlights) snapshot() []spoolFlushState {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]spoolFlushState, 0, len(f.m))
	for _, st := range f.m {
		out = append(out, *st)
	}
	return out
}

// getSpoolState — GET /api/admin/clickhouse/spool. Runbook'un otomatik
// yarısı: kuyruk + diskler + Distributed tablo listesi + uçuş defteri.
// serveCached YOK ve bilinçli: admin-only, elle açılan bir panel; bayat
// bir kopya tam da "flush işe yaradı mı" bakışını yalanlar.
func (s *Server) getSpoolState(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	queue := s.store.CollectDistributionQueue(ctx)
	disks, derr := s.store.CollectDisks(ctx)
	tables, terr := s.store.ListDistributedTables(ctx)
	resp := map[string]any{
		"queue":   queue, // nil = tek düğüm (kavram yok) — FE panel çizmez
		"disks":   disks,
		"tables":  tables,
		"flights": s.spoolFlights.snapshot(),
	}
	if derr != nil {
		resp["disksError"] = derr.Error()
	}
	if terr != nil {
		resp["tablesError"] = terr.Error()
	}
	writeJSON(w, resp)
}

// spoolTableFromBody — iki POST'un ortak gövdesi + doğrulaması.
func (s *Server) spoolTableFromBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var in struct {
		Table string `json:"table"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return "", false
	}
	table := strings.TrimSpace(in.Table)
	ok, err := s.store.ValidSpoolTable(r.Context(), table)
	if err != nil {
		writeErr(w, err)
		return "", false
	}
	if !ok {
		writeJSONError(w, http.StatusBadRequest,
			fmt.Sprintf("%q bu kurulumda bir Distributed tablo değil", table))
		return "", false
	}
	return table, true
}

// postSpoolFlush — POST /api/admin/clickhouse/spool/flush {table}.
func (s *Server) postSpoolFlush(w http.ResponseWriter, r *http.Request) {
	table, ok := s.spoolTableFromBody(w, r)
	if !ok {
		return
	}
	by := userIDFromRequest(r)
	st, started := s.spoolFlights.begin(table, by, time.Now().UnixNano())
	if !started {
		// 409: koşan uçuşun kaydı gövdede — FE "zaten çalışıyor"u
		// başlangıç zamanıyla gösterebilsin.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(st)
		return
	}
	s.audit(r, "clickhouse.flush_distributed", "clickhouse", table, "{}")
	log.Printf("[spool] FLUSH DISTRIBUTED %s başladı (isteyen: %s)", table, by)
	go func() {
		// İstek context'i DEĞİL: flush isteği bittikten sonra saatlerce
		// yaşar. Tavan chstore'un kendi ReadTimeout'uyla hizalı.
		ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
		defer cancel()
		err := s.store.FlushDistributed(ctx, table)
		s.spoolFlights.finish(table, time.Now().UnixNano(), err)
		if err != nil {
			// Dürüstlük notu: sürücü kopması sunucudaki flush'ı da iptal
			// eder ama O ANA KADAR giden dosyalar gitmiştir — tekrar basmak
			// kaldığı yerden ilerletir. Log bunu söyler ki operatör
			// "hata=hiçbir şey olmadı" okumasın.
			log.Printf("[spool] FLUSH DISTRIBUTED %s HATA (o ana dek gönderilen dosyalar gitti; tekrar başlatmak kaldığı yerden sürer): %v", table, err)
			return
		}
		log.Printf("[spool] FLUSH DISTRIBUTED %s tamamlandı", table)
	}()
	writeJSON(w, st)
}

// postSpoolStartSends — POST /api/admin/clickhouse/spool/start-sends
// {table}. Anlık ve idempotent; senkron koşar.
func (s *Server) postSpoolStartSends(w http.ResponseWriter, r *http.Request) {
	table, ok := s.spoolTableFromBody(w, r)
	if !ok {
		return
	}
	if err := s.store.StartDistributedSends(r.Context(), table); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "clickhouse.start_distributed_sends", "clickhouse", table, "{}")
	writeJSON(w, map[string]any{"ok": true, "table": table})
}

// userIDFromRequest — uçuş defterindeki "kim başlattı" alanı. Audit'in
// kimliğiyle aynı kaynaktan; boşsa "?" (audit zaten gerçek kaydı tutar).
func userIDFromRequest(r *http.Request) string {
	if c := auth.FromContext(r.Context()); c != nil && strings.TrimSpace(c.UserID) != "" {
		return c.UserID
	}
	return "?"
}
