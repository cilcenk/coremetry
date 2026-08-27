package api

// mcp_client_routes.go — v0.10.87 (MCP istemci dilim ②).
//
// api.go BÜYÜMEYECEK kuralı (registerVMetricsRoutes emsali): dış MCP
// sunucu listesinin rotaları kendi dosyasında, api.go tek satır register
// çağrısıyla büyür.
//
//	GET  /api/settings/mcp-servers       — sırsız snapshot + canlı sağlık
//	PUT  /api/settings/mcp-servers       — kaydet (token merge + audit)
//	POST /api/settings/mcp-servers/test  — TEK sunucuyu kaydetmeden prova
//
// Üçü de admin: liste hem kimlik (token) hem YÜZEY (modelin çağıracağı
// dış uçlar) taşıyor — editor'ün alarm kuralı yazması başka, sohbet
// döngüsüne yeni bir dış bağımlılık sokması başka iş.
//
// Sır sözleşmesi devops sekmesinin aynısı: GET hiç token vermez
// (hasToken), PUT'ta boş/"********" saklıyı korur, URL/komut boşalırsa
// token da düşer (öksüz kimlik kalmaz). Birleştirme SAF mergeMCPServers'ta
// — tablo testi handler'sız sürer.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/mcpclient"
)

func (s *Server) registerMCPClientRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings/mcp-servers", auth.RequireRole(auth.RoleAdmin, s.getMCPClientSettings))
	mux.HandleFunc("PUT /api/settings/mcp-servers", auth.RequireRole(auth.RoleAdmin, s.putMCPClientSettings))
	mux.HandleFunc("POST /api/settings/mcp-servers/test", auth.RequireRole(auth.RoleAdmin, s.testMCPClientServer))
}

// maxMCPServers — liste tavanı. Her sunucu sohbet kataloğuna tool
// enjekte eder ve küçük yerel modelin katalog diyeti (v0.9.1230) sınırlı;
// sekiz sunucu bile cömert.
const maxMCPServers = 8

// mcpServerInput — PUT/test gövdesindeki tek sunucu.
type mcpServerInput struct {
	Name               string   `json:"name"`
	Transport          string   `json:"transport"`
	URL                string   `json:"url"`
	Token              string   `json:"token"`
	Command            string   `json:"command"`
	Args               []string `json:"args"`
	Enabled            bool     `json:"enabled"`
	AllowTools         []string `json:"allowTools"`
	DenyTools          []string `json:"denyTools"`
	InsecureSkipVerify bool     `json:"insecureSkipVerify"`
}

// mergeMCPServers — girdi listesini doğrular ve saklı token'ları
// üstüne katlar. Dönen ikinci değer 400 gövdesi ("" = geçerli).
//
// Token sözleşmesi (devops mergeDevOpsSettings'in üç hâli):
//   - ""          → saklıyı koru
//   - "********"  → saklıyı koru (UI round-trip sentineli)
//   - başka değer → değiştir
//
// Eşleme SANITIZE edilmiş ada göre: Registry'nin kimliği o; operatör
// "Runbook KB"yi "runbook kb" yazsa da saklı token aynı sunucuya aittir.
func mergeMCPServers(in []mcpServerInput, cur mcpclient.Settings) (mcpclient.Settings, string) {
	if len(in) > maxMCPServers {
		return mcpclient.Settings{}, "en fazla 8 sunucu tanımlanabilir"
	}
	stored := map[string]string{} // sanitize(ad) → token
	for _, sv := range cur.Servers {
		stored[mcpclient.SanitizedName(sv.Name)] = sv.Token
	}
	seen := map[string]bool{}
	out := mcpclient.Settings{}
	for _, sv := range in {
		name := strings.TrimSpace(sv.Name)
		key := mcpclient.SanitizedName(name)
		if key == "" {
			return mcpclient.Settings{}, "sunucu adı boş olamaz"
		}
		if seen[key] {
			return mcpclient.Settings{}, "sunucu adı tekrar ediyor: " + name
		}
		seen[key] = true

		cfg := mcpclient.ServerConfig{
			Name: name, Transport: strings.TrimSpace(sv.Transport),
			URL: strings.TrimSpace(sv.URL), Token: sv.Token,
			Command: strings.TrimSpace(sv.Command), Args: cleanConventionList(sv.Args),
			Enabled:    sv.Enabled,
			AllowTools: cleanConventionList(sv.AllowTools),
			DenyTools:  cleanConventionList(sv.DenyTools),
			InsecureSkipVerify: sv.InsecureSkipVerify,
		}
		if cfg.Transport == "" {
			cfg.Transport = "http"
		}
		switch cfg.Transport {
		case "http":
			lower := strings.ToLower(cfg.URL)
			if cfg.URL == "" || (!strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://")) {
				return mcpclient.Settings{}, name + ": http taşıması http(s):// ile başlayan URL ister"
			}
		case "stdio":
			if cfg.Command == "" {
				return mcpclient.Settings{}, name + ": stdio taşıması komut yolu ister"
			}
		default:
			return mcpclient.Settings{}, name + ": taşıma http ya da stdio olmalı"
		}
		if cfg.Token == "" || cfg.Token == secretKept {
			cfg.Token = stored[key]
		}
		// URL boşalınca (stdio'ya geçiş dahil) http token'ı düşer —
		// hiçbir ekranın göstermediği öksüz kimlik system_settings'te
		// kalmasın (devops baseUrl sözleşmesinin aynısı).
		if cfg.URL == "" {
			cfg.Token = ""
		}
		out.Servers = append(out.Servers, cfg)
	}
	return out, ""
}

func (s *Server) getMCPClientSettings(w http.ResponseWriter, r *http.Request) {
	if s.mcpClient == nil {
		writeJSON(w, mcpclient.Snapshot{Servers: []mcpclient.ServerSnapshot{}})
		return
	}
	writeJSON(w, s.mcpClient.Snapshot())
}

type mcpServersInput struct {
	Servers []mcpServerInput `json:"servers"`
}

func (s *Server) putMCPClientSettings(w http.ResponseWriter, r *http.Request) {
	if s.mcpClient == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "mcp istemcisi bu kurulumda yok")
		return
	}
	var in mcpServersInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return
	}
	cfg, badReq := mergeMCPServers(in.Servers, s.mcpClient.CurrentSettings())
	if badReq != "" {
		writeJSONError(w, http.StatusBadRequest, badReq)
		return
	}
	if err := s.mcpClient.SavePersisted(r.Context(), s.store, cfg); err != nil {
		writeErr(w, err)
		return
	}
	// Case reloadConfigOnSignal'da AYNI sürümde (thanos v0.9.237 dersi:
	// dinleyicisiz publish peer pod'ları 30s poll'a bırakır).
	s.publishConfigReload(r.Context(), "mcpclient")
	snap := s.mcpClient.Snapshot()
	s.audit(r, "settings.mcp_servers.update", "settings", "mcp_client_servers",
		string(mcpServersAuditDetails(snap)))
	writeJSON(w, snap)
}

// mcpServersAuditDetails — sırsız iz. Token audit_log'a HİÇ girmez;
// hasToken zaten GET şeklinin parçası. Adres + taşıma + etkinlik izde,
// çünkü "modelin çağırdığı dış uç kim ekledi" sorusu tam bu izin işi.
func mcpServersAuditDetails(snap mcpclient.Snapshot) []byte {
	type row struct {
		Name      string `json:"name"`
		Transport string `json:"transport"`
		URL       string `json:"url,omitempty"`
		Command   string `json:"command,omitempty"`
		Enabled   bool   `json:"enabled"`
		HasToken  bool   `json:"hasToken"`
	}
	rows := make([]row, 0, len(snap.Servers))
	for _, sv := range snap.Servers {
		rows = append(rows, row{Name: sv.Name, Transport: sv.Transport,
			URL: sv.URL, Command: sv.Command, Enabled: sv.Enabled, HasToken: sv.HasToken})
	}
	b, _ := json.Marshal(map[string]any{"servers": rows, "count": len(rows)})
	return b
}

// testMCPClientServer — gövdedeki TEK sunucuyu kaydetmeden prova eder;
// boş token saklıyla birleştirilir (aynı merge yolu — ikinci bir yazım
// sözleşmeyi sessizce ayrıştırırdı).
func (s *Server) testMCPClientServer(w http.ResponseWriter, r *http.Request) {
	if s.mcpClient == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "mcp istemcisi bu kurulumda yok")
		return
	}
	var in mcpServerInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return
	}
	cfg, badReq := mergeMCPServers([]mcpServerInput{in}, s.mcpClient.CurrentSettings())
	if badReq != "" {
		writeJSONError(w, http.StatusBadRequest, badReq)
		return
	}
	writeJSON(w, s.mcpClient.Test(r.Context(), cfg.Servers[0]))
}
