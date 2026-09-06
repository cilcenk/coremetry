package api

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/mcp"
	"github.com/cilcenk/coremetry/internal/mcptools"
)

// v0.9.1136 (AI Faz 3.1, K7) — MCP yetki katmanının testleri.
//
// Olay/gerekçe: kapı (v0.9.14) yalnız KİMLİĞİ okuyordu (rate limit
// için); Claims.Role'e hiç bakmıyordu. mcp.go ve api.go yorumları
// "roller REST'te olduğu gibi MCP'de de geçerli" diyordu — yanlıştı.
// Somut sapma: GET /api/anomalies/active editor-kapılıydı, aynı
// satırları döndüren list_anomalies MCP'de kapısızdı.
//
// Üç sınıf pinlenir:
//  1. karar fonksiyonu (roleRank/roleSatisfies) — bilinmeyen rol ve
//     yazım-hatalı MinRole dâhil;
//  2. SIRA — rol reddi rate bütçesini TÜKETMEZ (mutasyon: iki bloğu
//     yer değiştir → bu test kırmızı);
//  3. spec filtresi — modele MinRole'ü aşan tool GÖSTERİLMEZ ve
//     çağrılamaz.
//
// Artı A7 kararının kaynak pini (REST ucu viewer'a indi).

func TestRoleSatisfiesMatrix(t *testing.T) {
	cases := []struct {
		name    string
		role    string
		minRole string
		want    bool
	}{
		{"viewer × açık tool", auth.RoleViewer, "", true},
		{"viewer × editor tool", auth.RoleViewer, auth.RoleEditor, false},
		{"viewer × admin tool", auth.RoleViewer, auth.RoleAdmin, false},
		{"editor × açık tool", auth.RoleEditor, "", true},
		{"editor × editor tool", auth.RoleEditor, auth.RoleEditor, true},
		{"editor × admin tool", auth.RoleEditor, auth.RoleAdmin, false},
		{"admin × açık tool", auth.RoleAdmin, "", true},
		{"admin × editor tool", auth.RoleAdmin, auth.RoleEditor, true},
		{"admin × admin tool", auth.RoleAdmin, auth.RoleAdmin, true},
		// Custom roller viewer'ın ALT kümesidir (auth.go:77-81) — açık
		// okumaları geçer, editor eşiğini geçmez. Bugünkü kapısız MCP
		// davranışını korur.
		{"custom rol × açık tool", "db-team", "", true},
		{"custom rol × editor tool", "db-team", auth.RoleEditor, false},
		// Kimliksiz: hiçbir kapıdan geçmez, MinRole "" olsa bile.
		{"boş rol × açık tool", "", "", false},
		{"boş rol × editor tool", "", auth.RoleEditor, false},
		// Yazım hatalı MinRole = KAPALI yön (fail-closed), admin bile.
		{"admin × yazım hatalı MinRole", auth.RoleAdmin, "editr", false},
		{"viewer × yazım hatalı MinRole", auth.RoleViewer, "Editor", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := roleSatisfies(tc.role, tc.minRole); got != tc.want {
				t.Fatalf("roleSatisfies(%q,%q)=%v, want %v", tc.role, tc.minRole, got, tc.want)
			}
		})
	}
}

func TestRoleRankOrdering(t *testing.T) {
	if !(roleRank(auth.RoleAdmin) > roleRank(auth.RoleEditor) &&
		roleRank(auth.RoleEditor) > roleRank(auth.RoleViewer) &&
		roleRank(auth.RoleViewer) == 0) {
		t.Fatalf("sıralama bozuk: admin=%d editor=%d viewer=%d",
			roleRank(auth.RoleAdmin), roleRank(auth.RoleEditor), roleRank(auth.RoleViewer))
	}
	if roleRank("") >= 0 {
		t.Fatalf("boş rol negatif olmalı, got %d", roleRank(""))
	}
	if roleRank("db-team") != roleRank(auth.RoleViewer) {
		t.Fatalf("custom rol viewer seviyesinde olmalı, got %d", roleRank("db-team"))
	}
}

// Kapının red metinleri: gereken rolü ve mevcut rolü ADIYLA söyler
// (modele okunur gider), yanlış-yapılandırma ayrı metinle görünür.
func TestMCPGateDenialMessages(t *testing.T) {
	s := &Server{}
	editorCall := mcp.GateCall{Kind: "tool", Name: "hypothetical_write_tool", MinRole: auth.RoleEditor}

	err := s.mcpGateDecide(auth.RoleViewer, "u1", editorCall, time.Now())
	if err == nil {
		t.Fatal("viewer editor-tool'u geçmemeliydi")
	}
	for _, want := range []string{"hypothetical_write_tool", "editor", "viewer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("red metni %q içermiyor: %q", want, err)
		}
	}

	if err := s.mcpGateDecide("", "", editorCall, time.Now()); err == nil ||
		!strings.Contains(err.Error(), "authentication required") {
		t.Errorf("kimliksiz çağrı için 'authentication required' beklendi, got %v", err)
	}

	bad := mcp.GateCall{Kind: "tool", Name: "typo_tool", MinRole: "editr"}
	if err := s.mcpGateDecide(auth.RoleAdmin, "u1", bad, time.Now()); err == nil ||
		!strings.Contains(err.Error(), "unrecognized MinRole") {
		t.Errorf("yazım hatalı MinRole için yapılandırma reddi beklendi, got %v", err)
	}
}

// MUTASYON PİNİ — rol SORUSU rate sorusundan ÖNCE gelir. İki yönlü
// assert, çünkü tek yön mutasyonu kaçırıyor:
//
//	(a) rol reddi bütçe TÜKETMEZ — rol bloğunu rate sayacının ALTINA
//	    (b.count++ sonrasına) taşırsan bu yarısı kırmızı;
//	(b) bütçe DOLUYKEN rol reddi hâlâ ROL gerekçesini söyler — rol
//	    bloğunu rate LİMİT kontrolünün altına taşırsan bu yarısı
//	    kırmızı (yetkisiz istemci "rate limited" görür, sebebi
//	    görünmez olur).
func TestMCPGateRoleCheckPrecedesRate(t *testing.T) {
	denied := mcp.GateCall{Kind: "tool", Name: "editor_only", MinRole: auth.RoleEditor}
	open := mcp.GateCall{Kind: "tool", Name: "list_services"}
	at := time.Now()

	t.Run("rol reddi bütçe yakmaz", func(t *testing.T) {
		s := &Server{}
		for i := 0; i < mcpRateLimit+5; i++ {
			if err := s.mcpGateDecide(auth.RoleViewer, "viewer-1", denied, at); err == nil {
				t.Fatalf("çağrı %d reddedilmeliydi", i)
			}
		}
		if err := s.mcpGateDecide(auth.RoleViewer, "viewer-1", open, at); err != nil {
			t.Fatalf("rol reddi bütçe yakmış (sıra ters çevrilmiş): %v", err)
		}
	})

	t.Run("bütçe dolu olsa da gerekçe ROL", func(t *testing.T) {
		s := &Server{}
		for i := 0; i < mcpRateLimit; i++ {
			if err := s.mcpGateDecide(auth.RoleViewer, "viewer-2", open, at); err != nil {
				t.Fatalf("bütçe içi çağrı %d reddedildi: %v", i, err)
			}
		}
		err := s.mcpGateDecide(auth.RoleViewer, "viewer-2", denied, at)
		if err == nil {
			t.Fatal("rol reddi beklendi")
		}
		if strings.Contains(err.Error(), "rate limited") {
			t.Fatalf("rate sorusu rolün ÖNÜNE geçmiş: %v", err)
		}
		if !strings.Contains(err.Error(), auth.RoleEditor) {
			t.Fatalf("gerekçe rolü söylemeli: %v", err)
		}
	})
}

// Rate limiti geçen çağrılar için hâlâ ısırıyor (v0.9.14 sözleşmesi
// gevşemedi) ve kimlik başına ayrı bütçe.
func TestMCPGateRateStillBitesPerIdentity(t *testing.T) {
	s := &Server{}
	at := time.Now()
	open := mcp.GateCall{Kind: "tool", Name: "list_services"}

	for i := 0; i < mcpRateLimit; i++ {
		if err := s.mcpGateDecide(auth.RoleViewer, "a", open, at); err != nil {
			t.Fatalf("bütçe içi çağrı %d reddedildi: %v", i, err)
		}
	}
	err := s.mcpGateDecide(auth.RoleViewer, "a", open, at)
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("bütçe aşımı 'rate limited' vermeliydi, got %v", err)
	}
	if err := s.mcpGateDecide(auth.RoleViewer, "b", open, at); err != nil {
		t.Fatalf("ikinci kimliğin bütçesi ayrı olmalı: %v", err)
	}
}

// Kimliksiz context = red. mcpCallGate'in context okuma yolunu
// (auth.FromContext) gerçek entry point üzerinden pinler.
func TestMCPCallGateMissingClaimsDenied(t *testing.T) {
	s := &Server{}
	err := s.mcpCallGate(context.Background(), mcp.GateCall{Kind: "tool", Name: "list_services"})
	if err == nil || !strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("claims'siz çağrı reddedilmeliydi, got %v", err)
	}
}

// Spec filtresi — property: süzülmüş kümede rolü AŞAN hiçbir tool
// kalmaz; admin her şeyi görür; viewer tam olarak MinRole>"" olanları
// kaybeder. Gerçek kayıt defteri + sentetik editor/admin tool'la
// koşar (bugün 33 tool'un tamamı "" olduğu için sentetik olmadan
// property boş yere yeşil kalırdı).
func TestToolsForRoleFiltersByMinRole(t *testing.T) {
	registry := append(mcptools.ToolList(mcptools.Deps{}),
		mcp.Tool{Name: "synthetic_editor_tool", MinRole: auth.RoleEditor},
		mcp.Tool{Name: "synthetic_admin_tool", MinRole: auth.RoleAdmin},
	)

	for _, role := range []string{auth.RoleViewer, auth.RoleEditor, auth.RoleAdmin, "db-team", ""} {
		got := toolsForRole(registry, role)
		for _, tool := range got {
			if !roleSatisfies(role, tool.MinRole) {
				t.Fatalf("rol %q için sızan tool: %s (MinRole %q)", role, tool.Name, tool.MinRole)
			}
		}
		names := map[string]bool{}
		for _, tool := range got {
			names[tool.Name] = true
		}
		switch role {
		case auth.RoleAdmin:
			if len(got) != len(registry) {
				t.Errorf("admin tüm tool'ları görmeli: %d/%d", len(got), len(registry))
			}
		case auth.RoleEditor:
			if names["synthetic_admin_tool"] || !names["synthetic_editor_tool"] {
				t.Errorf("editor kümesi yanlış: %v", names)
			}
		case auth.RoleViewer, "db-team":
			if names["synthetic_editor_tool"] || names["synthetic_admin_tool"] {
				t.Errorf("%s kümesinde yükseltilmiş tool var: %v", role, names)
			}
			if len(got) != len(registry)-2 {
				t.Errorf("%s N-2 tool görmeli: %d/%d", role, len(got), len(registry))
			}
		case "":
			if len(got) != 0 {
				t.Errorf("kimliksiz hiçbir tool görmemeli: %d", len(got))
			}
		}
	}
}

// Bugünkü duruş: 33 tool'un tamamı MinRole "" (salt-okunur + REST eşi
// viewer'a açık). Bir tool'a MinRole eklendiğinde bu test bilinçli
// olarak kırmızı yanar — REST eşinin kapısıyla eşleştiğini doğrula ve
// listeyi güncelle (mcptools/tools.go başlığındaki sözleşme).
//
// KAPSAM ÇİVİSİ (v0.9.1146): sayım da burada. Yeni bir tool eklenip bu
// dosya hiç açılmazsa yukarıdaki "hepsi viewer" cümlesi TARAMA olarak
// hâlâ yeşil kalır ama İNCELEME yapılmamış olur — sayı, yeni tool'un
// REST eşinin kapısına bakılmasını ZORUNLU kılan tek sinyal.
// mcptools/discovery_test.go'daki sayım kardeşi (aynı sayı, ayrı paket).
func TestAllShippedToolsAreViewerLevel(t *testing.T) {
	tools := mcptools.ToolList(mcptools.Deps{})
	// v0.9.1227 — 33: get_operation_health'in REST eşi GET /api/endpoints
	// kapısız (viewer-açık, api.go:690), MinRole "" ona eşit.
	// v0.9.1233 — 34: get_exception_samples'ın REST eşi
	// GET /api/exception-groups/{fp}/samples de kapısız (api.go:850;
	// aynı ailenin YAZMA uçları RequireAnyRole(editorRoles) ile sarılı,
	// tool yalnız okuma yapıyor), MinRole "" ona eşit.
	// v0.9.1244 — 36: list_teams'in REST eşi GET /api/services-metadata
	// (api.go:726) ve get_team_services'inki GET /api/services?ownerTeam=…
	// (api.go:576) de kapısız — takım atamaları Service Catalog'da her
	// role zaten görünür; ikisi de MinRole "". Takım AYARLARI (PUT
	// /api/settings/team-aliases) admin-kapılı KALDI: tool'lar alias
	// tablosunu bir çözümleyici olarak kullanıyor, İÇERİĞİNİ yayınlamıyor.
	// v0.10.468 — 36 → 39: entity_catalog.go (list_namespaces / list_workloads /
	// list_pods); REST eşleri GET /api/entities, /api/entity/services,
	// /api/services/{name}/pods — RequireRole'süz (viewer tabanı) → MinRole "".
	// v0.10.469 — 39 → 40: resolve_entity (katalog okuması, viewer).
	// v0.10.472 — 40 → 42: describe_attributes / find_attribute_by_value (REST eşleri
	// /api/attribute-keys, /api/services/{name}/attrs viewer).
	// v0.10.474 — 42 → 43: trace_stats (REST eşi /api/traces/aggregate viewer).
	if len(tools) != 43 {
		t.Errorf("katalog %d tool (43 bekleniyordu) — yeni tool'un REST eşinin kapısını (auth.RequireRole/"+
			"RequireAnyRole) kontrol et, MinRole'ü ona eşitle, sonra bu sayıyı güncelle", len(tools))
	}
	for _, tool := range tools {
		if tool.MinRole != "" {
			t.Errorf("%s MinRole=%q — REST eşinin kapısıyla eşleştiğini doğrula, sonra bu testi güncelle",
				tool.Name, tool.MinRole)
		}
	}
}

// MUTASYON PİNİ (spec filtresi) — copilot_chat.go tool kümesini role
// göre süzmeyi bırakırsa (toolsForRole çağrısı kalkarsa) burası
// kırmızı. Kaynak pini: filtre, spec listesi VE byName yürütme
// haritasının ikisini de beslemeli — yalnız specleri gizlemek modelin
// adı tahmin edip çağırmasını engellemez.
func TestChatSpecFilterWiredToRole(t *testing.T) {
	b, err := os.ReadFile("copilot_chat.go")
	if err != nil {
		t.Fatalf("copilot_chat.go okunamadı: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "toolsForRole(mcptools.ToolList(") {
		t.Error("sohbet tool kümesi role göre SÜZÜLMÜYOR — toolsForRole(mcptools.ToolList(...), role) bekleniyor")
	}
	idxFilter := strings.Index(src, "toolsForRole(")
	idxByName := strings.Index(src, "byName[t.Name] = t.Handler")
	if idxFilter < 0 || idxByName < 0 || idxFilter > idxByName {
		t.Error("filtre byName yürütme haritasından ÖNCE uygulanmalı (gizlemek tek başına yetmez)")
	}
}

// A7 kaynak pini — REST ucu viewer'a indi ve silence YAZMALARI
// editor-kapılı KALDI. İkisi bir arada: sapma tablosu sıfır ve
// invariant #7 (viewer görür, yazmaz) bozulmadı.
func TestAnomaliesActiveIsViewerReadableSilenceWritesGated(t *testing.T) {
	b, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatalf("api.go okunamadı: %v", err)
	}
	var activeLine string
	writes := map[string]string{
		`"POST   /api/anomalies/silences"`:             "",
		`"DELETE /api/anomalies/silences/{id}"`:        "",
		`"POST   /api/anomalies/silences/bulk-delete"`: "",
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, `"GET /api/anomalies/active"`) {
			activeLine = line
		}
		for pat := range writes {
			if strings.Contains(line, pat) {
				writes[pat] = line
			}
		}
	}
	if activeLine == "" {
		t.Fatal("GET /api/anomalies/active route'u bulunamadı")
	}
	if strings.Contains(activeLine, "auth.RequireRole") || strings.Contains(activeLine, "auth.RequireAnyRole") {
		t.Errorf("A7: /api/anomalies/active viewer-okunur olmalı (silence YAZMASI ayrı kapılı): %s",
			strings.TrimSpace(activeLine))
	}
	for pat, line := range writes {
		if line == "" {
			t.Errorf("%s route'u bulunamadı", pat)
			continue
		}
		if !strings.Contains(line, "auth.RequireAnyRole(editorRoles") {
			t.Errorf("silence yazması editor-kapılı KALMALI (A7 gerekçesinin dayanağı): %s",
				strings.TrimSpace(line))
		}
	}
}
