package api

// copilot_starters.go — v0.10.702 (operatör isteği 2026-09-12: "ilk sohbetteki
// öneriler zenginleşsin — takımımın servislerine ek olarak belirli bir
// endpoint'te hata alan trace'ler").
//
//	GET /api/copilot/starters?range_s=3600
//
// Boş sohbetin VERİ çipleri. Statik çipler ("Takımımın servisleri nasıl?")
// FE'de kalır; burası kullanıcının o anki durumundan üretilen 0-2 çip:
// takım → takım servisleri şu anki hata oranına göre (my_services ile aynı
// okuma: mcptools.ReadTeamServicesRED) → EN KÖTÜ servis → o servisin en çok
// hata alan yolu. Üretilen her çip guided router'da LLM'siz yönlenen TAM
// cümledir (copilot_followup.go sözleşmesi; copilot_starters_test her
// cümleyi routeGuidedIntent'ten geçirir). Takımsız kullanıcı, servissiz
// takım, hatasız pencere → boş liste: uydurma çip yok.
//
// requireCopilot kapısı YOK — LLM kullanmıyor (config ucu gibi). Rol kapısı
// yok — viewer de görür. api.go/ai_routes.go BÜYÜMEZ: route defteri (init).
// Cache 60 s (guidedTeamCatalogue ile aynı tazelik); anahtar kullanıcı
// kimliği (takım ondan türer) + pencere. MV okumaları, ES yok.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/mcptools"
)

func init() { registerRoutesExtra("copilot_starters", (*Server).registerCopilotStarterRoutes) }

func (s *Server) registerCopilotStarterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/copilot/starters", s.copilotStarters)
}

// copilotStarter — bir veri çipi. Chip kısa etiket, Question gönderilen
// tam cümle (router'a giden metin), Kind FE'nin ikon/ton seçimi için.
type copilotStarter struct {
	Chip     string `json:"chip"`
	Question string `json:"question"`
	Kind     string `json:"kind"` // service_health | endpoint_errors
}

const (
	startersRangeDefault = 3600
	startersRangeMin     = 300
	startersRangeMax     = 86400
	startersEndpointTopN = 5
)

// starterChips — SAF. worst = takımın en kötü servisi (hata oranına göre),
// route = o servisin en çok hata alan yolu. Hata sıfırsa çip üretmez;
// yol "/" ile başlamıyorsa (gRPC metodu, mesajlaşma öznesi) endpoint çipi
// üretmez — router'ın yol deseni "/…" ister, aksi hâlde yanlış tanımlayıcı
// ile boş aday listesine düşerdi.
func starterChips(worst *chstore.ServiceSummary, route *chstore.EndpointRow) []copilotStarter {
	out := []copilotStarter{}
	if worst == nil || worst.Name == "" || worst.ErrorCount == 0 {
		return out
	}
	out = append(out, copilotStarter{
		Chip:     worst.Name + " sağlığı nasıl?",
		Question: worst.Name + " sağlığı nasıl?",
		Kind:     "service_health",
	})
	if route != nil && route.Errors > 0 && routeChipEligible(route.Path) {
		out = append(out, copilotStarter{
			Chip:     route.Path + " hatalı trace'leri",
			Question: route.Path + " hatalı trace'lerini getir",
			Kind:     "endpoint_errors",
		})
	}
	return out
}

// routeChipEligible — router'ın yol deseniyle uyumlu: "/" öneki, ≥3
// karakter, boşluksuz.
func routeChipEligible(path string) bool {
	p := strings.TrimSpace(path)
	return strings.HasPrefix(p, "/") && len(p) >= 3 && !strings.ContainsAny(p, " \t\n")
}

// worstTeamService — SAF: hata oranına göre sıralı listede hata TAŞIYAN
// ilk servis; hiçbiri hata taşımıyorsa nil.
func worstTeamService(rows []chstore.ServiceSummary) *chstore.ServiceSummary {
	for i := range rows {
		if rows[i].ErrorCount > 0 && rows[i].Name != "" {
			return &rows[i]
		}
	}
	return nil
}

// worstEndpoint — SAF: hata-sıralı endpoint listesinde çip'e uygun ilk yol.
func worstEndpoint(rows []chstore.EndpointRow) *chstore.EndpointRow {
	for i := range rows {
		if rows[i].Errors > 0 && routeChipEligible(rows[i].Path) {
			return &rows[i]
		}
	}
	return nil
}

func (s *Server) copilotStarters(w http.ResponseWriter, r *http.Request) {
	claims := auth.FromContext(r.Context())
	if claims == nil || claims.UserID == "" {
		writeJSON(w, map[string]any{"starters": []copilotStarter{}})
		return
	}
	rangeS := parseInt(r.URL.Query().Get("range_s"), startersRangeDefault)
	if rangeS < startersRangeMin || rangeS > startersRangeMax {
		rangeS = startersRangeDefault
	}
	userID := claims.UserID
	key := fmt.Sprintf("copilot:starters:v1:u=%s:r=%d", userID, rangeS)
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		empty := map[string]any{"starters": []copilotStarter{}, "rangeS": rangeS}
		u, err := s.store.GetUserByID(ctx, userID)
		if err != nil || u == nil || u.Team == "" {
			return empty, nil
		}
		mds, err := s.store.ListServiceMetadata(ctx)
		if err != nil {
			return nil, err
		}
		svcs := servicesForUserTeam(s.teamAliasesCtx(ctx), mds, u.Team)
		if len(svcs) == 0 {
			return empty, nil
		}
		if len(svcs) > maxTeamServices {
			svcs = svcs[:maxTeamServices]
		}
		to := time.Now()
		from := to.Add(-time.Duration(rangeS) * time.Second)
		rows, err := mcptools.ReadTeamServicesRED(ctx, s.mcpDeps(), svcs, from, to)
		if err != nil {
			return nil, err
		}
		mcptools.SortServicesByErrorRate(rows)
		worst := worstTeamService(rows)
		var route *chstore.EndpointRow
		if worst != nil {
			eps, eerr := s.store.GetEndpointsMV(ctx, chstore.EndpointsQuery{
				From: from, To: to, Service: worst.Name,
				Limit: startersEndpointTopN, Sort: "errors", Dir: "desc",
			})
			if eerr == nil {
				route = worstEndpoint(eps)
			}
		}
		return map[string]any{"starters": starterChips(worst, route), "team": u.Team, "rangeS": rangeS}, nil
	})
}
