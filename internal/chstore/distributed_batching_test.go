// v0.9.1076 regresyon testleri — Distributed batching boot geçişinin
// saf çekirdekleri.
//
// Olay (2026-08-16 prod): 31 Temmuz'daki bellek tepesinin şişirdiği
// 3,66M dosyalık spool, bellek düzeldikten sonra bile ~5 dosya/dk
// süründü — gönderici dosyaları tek tek INSERT'liyordu. Düzeltme batch
// modu; operatör konsolu salt-okunur olduğundan geçiş boot'ta otomatik.
package chstore

import (
	"os"
	"strings"
	"testing"
)

func TestDistributedClusterOf(t *testing.T) {
	cases := []struct {
		name, engineFull, want string
	}{
		{
			"tırnaklı küme adı (prod şekli)",
			"Distributed('uptrace_all', 'coremetry', 'metric_points_local', rand())",
			"uptrace_all",
		},
		{
			"çıplak tanımlayıcı",
			"Distributed(uptrace_all, coremetry, metric_points_local, rand())",
			"uptrace_all",
		},
		{
			"backtick'li ad",
			"Distributed(`uptrace_all`, `coremetry`, `spans_local`)",
			"uptrace_all",
		},
		{
			// SETTINGS eki adın ayrıştırılmasını bozmamalı.
			"settings ekiyle",
			"Distributed('c1', 'db', 't_local', rand()) SETTINGS fsync_after_insert = 0",
			"c1",
		},
		{"distributed değil", "MergeTree() ORDER BY time", ""},
		{"tek argüman (virgül yok)", "Distributed('c1')", ""},
		{"boş metin", "", ""},
		{
			// Ayrıştırma artığı tırnak/boşluk taşıyan "ad" SQL'e
			// girmemeli — güvenli taraf boş dönmek (yalnız yerel ALTER).
			"bozuk tırnaklama → boş",
			"Distributed('a b', 'db', 't')",
			"",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := distributedClusterOf(c.engineFull); got != c.want {
				t.Errorf("distributedClusterOf(%q) = %q, beklenen %q", c.engineFull, got, c.want)
			}
		})
	}
}

func TestHasDistributedBatching(t *testing.T) {
	cases := []struct {
		name, engineFull string
		want             bool
	}{
		{
			"yeni ad açık",
			"Distributed('c','d','t') SETTINGS background_insert_batch = 1, background_insert_split_batch_on_failure = 1",
			true,
		},
		{
			"eski ad açık",
			"Distributed('c','d','t') SETTINGS monitor_batch_inserts = 1",
			true,
		},
		{"kapalı (ayarsız)", "Distributed('c','d','t', rand())", false},
		{
			// Açıkça 0'a çekilmişse DOKUNULMAZ değil — geçiş yine açar.
			// (Operatör 0'ı bilinçli istiyorsa engine_full'da '= 1'
			// görünmez ve ALTER yeniden koşar; bilinen sınır, yorum
			// distributed_batching.go başlığında.)
			"açıkça kapatılmış",
			"Distributed('c','d','t') SETTINGS background_insert_batch = 0",
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasDistributedBatching(c.engineFull); got != c.want {
				t.Errorf("hasDistributedBatching(%q) = %v, beklenen %v", c.engineFull, got, c.want)
			}
		})
	}
}

func TestBatchingAlters(t *testing.T) {
	t.Run("küme adıyla 4 deneme, geniş+yeni önce", func(t *testing.T) {
		got := batchingAlters("metric_points", "uptrace_all")
		if len(got) != 4 {
			t.Fatalf("4 deneme beklenirdi, %d geldi: %v", len(got), got)
		}
		// 1. ON CLUSTER + yeni adlar + kısa DDL zamanaşımı (tıkalı
		// kuyruk boot'u 180s×tablo bekletmemeli).
		if !strings.Contains(got[0], "ON CLUSTER `uptrace_all`") ||
			!strings.Contains(got[0], "background_insert_batch = 1") ||
			!strings.Contains(got[0], "distributed_ddl_task_timeout = 20") {
			t.Errorf("ilk deneme ON CLUSTER + yeni ad + ddl timeout olmalı: %s", got[0])
		}
		if !strings.Contains(got[1], "monitor_batch_inserts = 1") {
			t.Errorf("ikinci deneme eski adlar olmalı: %s", got[1])
		}
		// 3-4: yerel düşüşler — ON CLUSTER taşımaz.
		for _, stmt := range got[2:] {
			if strings.Contains(stmt, "ON CLUSTER") {
				t.Errorf("yerel düşüş ON CLUSTER taşımamalı: %s", stmt)
			}
		}
		// Sigorta her denemede: batch açılıyorsa split_batch_on_failure
		// da açılmalı — 241 tekrarında tüm batch .broken'a düşmesin.
		for i, stmt := range got {
			if !strings.Contains(stmt, "split_batch_on_failure = 1") &&
				!strings.Contains(stmt, "monitor_split_batch_on_failure = 1") {
				t.Errorf("deneme %d split_batch_on_failure sigortasız: %s", i, stmt)
			}
		}
	})
	t.Run("küme adı yoksa yalnız yerel 2 deneme", func(t *testing.T) {
		got := batchingAlters("spans", "")
		if len(got) != 2 {
			t.Fatalf("2 deneme beklenirdi, %d geldi: %v", len(got), got)
		}
		for _, stmt := range got {
			if strings.Contains(stmt, "ON CLUSTER") {
				t.Errorf("küme adı yokken ON CLUSTER üretilmemeli: %s", stmt)
			}
		}
	})
}

// ── v0.9.1081 — gerçeklik düzeltmesi ─────────────────────────────────
//
// Canlı doğrulama (lokal CH 24.8, 2026-08-16): Distributed motoru ALTER
// MODIFY SETTING'i topyekûn reddediyor (Code 36) — v0.9.1076'nın dört
// fallback'i de çalışmıyordu ve 30 tablo × 1 yanıltıcı hata satırı
// basılıyordu. Kullanıcı-düzeyi ayar ise 24.8'de varsayılan 1.

func TestIsSettingsChangeUnsupported(t *testing.T) {
	if !isSettingsChangeUnsupported(errStr("code: 36, message: Cannot alter settings, because table engine doesn't support settings changes")) {
		t.Error("Code 36 metni yakalanmalı")
	}
	if isSettingsChangeUnsupported(errStr("code: 36, message: some other bad argument")) {
		t.Error("başka bir Code 36 bu sınıfa GİRMEZ — metin eşleşmesi şart")
	}
	if isSettingsChangeUnsupported(nil) {
		t.Error("nil hata false olmalı")
	}
}

// Kaynak pini: etkin-durum kontrolü ALTER denemelerinden ÖNCE koşmalı
// ve Code 36 döngüyü TEK özetle kesmeli. Bu ikisi gevşerse ya gereksiz
// ALTER yağmuru ya 30 satır yanıltıcı hata geri gelir.
func TestBatchingEffectiveFirstAndCode36Bails(t *testing.T) {
	b, err := os.ReadFile("distributed_batching.go")
	if err != nil {
		t.Fatalf("kaynak okunamadı: %v", err)
	}
	src := string(b)
	effIdx := strings.Index(src, "effectiveBatchingSQL).Scan")
	altIdx := strings.Index(src, "range targets")
	if effIdx < 0 || altIdx < 0 || effIdx > altIdx {
		t.Error("etkin-durum kontrolü ALTER döngüsünden ÖNCE olmalı")
	}
	if !strings.Contains(src, "isSettingsChangeUnsupported(err)") ||
		!strings.Contains(src, "distributed_background_insert_batch=1 ve distributed_background_insert_split_batch_on_failure=1") {
		t.Error("Code 36 dalı tek özet + profil çaresiyle kesmeli")
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }
