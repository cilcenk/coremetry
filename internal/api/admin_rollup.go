// admin_rollup.go — rollup kurulum sihirbazının HTTP yüzeyi (v0.9.770).
//
// Dört uç, hepsi admin: status (ne var), preflight (kaldırır mı),
// apply (kur), rollback (yazımı kes). Çekirdek mantık ve "neden
// otomatik değil" gerekçesi chstore/rollup_admin.go başlığında.
//
// serveCached YOK — bilinçli. Bu bir operatör KONTROL yüzeyi: "Kur"a
// bastıktan 5 saniye sonra basılan "Yenile" 30 saniyelik bir cache'ten
// eski gerçeği döndürürse operatör kurulumun tutmadığını sanar. Okuma
// maliyeti de zaten düşük (7 count) ve kart yalnız admin sayfası
// açıkken, elle tetiklenerek okunuyor.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

// registerRollupAdminRoutes — api.go'daki /api/admin/clickhouse bloğundan
// çağrılır (registerXxxRoutes deseni, ch_ddl_queue.go ile aynı).
func (s *Server) registerRollupAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/rollup/status",
		auth.RequireRole(auth.RoleAdmin, s.getRollupStatus))
	mux.HandleFunc("GET /api/admin/rollup/preflight",
		auth.RequireRole(auth.RoleAdmin, s.getRollupPreflight))
	mux.HandleFunc("POST /api/admin/rollup/apply",
		auth.RequireRole(auth.RoleAdmin, s.postRollupApply))
	mux.HandleFunc("POST /api/admin/rollup/rollback",
		auth.RequireRole(auth.RoleAdmin, s.postRollupRollback))
}

func (s *Server) getRollupStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := s.store.RollupStatus(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) getRollupPreflight(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := s.store.RollupPreflight(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

// rollupActionBody — apply/rollback ortak gövdesi.
type rollupActionBody struct {
	Cluster string `json:"cluster"`
	Target  string `json:"target"`
}

// decodeRollupAction — gövdeyi çözer ve target'ı normalize eder.
// Boş target = "both" (UI varsayılanı); geçersiz target 400 döner —
// sessizce "both"a düşürmek, operatörün yalnız metrikleri kurmak
// isterken span ailesini de kurmasına yol açardı.
func decodeRollupAction(w http.ResponseWriter, r *http.Request) (rollupActionBody, bool) {
	var b rollupActionBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return b, false
	}
	b.Cluster = strings.TrimSpace(b.Cluster)
	b.Target = strings.TrimSpace(b.Target)
	if b.Target == "" {
		b.Target = "both"
	}
	switch b.Target {
	case "narrow", "metrics", "both":
	default:
		http.Error(w, "target must be narrow | metrics | both", http.StatusBadRequest)
		return b, false
	}
	return b, true
}

// rollupResultsOK — listede hata var mı. Audit ayrıntısına giriyor:
// denemenin YAPILDIĞI değil, TUTUP tutmadığı da denetim kaydında olmalı.
func rollupResultsOK(res []chstore.RollupStmtResult) bool {
	for _, r := range res {
		if !r.OK {
			return false
		}
	}
	return len(res) > 0
}

// auditRollupDetail — denetim kaydının details alanı. Cluster + kaç
// ifadenin tuttuğu + İLK hatanın metni. İlk hata şart: denetim kaydı
// "başarısız" derken NEDEN başarısız olduğunu da taşımalı, yoksa
// olay sonrası inceleme yeniden CH loglarına inmek zorunda kalır.
func auditRollupDetail(cluster string, res []chstore.RollupStmtResult) string {
	okCount, firstErr := 0, ""
	for _, r := range res {
		if r.OK {
			okCount++
			continue
		}
		if firstErr == "" {
			firstErr = r.Head + ": " + r.Err
		}
	}
	return fmt.Sprintf(`{"cluster":%q,"ok":%d,"total":%d,"firstError":%q}`,
		cluster, okCount, len(res), firstErr)
}

func (s *Server) postRollupApply(w http.ResponseWriter, r *http.Request) {
	b, ok := decodeRollupAction(w, r)
	if !ok {
		return
	}
	if b.Cluster == "" {
		http.Error(w, "cluster required", http.StatusBadRequest)
		return
	}
	// 5 dk: ON CLUSTER DDL dağıtık kuyruğa girer ve
	// distributed_ddl_task_timeout (varsayılan 180s) her ifade için
	// ayrı ayrı işler. 19 ifadelik "both" hedefinde istek ömrü
	// tarayıcı zaman aşımından uzun olabilir — üst sınırı burada
	// çiziyoruz ki kopan bağlantı DDL'i yarıda bırakmasın.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()
	res := s.store.RollupApply(ctx, b.Cluster, b.Target)
	s.audit(r, "rollup.apply", "clickhouse", b.Target, auditRollupDetail(b.Cluster, res))
	writeJSON(w, map[string]any{
		"statements": res,
		"ok":         rollupResultsOK(res),
	})
}

func (s *Server) postRollupRollback(w http.ResponseWriter, r *http.Request) {
	b, ok := decodeRollupAction(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Minute)
	defer cancel()
	res := s.store.RollupRollback(ctx, b.Cluster, b.Target)
	s.audit(r, "rollup.rollback", "clickhouse", b.Target, auditRollupDetail(b.Cluster, res))
	writeJSON(w, map[string]any{
		"statements": res,
		"ok":         rollupResultsOK(res),
	})
}
