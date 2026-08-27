package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/appschema"
	"github.com/cilcenk/coremetry/internal/auth"
)

// schema_catalog.go — Settings → Kod entegrasyonu → Şema kataloğu
// (v0.10.115, spec Dilim D). Kendi dosyası, kendi registerXxxRoutes'u
// (/api-route): api.go tek satır büyür.
//
// Üç uç, hepsi admin:
//
//	GET    /api/settings/schema-catalog  → özet (tablo/kolon sayısı, tarih,
//	                                        flavor başına SnapshotSQL) — içerik DÖNMEZ
//	PUT    /api/settings/schema-catalog  → {csv, source?, flavor?} → ayrıştır,
//	                                        kaydet, audit; özet döner
//	DELETE /api/settings/schema-catalog  → temizle, audit
//
// Katalog SALT-OKUNUR anlık görüntüdür (appschema paket yorumu): canlı
// DB bağlantısı yok, sürücü yok.

func (s *Server) registerSchemaCatalogRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/settings/schema-catalog",
		auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.getSchemaCatalog)))
	mux.Handle("PUT /api/settings/schema-catalog",
		auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.putSchemaCatalog)))
	mux.Handle("DELETE /api/settings/schema-catalog",
		auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.deleteSchemaCatalog)))
}

// schemaCatalogInput — PUT gövdesi. CSV metin olarak (dosya seçici de
// ekranda okuyup metin gönderir); 64 MB tavan appschema.ParseCSV'de.
type schemaCatalogInput struct {
	CSV    string `json:"csv"`
	Source string `json:"source"`
	Flavor string `json:"flavor"`
}

func (s *Server) getSchemaCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.schema.Summary())
}

func (s *Server) putSchemaCatalog(w http.ResponseWriter, r *http.Request) {
	if s.schema == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "şema kataloğu servisi yok")
		return
	}
	var in schemaCatalogInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 72<<20)).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "gövde ayrıştırılamadı: "+err.Error())
		return
	}
	cat, err := appschema.ParseCSV(strings.NewReader(in.CSV))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "CSV: "+err.Error())
		return
	}
	cat.ImportedAt = time.Now().UnixMilli()
	cat.Source = strings.TrimSpace(in.Source)
	cat.Flavor = strings.ToLower(strings.TrimSpace(in.Flavor))
	if err := s.schema.SavePersisted(r.Context(), s.store, cat); err != nil {
		writeErr(w, err)
		return
	}
	tables, cols := cat.Count()
	// Audit: sayılar ve kaynak — kolon adları/tipleri izde DEĞİL (o
	// bilgi kataloğun kendisi; iz "kim, ne zaman, ne kadar"ı tutar).
	s.audit(r, "schema_catalog.import", "settings", "schema_catalog",
		fmt.Sprintf(`{"tables":%d,"columns":%d,"flavor":%q,"source":%q}`, tables, cols, cat.Flavor, cat.Source))
	writeJSON(w, s.schema.Summary())
}

func (s *Server) deleteSchemaCatalog(w http.ResponseWriter, r *http.Request) {
	if s.schema == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "şema kataloğu servisi yok")
		return
	}
	if err := s.schema.SavePersisted(r.Context(), s.store, appschema.Catalog{}); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "schema_catalog.clear", "settings", "schema_catalog", `{}`)
	writeJSON(w, s.schema.Summary())
}

// schemaEvidence — bir Explain için şema bloğu + maske özeti (v0.10.115).
// Kaynaklar, öncelik sırasıyla: (1) hata span'larının db_statement'ı,
// (2) kod bağlamındaki mapper statement bloğu; ikisi de yoksa ama hata
// metninde DB sinyali varsa yalnız sinyal satırı (dürüst: tablo yok).
// Katalog yüklü değilse ve sinyal yoksa boş.
type schemaEvidence struct {
	Block   string // gerçek prompt'a giden bölüm
	Summary string // ai_calls kopyasına giden özet
	Columns int    // span attribute'u için
	Signal  bool
}

// schemaSectionBudget — şema bölümü rune tavanı (kod 4000 > şema 800 >
// SQL(kod penceresi içinde) > log). Operatör direktifi 2026-08-28.
const schemaSectionBudget = 800

func (s *Server) buildSchemaEvidence(errText string, dbStatements []string, mapperBlocks []string) schemaEvidence {
	var out schemaEvidence
	sig, hasSig := appschema.SQLErrorSignal(errText)
	cat := s.schema.Current()
	if !hasSig && cat.Empty() {
		return out
	}
	out.Signal = hasSig
	// Hedefler: önce db_statement (gerçek çalışan SQL), sonra mapper bloğu.
	var tg appschema.Targets
	for _, src := range append(append([]string{}, dbStatements...), mapperBlocks...) {
		t := appschema.TargetsOf(src)
		if len(t.Tables) == 0 {
			continue
		}
		tg.Tables = append(tg.Tables, t.Tables...)
		tg.Columns = append(tg.Columns, t.Columns...)
		if len(tg.Tables) >= 4 {
			break
		}
	}
	cols, dropped := appschema.Lookup(cat, tg)
	// Sinyal yoksa ve kolon da yoksa gönderecek bir şey yok — "DB hatası
	// değil" durumunda şema bölümü gürültü olur.
	if !hasSig && len(cols) == 0 {
		return out
	}
	var sp *appschema.Signal
	if hasSig {
		sp = &sig
	}
	imported := ""
	if cat.ImportedAt > 0 {
		imported = time.UnixMilli(cat.ImportedAt).UTC().Format("2006-01-02")
	}
	block, shown := appschema.PromptSection(sp, cols, dropped, imported, schemaSectionBudget)
	out.Block = block
	out.Summary = appschema.MaskSummary(cols, shown)
	if out.Summary == "" && hasSig {
		out.Summary = "\n\n[şema: sinyal " + sig.SQLCode + sig.SQLState + ", kolon yok]"
	}
	out.Columns = shown
	return out
}
