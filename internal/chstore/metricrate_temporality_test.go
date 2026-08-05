package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.9.669 — temporality PROBU, sorgunun baktığı satır kümesine bakmalı.
//
// v0.9.668'de metrik throughput'un `job` yolunu Service'siz kurdum;
// prob da Service'siz çalışınca any(temporality) TÜM servislere baktı.
// http.server.duration yerel veride KARIŞIK (6 servis delta, 1 servis
// cumulative), yani prob yanlış tarafı seçtiğinde diğer grup ya şişiyor
// ya çöp dönüyordu — üstelik any() deterministik olmadığı için grafik
// bir gün kendiliğinden yanlışa geçebilirdi.

func probeArgs(t *testing.T) (time.Time, time.Time) {
	t.Helper()
	to := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	return to.Add(-time.Hour), to
}

// ASIL KAPI: filtreler proba İNMELİ.
func TestTemporalityProbeAppliesFilters(t *testing.T) {
	from, to := probeArgs(t)
	f := []FilterExpr{{Key: "job", Op: "=~", Values: []string{"^(.*/)?checkout$"}}}

	withF := temporalityProbeWhere("http.server.duration", "", from, to, f)
	without := temporalityProbeWhere("http.server.duration", "", from, to, nil)

	if withF.sql() == without.sql() {
		t.Fatal("filtre proba inmiyor — prob tüm servislere bakar ve karışık temporality'de yanlış dalı seçer")
	}
	if len(withF.args) <= len(without.args) {
		t.Error("filtre bind argümanı eklemedi")
	}
}

// Service kapsamı korunmalı (mevcut çağıranların davranışı).
func TestTemporalityProbeScopesByService(t *testing.T) {
	from, to := probeArgs(t)
	scoped := temporalityProbeWhere("m", "checkout", from, to, nil)
	if !strings.Contains(scoped.sql(), "service_name = ?") {
		t.Error("service kapsamı düştü")
	}
	bare := temporalityProbeWhere("m", "", from, to, nil)
	if strings.Contains(bare.sql(), "service_name") {
		t.Error("servissiz çağrıda service_name yüklemi olmamalı")
	}
}

// Pencere ve metrik adı HER ZAMAN bağlı olmalı: probun kendisi de
// metric_points'e giden bir sorgu, zaman-sınırsız çalışamaz.
func TestTemporalityProbeAlwaysBounded(t *testing.T) {
	from, to := probeArgs(t)
	wc := temporalityProbeWhere("http.server.duration", "", from, to, nil)
	sql := wc.sql()
	for _, want := range []string{"metric = ?", "time >= ?", "time <= ?"} {
		if !strings.Contains(sql, want) {
			t.Errorf("prob %q taşımıyor: %s", want, sql)
		}
	}
	// İlk arg metrik adı olmalı — bind sırası bozulursa sorgu sessizce
	// başka bir metriği prob'lar.
	if len(wc.args) < 3 || wc.args[0] != "http.server.duration" {
		t.Errorf("bind sırası bozuk: %v", wc.args)
	}
}
