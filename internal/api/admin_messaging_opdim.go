package api

// admin_messaging_opdim.go — v0.10.564 (messaging_summary_5m `operation`
// boyutunun YERİNDE geçişi; Admin → ClickHouse sihirbazı).
// admin_function_id.go aynası; kayıt route_registry.go defterinden
// (api.go büyümez).
//
//	GET  /api/admin/messaging-opdim/status     admin — dört sözleşme parçası, host başına
//	GET  /api/admin/messaging-opdim/preflight  admin — küme listesi + üretilecek TAM SQL
//	POST /api/admin/messaging-opdim/apply      admin — {cluster}; ALTER zinciri; audit
//
// Geri alma rotası YOK ve bilinçli: MODIFY ORDER BY anahtarı yalnız
// genişletebilir (CH daraltmaz), MODIFY QUERY'yi eski SELECT'e döndürmek de
// kolonu/anahtarı geri almaz. Gerekçe chstore/messaging_opdim_admin.go
// başlığında; yanlışlıkla "geri al" düğmesi çizilmesin diye burada da yazılı.
//
// Apply HER seferinde ön kontrolü yeniden koşar (istemciye güvenmez) ve
// store tarafı da bağımsız olarak koşar — çift kapı bilinçli: bu uç HTTP
// dışından (test, ileride MCP) da çağrılabilir.
//
// Süre: ON CLUSTER ALTER dağıtık DDL kuyruğuna girer; MODIFY ORDER BY +
// MODIFY QUERY metadata mutasyonu, veri yeniden yazılmaz — 5 dk fazlasıyla
// yeter. İstek kopsa da DDL yarıda kalmasın: context.WithoutCancel.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
)

func init() { registerRoutesExtra("messaging-opdim", (*Server).registerMessagingOpDimRoutes) }

func (s *Server) registerMessagingOpDimRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/messaging-opdim/status",
		auth.RequireRole(auth.RoleAdmin, s.getMessagingOpDimStatus))
	mux.HandleFunc("GET /api/admin/messaging-opdim/preflight",
		auth.RequireRole(auth.RoleAdmin, s.getMessagingOpDimPreflight))
	mux.HandleFunc("POST /api/admin/messaging-opdim/apply",
		auth.RequireRole(auth.RoleAdmin, s.postMessagingOpDimApply))
}

// decodeMessagingOpDimAction — {cluster}; decodeEntityLayerAction'dan AYRI
// olmasının tek nedeni: o, boş cluster'ı 400 ile reddediyor. Bu sihirbaz
// TEK-NODE kurulumda da anlamlı (combined MV'nin inner'ı orada da var) ve
// orada ON CLUSTER hiç basılmaz — boş gövde meşru. Küme kipinde adın
// zorunluluğunu store tarafı (preflight) uygular; burada yalnız biçim.
//
// Boş gövde de kabul: `POST` gövdesiz gelirse cluster = "".
func decodeMessagingOpDimAction(w http.ResponseWriter, r *http.Request) (string, bool) {
	var in struct {
		Cluster string `json:"cluster"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return "", false
	}
	return strings.TrimSpace(in.Cluster), true
}

func (s *Server) getMessagingOpDimStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := s.store.MessagingOpDimStatus(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) getMessagingOpDimPreflight(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	res, err := s.store.MessagingOpDimPreflight(ctx, strings.TrimSpace(r.URL.Query().Get("cluster")))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) postMessagingOpDimApply(w http.ResponseWriter, r *http.Request) {
	cluster, ok := decodeMessagingOpDimAction(w, r)
	if !ok {
		return
	}
	pctx, pcancel := context.WithTimeout(context.WithoutCancel(r.Context()), 45*time.Second)
	pre, err := s.store.MessagingOpDimPreflight(pctx, cluster)
	pcancel()
	if err != nil || !pre.Supported || len(pre.ProbeErrors) > 0 {
		detail := pre.Detail
		if err != nil {
			detail = err.Error()
		} else if len(pre.ProbeErrors) > 0 {
			detail += " (" + strings.Join(pre.ProbeErrors, "; ") + ")"
		}
		s.audit(r, "messaging_opdim.apply", "clickhouse", "messaging_summary_5m", "REDDEDİLDİ cluster="+cluster+": "+detail)
		writeJSONError(w, http.StatusConflict, "ön kontrol geçmedi — "+detail)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()
	res := s.store.MessagingOpDimApply(ctx, cluster)
	s.audit(r, "messaging_opdim.apply", "clickhouse", "messaging_summary_5m", auditRollupDetail(cluster, res))
	writeJSON(w, map[string]any{"statements": res, "ok": rollupResultsOK(res),
		"note": "deploy'dan ÖNCE koşun; kolon boot'tan önce var olur, boot geçişi no-op'a düşer ve 90 günlük messaging kovaları korunur"})
}
