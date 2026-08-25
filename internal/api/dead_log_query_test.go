package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// dead_log_query_test.go — v0.9.1384.
//
// Kayıtlı-arama alarmı, koştuğu arka ucun ANLAMADIĞI bir yazımla
// kaydedilemez. Gerekçe ölçülmüş: ClickHouse (varsayılan arka uç)
// Search alanını gövdede arıyor, dolayısıyla `service.name:"x"` yapısal
// olarak eşleşemez (aynı servis: yapısal filtre 858 satır, bu yazım 0).
// Böyle bir kural daima 0 sayar, eşiği asla aşmaz, hata da vermez.
//
// Bu dosya KARAR TABLOSUNU çiviliyor. Handler'ın kendisi HTTP + store
// gerektiriyor; çivilenmesi gereken şey ise "hangi (yazım, arka uç)
// çiftinde reddedilir" — o saf ve burada.

// shouldReject — handler'ın kararının saf hâli. Handler bu üçlüyü
// kullanıyor: boş sorgu → geç, arka uç anlıyor → geç, aksi hâlde reddet.
func shouldReject(logQuery, backend string) bool {
	q := strings.TrimSpace(logQuery)
	if q == "" {
		return false
	}
	return logstore.LooksLikeFieldQuery(q) && !logstore.BackendUnderstandsFieldQuery(backend)
}

func TestDeadLogQueryDecisionTable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		query   string
		backend string
		reject  bool
	}{
		// ── reddedilmesi gerekenler ───────────────────────────────────
		{"CH + servis kapsamı (şerhin önerdiği yazım)", `service.name:"checkout"`, "clickhouse", true},
		{"CH + seviye alanı", `level:error`, "clickhouse", true},
		{"CH + bileşik ifadenin içinde alan", `"disk full" AND service.name:"db"`, "clickhouse", true},

		// ── geçmesi gerekenler ────────────────────────────────────────
		{"CH + düz metin ÇALIŞIR", `OutOfMemoryError`, "clickhouse", false},
		{"CH + tırnaklı ifade ÇALIŞIR", `"no space left on device"`, "clickhouse", false},
		{"ES + alan yazımı GERÇEKTEN çalışır", `service.name:"checkout"`, "elasticsearch", false},
		{"boş sorgu — log alarmı değil", ``, "clickhouse", false},
		{"yalnız boşluk", `   `, "clickhouse", false},

		// ── ayırt edici vaka ──────────────────────────────────────────
		// Aynı sorgu, farklı arka uç → farklı karar. Kapı arka uca
		// BAKMAZSA ES kurulumlarında geçerli kuralları reddeder; yazıma
		// bakmazsa CH'de ölü kuralları geçirir. İkisi de tek başına
		// yanlış.
		{"aynı sorgu ES'te geçer", `level:error`, "elasticsearch", false},
		{"aynı sorgu CH'de reddedilir", `level:error`, "clickhouse", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldReject(tc.query, tc.backend); got != tc.reject {
				t.Errorf("shouldReject(%q, %q) = %v; want %v", tc.query, tc.backend, got, tc.reject)
			}
		})
	}
}

// TestBothSaveHandlersAreGuarded — create VE update.
//
// Yalnız create'i kapatmak, kuralı önce boş LogQuery ile kaydedip sonra
// düzenleyerek geçilebilir bir kapı olurdu — ve o yol bir kaçamak değil,
// düzenlemenin NORMAL akışıdır. Kaynak taraması, çünkü korunan şey iki
// handler'ın DAVRANIŞI ve ikisi de HTTP+store gerektiriyor.
func TestBothSaveHandlersAreGuarded(t *testing.T) {
	// Depodaki mevcut yardımcı — şerhleri DÜŞÜRÜYOR. Bu şart: bu
	// dosyanın ve api.go'nun yorumları muhafızın adını anlatıyor ve ham
	// metne bakan bir sayım kendi dokümantasyonunu sayardı.
	src := readAPISourceNoComments(t, "api.go")
	if n := strings.Count(src, "s.rejectDeadLogQuery(w, rule)"); n != 2 {
		t.Errorf("rejectDeadLogQuery çağrısı %d yerde; create ve update olmak üzere 2 bekleniyor", n)
	}
	for _, h := range []string{"func (s *Server) createAlertRule", "func (s *Server) updateAlertRule"} {
		i := strings.Index(src, h)
		if i < 0 {
			t.Fatalf("%s bulunamadı — handler yeniden adlandırıldıysa bu kapıyı da güncelle", h)
		}
		// Handler gövdesinin makul bir dilimi: sonraki UpsertAlertRule'a
		// kadar. Muhafız o çağrıdan ÖNCE gelmeli, yoksa kural zaten
		// yazılmış olur.
		body := src[i:]
		up := strings.Index(body, "s.store.UpsertAlertRule")
		if up < 0 {
			t.Fatalf("%s içinde UpsertAlertRule bulunamadı", h)
		}
		if !strings.Contains(body[:up], "rejectDeadLogQuery") {
			t.Errorf("%s: muhafız UpsertAlertRule'dan ÖNCE çağrılmıyor — ölü kural yazılıp sonra reddedilir", h)
		}
	}
}
