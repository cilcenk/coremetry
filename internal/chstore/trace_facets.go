package chstore

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync/atomic"
)

// trace_facets.go — v0.10.302 (docs/audit/trace-attribute-search.md Dilim 2,
// Datadog "facets"): operatör-yönetimli terfi attribute'ları.
//
// Dilim 1'in hash indeksi (attr_kvh + bloom) NADİR/orta kardinaliteli
// değerlerde granül budar; YAYGIN değerde (servisin her span'inde olan
// `channel_code`, `tenant`) budamaz — orası terfi kolonu + set(0) indeksinin
// yeri (CHANNEL_CODE ölçümü: 10M → 1.3M satır, v0.9.623). Bugüne dek terfi
// listesi KODDA sabitti (promoted_attr.go); operatör yeni bir facet'i ancak
// sürümle alıyordu. Şimdi `system_settings['trace_facets']` blobu:
//
//	{"facets":[{"key":"tenant","spellings":["tenant","TENANT_ID"],"scope":"span","type":"lc"}]}
//
// Blob → promotedAttr (kolon `attr_f_<anahtar>`), yerleşik listeyle
// BİRLEŞİR (allPromotedAttrs) ve aynı makineden geçer: boot DDL
// (app-managed; küme kipinde ertelenmiş → iki-boot), probe ("kolon VAR ≠
// DOLU", v0.9.621), filtre/projeksiyon yönlendirmesi. Dış Distributed
// prod'da boot koşmaz: TraceFacetMigrationSQL üretilen ON CLUSTER metnini
// verir (0013 şekli), operatör elle koşar, pod restart.
//
// Sınırlar: en çok 16 facet (her biri boot'ta bir probe sorgusu), anahtar
// ≤ 128, yazım ≤ 8; yerleşik kolon/anahtarla çakışma reddedilir.

const traceFacetsSettingKey = "trace_facets"

const (
	traceFacetsMax     = 16
	traceFacetKeyMax   = 128
	traceFacetSpellMax = 8
	facetColPrefix     = "attr_f_"
	facetColMaxLen     = 48
)

// TraceFacet — operatör girdisi.
type TraceFacet struct {
	Key       string   `json:"key"`
	Spellings []string `json:"spellings,omitempty"` // boş → [Key]
	Scope     string   `json:"scope,omitempty"`     // "span" (varsayılan) | "resource"
	Type      string   `json:"type,omitempty"`      // "lc" (varsayılan) | "string"
}

// TraceFacetSettings — blob.
type TraceFacetSettings struct {
	Facets []TraceFacet `json:"facets"`
}

var facetIdentRe = regexp.MustCompile(`[^a-z0-9]+`)

// FacetColumn — anahtar → kolon adı (deterministik, güvenli tanımlayıcı).
func FacetColumn(key string) string {
	k := strings.ToLower(strings.TrimSpace(key))
	k = facetIdentRe.ReplaceAllString(k, "_")
	k = strings.Trim(k, "_")
	if k == "" {
		return ""
	}
	col := facetColPrefix + k
	if len(col) > facetColMaxLen {
		col = col[:facetColMaxLen]
		col = strings.TrimRight(col, "_")
	}
	return col
}

// NormalizeTraceFacets — boşlukları kırpar, varsayılanları doldurur, doğrular.
func NormalizeTraceFacets(in TraceFacetSettings) (TraceFacetSettings, error) {
	out := TraceFacetSettings{Facets: []TraceFacet{}}
	if len(in.Facets) > traceFacetsMax {
		return out, fmt.Errorf("en çok %d facet (her biri boot'ta bir probe sorgusu)", traceFacetsMax)
	}
	seenCol := map[string]string{}
	seenKey := map[string]bool{}
	builtinCols := map[string]bool{}
	builtinKeys := map[string]bool{}
	for _, a := range promotedAttrs {
		builtinCols[a.col] = true
		for _, k := range a.keys {
			builtinKeys[strings.ToLower(k)] = true
		}
	}
	for i, f := range in.Facets {
		f.Key = strings.TrimSpace(f.Key)
		if f.Key == "" {
			return out, fmt.Errorf("facet %d: anahtar boş", i+1)
		}
		if len(f.Key) > traceFacetKeyMax {
			return out, fmt.Errorf("facet %q: anahtar %d karakterden uzun", f.Key, traceFacetKeyMax)
		}
		if strings.HasPrefix(f.Key, "resource.") || strings.HasPrefix(f.Key, "span.") {
			return out, fmt.Errorf("facet %q: anahtar öneksiz yazılır; kapsamı `scope` belirler", f.Key)
		}
		switch f.Scope {
		case "", "span":
			f.Scope = "span"
		case "resource":
		default:
			return out, fmt.Errorf("facet %q: scope span | resource", f.Key)
		}
		switch f.Type {
		case "", "lc":
			f.Type = "lc"
		case "string":
		default:
			return out, fmt.Errorf("facet %q: type lc | string", f.Key)
		}
		var sp []string
		seenSp := map[string]bool{}
		for _, s := range append([]string{f.Key}, f.Spellings...) {
			s = strings.TrimSpace(s)
			if s == "" || seenSp[s] {
				continue
			}
			if strings.ContainsAny(s, "'\\`\"") {
				return out, fmt.Errorf("facet %q: yazım %q tırnak/ters bölü içeremez", f.Key, s)
			}
			seenSp[s] = true
			sp = append(sp, s)
		}
		if len(sp) > traceFacetSpellMax {
			return out, fmt.Errorf("facet %q: en çok %d yazım", f.Key, traceFacetSpellMax)
		}
		f.Spellings = sp
		lk := strings.ToLower(f.Key)
		if seenKey[lk] {
			return out, fmt.Errorf("facet %q iki kez", f.Key)
		}
		seenKey[lk] = true
		if builtinKeys[lk] {
			return out, fmt.Errorf("facet %q zaten yerleşik terfi kolonu (promoted_attr.go)", f.Key)
		}
		if _, ok := wellKnown[lk]; ok {
			return out, fmt.Errorf("facet %q zaten bir kolon (bilinen alan)", f.Key)
		}
		col := FacetColumn(f.Key)
		if col == "" {
			return out, fmt.Errorf("facet %q: kolon adı türetilemedi", f.Key)
		}
		if other, dup := seenCol[col]; dup {
			return out, fmt.Errorf("facet %q ile %q aynı kolona (%s) düşüyor", f.Key, other, col)
		}
		if builtinCols[col] {
			return out, fmt.Errorf("facet %q: kolon %s yerleşik", f.Key, col)
		}
		seenCol[col] = f.Key
		out.Facets = append(out.Facets, f)
	}
	return out, nil
}

// facetToPromoted — blob girdisi → terfi tanımı (aynı makine).
func facetToPromoted(f TraceFacet) promotedAttr {
	a := promotedAttr{col: FacetColumn(f.Key), keys: f.Spellings, res: f.Scope == "resource"}
	if f.Type == "string" {
		a.typ = "String"
	}
	return a
}

var facetPromotedPtr atomic.Pointer[[]promotedAttr]

// allPromotedAttrs — yerleşik + facet kaydı (her tüketici bunu okur).
func allPromotedAttrs() []promotedAttr {
	out := append([]promotedAttr(nil), promotedAttrs...)
	if p := facetPromotedPtr.Load(); p != nil {
		out = append(out, (*p)...)
	}
	return out
}

// registerTraceFacets — kaydı yayınlar (copy-on-write).
func registerTraceFacets(s TraceFacetSettings) {
	list := make([]promotedAttr, 0, len(s.Facets))
	for _, f := range s.Facets {
		list = append(list, facetToPromoted(f))
	}
	facetPromotedPtr.Store(&list)
}

// LoadTraceFacets — system_settings'ten okur, doğrular, kaydı yayınlar
// (boot + PUT + çapraz-pod yenileme). Bozuk blob: log + eski kayıt kalır.
func (s *Store) LoadTraceFacets(ctx context.Context) error {
	raw, err := s.GetSetting(ctx, traceFacetsSettingKey)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		registerTraceFacets(TraceFacetSettings{})
		return nil
	}
	var in TraceFacetSettings
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("trace_facets decode: %w", err)
	}
	cfg, err := NormalizeTraceFacets(in)
	if err != nil {
		return fmt.Errorf("trace_facets: %w", err)
	}
	registerTraceFacets(cfg)
	return nil
}

// SaveTraceFacets — doğrula + yaz + yayınla.
func (s *Store) SaveTraceFacets(ctx context.Context, in TraceFacetSettings) (TraceFacetSettings, error) {
	cfg, err := NormalizeTraceFacets(in)
	if err != nil {
		return cfg, err
	}
	raw, _ := json.Marshal(cfg)
	if err := s.PutSetting(ctx, traceFacetsSettingKey, raw); err != nil {
		return cfg, err
	}
	registerTraceFacets(cfg)
	return cfg, nil
}

// CurrentTraceFacets — kayıttaki facet'ler (GET).
func CurrentTraceFacets() []TraceFacet {
	p := facetPromotedPtr.Load()
	if p == nil {
		return []TraceFacet{}
	}
	out := make([]TraceFacet, 0, len(*p))
	for _, a := range *p {
		f := TraceFacet{Key: a.keys[0], Spellings: a.keys, Scope: "span", Type: "lc"}
		if a.res {
			f.Scope = "resource"
		}
		if a.typ == "String" {
			f.Type = "string"
		}
		out = append(out, f)
	}
	return out
}

// ApplyTraceFacets — app-managed kurulumda DDL'i hemen dener (küme kipinde
// ertelenir) ve probe ile haritayı tazeler; dış Distributed'da yalnız
// probe (kolonlar 0014-benzeri elle script ile gelir).
func (s *Store) ApplyTraceFacets(ctx context.Context) (bootManaged bool) {
	bootManaged = !s.spansIsExternalDistributed(ctx)
	if bootManaged {
		s.repairPromotedAttrCols(ctx)
	}
	registerTraceAttrMaterialized(s.probePromotedAttrs(ctx))
	return bootManaged
}

// TraceFacetStatus — GET için host-bağımsız durum (bu pod'un gördüğü).
type TraceFacetStatus struct {
	Key          string `json:"key"`
	Column       string `json:"column"`
	ColumnExists bool   `json:"columnExists"`
	IndexExists  bool   `json:"indexExists"`
	// Routed — probe kolonu DOLU gördü ve filtre/projeksiyon ona yönleniyor.
	Routed bool `json:"routed"`
}

func (s *Store) TraceFacetsStatus(ctx context.Context) []TraceFacetStatus {
	cols := promotedCols()
	out := []TraceFacetStatus{}
	for _, f := range CurrentTraceFacets() {
		col := FacetColumn(f.Key)
		_, colExists := s.spansColumnExpr(ctx, col)
		st := TraceFacetStatus{Key: f.Key, Column: col, ColumnExists: colExists, IndexExists: s.spansIndexExists(ctx, "idx_"+col)}
		for _, k := range f.Spellings {
			if cols[k] == col || cols["resource."+k] == col {
				st.Routed = true
			}
		}
		out = append(out, st)
	}
	return out
}

// TraceFacetMigrationSQL — prod (dış Distributed) için ON CLUSTER script
// (0013 şekli): spans_local ADD COLUMN → spans ADD COLUMN → set(0) index;
// küme adı `uptrace_all` token'ı (operatör değiştirir). SAF.
func TraceFacetMigrationSQL(cfg TraceFacetSettings) string {
	var b strings.Builder
	b.WriteString("-- trace facets — Settings → Traces → Facets tarafından üretildi. `uptrace_all` yerine gerçek küme adını yazın.\n")
	b.WriteString("-- Sıra: spans_local ADD COLUMN → spans (Distributed) ADD COLUMN → spans_local ADD INDEX; sonra pod restart (probe).\n")
	for _, f := range cfg.Facets {
		a := facetToPromoted(f)
		expr := promotedAttrExprFor(a)
		for _, tbl := range []string{"spans_local", "spans"} {
			fmt.Fprintf(&b, "ALTER TABLE %s ON CLUSTER uptrace_all\n  ADD COLUMN IF NOT EXISTS %s %s\n  MATERIALIZED %s;\n", tbl, a.col, a.colType(), expr)
		}
		fmt.Fprintf(&b, "ALTER TABLE spans_local ON CLUSTER uptrace_all\n  ADD INDEX IF NOT EXISTS idx_%s %s TYPE set(0) GRANULARITY 4;\n", a.col, a.col)
	}
	if len(cfg.Facets) == 0 {
		b.WriteString("-- (facet yok)\n")
	}
	return b.String()
}

// SpansIsExternalDistributed — API için (facet GET: bootManaged bilgisi).
func (s *Store) SpansIsExternalDistributed(ctx context.Context) bool {
	return s.spansIsExternalDistributed(ctx)
}

func init() {
	// Boş kayıt: yerleşik liste tek başına (LoadTraceFacets gelene dek).
	registerTraceFacets(TraceFacetSettings{})
	_ = log.Printf
}
