package otlp

import (
	"strings"
	"testing"

	tracecollpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.628 — semconv sözlüğü 2023'te donmuştu: convertSpan tipli
// kolonları yalnız http.method / http.status_code / db.system /
// db.statement'tan okuyordu. OTel bu dördünü ≥1.23'te yeniden
// adlandırdı ve modern SDK'lar YALNIZ yeni adı basıyor — o servislerin
// http_method / http_status / db_system / db_statement kolonları boş
// kalıyor, üzerlerine kurulu her şey (endpoint listeleri, DB özetleri,
// HTTP hata sınıflandırması) onları hiç görmüyordu.
//
// Emsal: v0.9.71, aynı sınıf, tek alan (http.target → url.path).

func spanWithAttrs(t *testing.T, attrs ...*commonpb.KeyValue) *chstore.Span {
	t.Helper()
	rows, _ := ConvertTraces(&tracecollpb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{kvStr("service.name", "svc")},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId:           make([]byte, 16),
					SpanId:            make([]byte, 8),
					Name:              "op",
					StartTimeUnixNano: 1_700_000_000_000_000_000,
					EndTimeUnixNano:   1_700_000_000_010_000_000,
					Attributes:        attrs,
				}},
			}},
		}},
	})
	if len(rows) != 1 {
		t.Fatalf("tek span bekleniyordu, alınan %d", len(rows))
	}
	return rows[0]
}

func TestModernSemconvReachesTypedColumns(t *testing.T) {
	// YALNIZ yeni adlar — modern SDK'nın bastığı hâl.
	got := spanWithAttrs(t,
		kvStr("http.request.method", "POST"),
		kvInt("http.response.status_code", 503),
		kvStr("db.system.name", "postgresql"),
		kvStr("db.query.text", "SELECT 1"),
	)
	if got.HTTPMethod != "POST" {
		t.Errorf("http.request.method → http_method: %q", got.HTTPMethod)
	}
	if got.HTTPStatus != 503 {
		t.Errorf("http.response.status_code → http_status: %d", got.HTTPStatus)
	}
	if got.DBSystem != "postgresql" {
		t.Errorf("db.system.name → db_system: %q", got.DBSystem)
	}
	if got.DBStatement != "SELECT 1" {
		t.Errorf("db.query.text → db_statement: %q", got.DBStatement)
	}
}

func TestLegacySemconvStillWorks(t *testing.T) {
	// Eski adlar YAŞAMAYA devam etmeli: prod'daki mevcut ajanların
	// çoğu hâlâ bunları basıyor ve bir "modernleşme" onları kırarsa
	// düzeltme, düzelttiğinden fazlasını bozar.
	got := spanWithAttrs(t,
		kvStr("http.method", "GET"),
		kvInt("http.status_code", 200),
		kvStr("db.system", "oracle"),
		kvStr("db.statement", "SELECT 2"),
	)
	if got.HTTPMethod != "GET" || got.HTTPStatus != 200 ||
		got.DBSystem != "oracle" || got.DBStatement != "SELECT 2" {
		t.Fatalf("eski semconv kırıldı: %+v", got)
	}
}

func TestNewSpellingWinsWhenBothPresent(t *testing.T) {
	got := spanWithAttrs(t,
		kvStr("http.method", "GET"), kvStr("http.request.method", "POST"),
		kvInt("http.status_code", 200), kvInt("http.response.status_code", 503),
	)
	if got.HTTPMethod != "POST" || got.HTTPStatus != 503 {
		t.Fatalf("iki ad birden varken YENİ kazanmalı: %q %d", got.HTTPMethod, got.HTTPStatus)
	}
}

// Boş DEĞER "yok" sayılmalı: bazı otomatik enstrümantasyonlar yeni
// anahtarı boş basıyor. "Anahtar var" ile "değer var" farklı şeyler —
// bu oturumda tam bu ayrımı bir kez kaçırdım (terfi kolonunun VAR
// olduğunu DOLU olduğu sandım).
func TestEmptyNewValueFallsBackToLegacy(t *testing.T) {
	got := spanWithAttrs(t,
		kvStr("http.request.method", ""),
		kvStr("http.method", "GET"),
		kvStr("db.query.text", ""),
		kvStr("db.statement", "SELECT 3"),
	)
	if got.HTTPMethod != "GET" {
		t.Errorf("boş yeni değer eski ada düşmeli: %q", got.HTTPMethod)
	}
	if got.DBStatement != "SELECT 3" {
		t.Errorf("boş yeni değer eski ada düşmeli: %q", got.DBStatement)
	}
}

// EN ÖNEMLİ TEST — "bir kural üç yere bölünüp ayrışıyor" sınıfı.
//
// Bir yazımı INGEST tanıyor da FİLTRE tanımıyorsa, operatör modern adı
// yazdığında sorgu indeksli kolonu kaçırıp dizi aramasına düşer (ve
// dizide o anahtar YOK, çünkü ingest onu kolona taşıdı) — yani sessizce
// SIFIR sonuç. Aynısı gösterim yolu için de geçerli.
func TestEverySpellingIsKnownToFilterAndDisplay(t *testing.T) {
	for col, spellings := range spanAttrAliases {
		for _, key := range spellings {
			// Filtre yolu: indeksli kolona yönlenmeli, diziye DEĞİL.
			sql, _, err := chstore.FilterExpr{Key: key, Op: "=", Values: []string{"x"}}.SQL()
			if err != nil {
				t.Fatalf("%s: %v", key, err)
			}
			if strings.Contains(sql, "indexOf(attr_keys") {
				t.Errorf("filtre %q'yi tanımıyor — dizi aramasına düşüyor (ingest onu %s kolonuna taşıdı, dizide YOK): %s",
					key, col, sql)
			}
			// Gösterim yolu.
			if _, ok := chstore.WellKnownTraceCol[key]; !ok {
				t.Errorf("WellKnownTraceCol %q'yi tanımıyor (kolon %s)", key, col)
			}
		}
	}
}
