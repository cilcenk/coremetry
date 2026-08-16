// v0.9.1097 regresyon testleri — 0.5 Alternatif-B: dbstmt exemplar'ı
// MV-gömülü argMax'a taşındı.
//
// Yapısal sebep (exemplar-pivot unification audit §5.3): statement
// class'ı SERVİSLER ARASI — ham tarama service_name predicate'i
// koyamaz, PK prefix yok; 1B span/gün ölçeğinde 24h penceresi ~22s ve
// timeout'un tek belirtisi "No exemplar spans" idi. MV iki soruyu
// (slow+error) tek küçük sorguda cevaplar; ham yol yalnız fallback ve
// artık pencere kelepçeli.
package chstore

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Finalizer eşleşmesi sözleşmedir ([[reference-ch-inplace-mv-column-add]]
// dersi): argMaxState↔argMaxMerge, argMaxIfState↔argMaxIfMerge. Yanlış
// eş CH'de hata değil SESSİZ boş üretir — bu test o sınıfı çiviler.
func TestDBStmtExemplarFinalizerPairs(t *testing.T) {
	if !strings.Contains(dbStmtExemplarMVSQL, "argMaxMerge(slow_exemplar_state)") {
		t.Error("slow exemplar finalizer'ı argMaxMerge olmalı")
	}
	if !strings.Contains(dbStmtExemplarMVSQL, "argMaxIfMerge(error_exemplar_state)") {
		t.Error("error exemplar finalizer'ı argMaxIfMerge olmalı (argMaxIfState'in eşi)")
	}
	if !strings.Contains(dbStmtExemplarMVSQL, "max_execution_time") {
		t.Error("MV okuması da bütçesiz kalamaz")
	}
	// Kova hizası: MV 5dk gridinde — From, aralık başına yuvarlanmalı.
	if !strings.Contains(dbStmtExemplarMVSQL, "toStartOfInterval(?, INTERVAL 5 MINUTE)") {
		t.Error("time_bucket alt sınırı 5dk gridine hizalanmalı")
	}
}

func TestClampExemplarWindow(t *testing.T) {
	to := time.Unix(1_700_000_000, 0)
	t.Run("24h altı aynen", func(t *testing.T) {
		from := to.Add(-2 * time.Hour)
		gf, gt := clampExemplarWindow(from, to)
		if !gf.Equal(from) || !gt.Equal(to) {
			t.Errorf("dar pencere değişmemeli: %v-%v", gf, gt)
		}
	})
	t.Run("90 gün → son 24h", func(t *testing.T) {
		from := to.Add(-90 * 24 * time.Hour)
		gf, gt := clampExemplarWindow(from, to)
		if !gf.Equal(to.Add(-24*time.Hour)) || !gt.Equal(to) {
			t.Errorf("geniş pencere To-24h'a çekilmeli: %v-%v", gf, gt)
		}
	})
	t.Run("tam 24h sınırda aynen", func(t *testing.T) {
		from := to.Add(-24 * time.Hour)
		gf, _ := clampExemplarWindow(from, to)
		if !gf.Equal(from) {
			t.Errorf("tam sınır kelepçelenmemeli")
		}
	})
}

// Kaynak pinleri: (a) CREATE iki exemplar state'i taşıyor (taze kurulum
// + recreate kaynağı aynı literal); (b) upgrade bloğu hasDBStmtHashCol
// kapılı (dış-dağıtık kurulumda MV zaten devre dışı — code-16 sınıfına
// yeni üye eklenemez).
func TestDBStmtExemplarMVSourcePins(t *testing.T) {
	b, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("kaynak okunamadı: %v", err)
	}
	src := string(b)
	i := strings.Index(src, "db_statement_summary_5m\n")
	if i < 0 {
		t.Fatal("MV CREATE bulunamadı")
	}
	seg := src[i : i+2500]
	for _, must := range []string{
		"argMaxState(trace_id, duration)               AS slow_exemplar_state",
		"argMaxIfState(trace_id, duration, status_code = 'error') AS error_exemplar_state",
	} {
		if !strings.Contains(seg, must) {
			t.Errorf("CREATE'te eksik: %s", must)
		}
	}
	if !strings.Contains(src, "if s.hasDBStmtHashCol {") ||
		!strings.Contains(src, "upgrading db_statement_summary_5m MV (adding exemplar states)") {
		t.Error("exemplar upgrade bloğu hasDBStmtHashCol kapısıyla var olmalı")
	}
}

// ── v0.9.1098 — canlı doğrulamanın yakaladığı iki dağıtık-yol bug'ı ──
//
// Bug 1 (her restart'ta veri kaybı): v0.9.1097 probe'u ÇIPLAK adı
// ölçüyordu; cluster modunda çıplak ad Distributed wrapper'dır ve
// kolonu asla kazanmaz → upgrade her boot yeniden koşup statement
// geçmişini siliyordu. Probe artık mvStorageName (TDigest emsali).
//
// Bug 2: wrapper kolon listesini CREATE anında dondurur; _local iki
// kolon kazanınca okuma wrapper'dan Code 47 yiyordu. Upgrade'den
// BAĞIMSIZ drift-iyileştirici her boot kaymayı kapatır (deferred-DDL
// yarışına dayanıklı; soft-fail).
func TestDBStmtExemplarUpgradeDistributedSafety(t *testing.T) {
	b, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("kaynak okunamadı: %v", err)
	}
	src := string(b)
	i := strings.Index(src, "v0.9.1098 — probe hedefi STORAGE adı")
	if i < 0 {
		t.Fatal("1098 probe bloğu bulunamadı")
	}
	seg := src[i : i+4200]
	if !strings.Contains(seg, `s.mvStorageName("db_statement_summary_5m")`) {
		t.Error("probe STORAGE adını ölçmeli (çıplak ad = wrapper = her boot yeniden upgrade)")
	}
	if strings.Contains(seg, `AND table    = 'db_statement_summary_5m'
			  AND name     = 'slow_exemplar_state'`) &&
		!strings.Contains(seg, "wrapperHas") {
		t.Error("çıplak-ad probe'u yalnız drift-iyileştiricinin wrapper ölçümünde olabilir")
	}
	for _, must := range []string{
		"WRAPPER KOLON KAYMASI iyileştiricisi",
		"localHas == 1 && wrapperHas == 0",
		"AS db_statement_summary_5m_local ENGINE = Distributed",
	} {
		if !strings.Contains(seg, must) {
			t.Errorf("drift-iyileştirici parçası eksik: %s", must)
		}
	}
}

// v0.9.1099 — iyileştirici DDL'i adaptDDL'den GEÇMEMELİ: ifadeler ON
// CLUSTER'ı zaten taşır; execDDL yüksek-hacim adını görüp hedefi
// _local'a çevirip İKİNCİ bir ON CLUSTER basıyordu (code 62), CREATE
// ölüyor, DROP geçiyor ve her boot wrapper'ı silip geri koyamıyordu
// (Slow Queries UNKNOWN_TABLE — canlı doğrulama, restart kanıtı ajanı).
func TestDBStmtWrapperHealerBypassesAdaptDDL(t *testing.T) {
	b, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("kaynak okunamadı: %v", err)
	}
	src := string(b)
	i := strings.Index(src, "WRAPPER KOLON KAYMASI iyileştiricisi")
	if i < 0 {
		t.Fatal("iyileştirici bloğu bulunamadı")
	}
	end := strings.Index(src[i:], "Bayrak her boot'ta VERİDEN")
	if end < 0 {
		t.Fatal("blok sonu bulunamadı")
	}
	seg := src[i : i+end]
	if !strings.Contains(seg, "s.conn.Exec(ctx") {
		t.Error("iyileştirici doğrudan s.conn.Exec kullanmalı")
	}
	if strings.Contains(seg, "s.execDDL(") {
		t.Error("iyileştirici execDDL'den GEÇMEMELİ — adaptDDL çifte ON CLUSTER + _local yeniden adlandırma basar (v0.9.1098 regresyonu)")
	}
}
