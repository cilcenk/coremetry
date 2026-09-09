package api

// oracle_routes_test.go — v0.10.580, Oracle AŞAMA 1.
//
// Üç sözleşme HTTP düzeyinde çivileniyor:
//
//  1. Bağlantı testinin BAŞARISIZLIĞI 200 + ok:false. 5xx dönmek iki şeyi
//     birden bozardı: form hatayı gösteremez (fetch reject) ve Coremetry
//     kendi error_rate anomalisini tetikler (v0.7.13).
//  2. PUT bir audit satırı bırakır (CLAUDE.md "admin write = audit entry")
//     ve satırın details'ı ŞİFRE TAŞIMAZ.
//  3. Rol kapıları kayıt satırında: ayar uçları admin, durum ucu her rol
//     (viewer state'i GÖRMELİ, boş sayfa değil).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/oracle"
)

func oracleTestServer(t *testing.T) (*Server, *http.ServeMux) {
	t.Helper()
	c, _ := cache.NewNoop()
	s := &Server{cache: c}
	s.oracle = oracle.New()
	s.auditQ = make(chan chstore.AuditEntry, 8)
	mux := http.NewServeMux()
	s.registerOracleRoutes(mux)
	return s, mux
}

func asRole(r *http.Request, role string) *http.Request {
	return r.WithContext(auth.ContextWithClaims(r.Context(),
		&auth.Claims{UserID: "u1", Email: "admin@example.test", Role: role}))
}

func oracleSourceJSON(extra string) string {
	return `{"name":"core-oracle","host":"db.example.local","serviceName":"ORCLPDB",
	"user":"coremetry","schema":"APPOWNER","table":"ERROR_LOG"` + extra + `}`
}

// ── 1. test ucu: başarısızlık 200 + ok:false ────────────────────

func TestOracleTestEndpoint_FailureIs200WithOKFalse(t *testing.T) {
	_, mux := oracleTestServer(t)

	cases := []struct {
		name string
		body string
	}{
		// Şifre referansı çözülemiyor: ağa HİÇ çıkılmaz, cevap yine 200.
		{"çözülemeyen passwordRef", oracleSourceJSON(`,"passwordRef":"env:COREMETRY_ORACLE_PW_YOK_12345"`)},
		// Doğrulama hatası da operatörün sorusuna geçerli bir cevaptır.
		{"geçersiz şema adı", `{"name":"x","host":"h","serviceName":"s","user":"u","password":"p","schema":"A B","table":"T"}`},
		{"şifre yok", `{"name":"x","host":"h","serviceName":"s","user":"u","schema":"A","table":"T"}`},
		{"host yok", `{"name":"x","user":"u","password":"p","schema":"A","table":"T"}`},
		{"extraWhere yorumlu", oracleSourceJSON(`,"password":"p","extraWhere":"1=1 --"`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := asRole(httptest.NewRequest("POST", "/api/settings/oracle/test", strings.NewReader(c.body)), auth.RoleAdmin)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d, want 200 (gövde: %s)", w.Code, w.Body.String())
			}
			var res oracle.TestResult
			if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
				t.Fatalf("cevap JSON değil: %v — %s", err, w.Body.String())
			}
			if res.OK {
				t.Fatalf("ok:true dönmemeliydi: %s", w.Body.String())
			}
			if res.Error == "" {
				t.Fatal("hata metni boş — form ne olduğunu söyleyemez")
			}
			if res.Columns == nil {
				t.Error("columns null döndü; frontend .map()'liyor, [] olmalı")
			}
		})
	}

	// Bozuk JSON gövdesi TEK istisna: 400.
	req := asRole(httptest.NewRequest("POST", "/api/settings/oracle/test", strings.NewReader("{bozuk")), auth.RoleAdmin)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bozuk gövde status %d, want 400", w.Code)
	}
}

// Servis hiç yoksa 503 — sessiz boş cevap değil.
func TestOracleRoutes_ServiceUnavailable(t *testing.T) {
	c, _ := cache.NewNoop()
	s := &Server{cache: c}
	mux := http.NewServeMux()
	s.registerOracleRoutes(mux)
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/settings/oracle", ""},
		{"PUT", "/api/settings/oracle", `{"sources":[]}`},
		{"POST", "/api/settings/oracle/test", `{}`},
		{"GET", "/api/oracle/status", ""},
	} {
		req := asRole(httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)), auth.RoleAdmin)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s status %d, want 503", tc.method, tc.path, w.Code)
		}
	}
}

// ── 2. PUT → audit + şifre maskesi ──────────────────────────────

func TestOraclePut_AuditsAndNeverEchoesPassword(t *testing.T) {
	s, mux := oracleTestServer(t)
	body := `{"sources":[` + oracleSourceJSON(`,"password":"s3cret","enabled":true`) + `]}`
	req := asRole(httptest.NewRequest("PUT", "/api/settings/oracle", strings.NewReader(body)), auth.RoleAdmin)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	// Cevap = snapshot: şifre GERİ VERİLMEZ.
	if strings.Contains(w.Body.String(), "s3cret") {
		t.Fatalf("PUT cevabı şifreyi geri verdi: %s", w.Body.String())
	}
	var snap oracle.Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil {
		t.Fatalf("cevap JSON değil: %v", err)
	}
	if len(snap.Sources) != 1 || !snap.Sources[0].HasPassword || snap.Sources[0].ID == "" {
		t.Fatalf("snapshot: %+v", snap.Sources)
	}
	// Ayar canlıya alınmış olmalı (store yok, bellek takası var).
	if got := s.oracle.CurrentSettings().Sources[0].Password; got != "s3cret" {
		t.Fatalf("saklı şifre: %q", got)
	}

	select {
	case e := <-s.auditQ:
		if e.Action != "settings.oracle.update" || e.TargetKind != "settings" || e.TargetID != "oracle_sources" {
			t.Fatalf("audit satırı: %+v", e)
		}
		if strings.Contains(e.Details, "s3cret") {
			t.Fatalf("audit details şifre taşıyor: %s", e.Details)
		}
		if !strings.Contains(e.Details, "core-oracle") {
			t.Errorf("audit details kaynak adını söylemeli: %s", e.Details)
		}
	default:
		t.Fatal("audit satırı yazılmadı — CLAUDE.md: admin write = audit entry")
	}

	// Doğrulama hatası 400 ve audit YOK (yazım olmadı).
	bad := `{"sources":[{"name":"x","host":"h","serviceName":"s","user":"u","password":"p","schema":"A;B","table":"T","enabled":true}]}`
	req = asRole(httptest.NewRequest("PUT", "/api/settings/oracle", strings.NewReader(bad)), auth.RoleAdmin)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("geçersiz PUT status %d, want 400", w.Code)
	}
	select {
	case e := <-s.auditQ:
		t.Fatalf("reddedilen PUT audit yazdı: %+v", e)
	default:
	}
}

// GET ayarı da şifreyi geri vermez.
func TestOracleGet_MasksPassword(t *testing.T) {
	s, mux := oracleTestServer(t)
	cfg, err := oracle.Normalize(oracle.Settings{Sources: []oracle.SourceConfig{{
		Name: "core-oracle", Host: "db.example.local", ServiceName: "ORCLPDB",
		User: "coremetry", Password: "s3cret", Schema: "APPOWNER", Table: "ERROR_LOG", Enabled: true,
	}}}, oracle.Settings{}, oracle.NewSourceID)
	if err != nil {
		t.Fatal(err)
	}
	s.oracle.Configure(cfg)

	req := asRole(httptest.NewRequest("GET", "/api/settings/oracle", nil), auth.RoleAdmin)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "s3cret") {
		t.Fatalf("GET şifreyi sızdırdı: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"hasPassword":true`) {
		t.Errorf("hasPassword rozeti yok: %s", w.Body.String())
	}
}

// ── 3. rol kapıları ─────────────────────────────────────────────

func TestOracleRoutes_RoleGates(t *testing.T) {
	_, mux := oracleTestServer(t)
	settingsRoutes := []struct{ method, path, body string }{
		{"GET", "/api/settings/oracle", ""},
		{"PUT", "/api/settings/oracle", `{"sources":[]}`},
		{"POST", "/api/settings/oracle/test", `{"name":"x","host":"h","serviceName":"s","user":"u","password":"p","schema":"A B","table":"T"}`},
	}
	for _, rt := range settingsRoutes {
		// Oturum yok → 401.
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body)))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s kimliksiz status %d, want 401", rt.method, rt.path, w.Code)
		}
		// admin OLMAYAN roller → 403.
		for _, role := range []string{auth.RoleViewer, auth.RoleEditor} {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, asRole(httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body)), role))
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s rol %s status %d, want 403", rt.method, rt.path, role, w.Code)
			}
		}
	}

	// Durum ucu her rolde açık — viewer state'i GÖRMELİ.
	for _, role := range []string{auth.RoleViewer, auth.RoleEditor, auth.RoleAdmin} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, asRole(httptest.NewRequest("GET", "/api/oracle/status", nil), role))
		if w.Code != http.StatusOK {
			t.Errorf("status ucu rol %s → %d, want 200", role, w.Code)
		}
		var p oracleStatusPayload
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatalf("durum cevabı JSON değil: %v", err)
		}
		if p.Sources == nil {
			t.Error("sources null döndü; [] olmalı")
		}
	}
}
