package chstore

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// v0.9.1311 — 0009 göçünün ADIM 3c'si (append-only sınırlı yakalama).
//
// Orijinal semptom: göç dosyasının T2 tuzağı, ADIM 2 ile ADIM 3
// arasındaki saniyelerde uygulamanın eski tabloya yazdığı satırların
// `_old`'da kalmasını "kabul edilen boşluk" ilan ediyordu. Beş
// append-only MergeTree tablosunda (audit_log, incident_events,
// notification_log, ai_calls, monitor_results) bu KALICI kayıptı,
// çünkü 3b yakalaması orada çift satır üretir.
//
// Bu testler yakalamanın iki kırılgan yerini çiviler:
//   1. sözleşme göç dosyasıyla AYNI kalmalı (tek kaynak),
//   2. üretilen SQL pencereyi kesime SABİTLEMELİ ve elemeyi anti-join
//      ile yapmalı — düz `>` kısmi batch'i düşürür, `>=` çiftler
//      (v0.9.1306: bir tikin batch'i tek nanosaniyeyi paylaşabiliyor).

func TestStateCatchUpSQL(t *testing.T) {
	tests := []struct {
		name       string
		table      string
		wantOK     bool
		wantCut    string
		wantProbe  string
		wantInsert string
	}{
		{
			name:       "audit_log — id ayırt edici",
			table:      "audit_log",
			wantOK:     true,
			wantCut:    "SELECT toString(max(`time`)) FROM `audit_log_unified`",
			wantProbe:  "SELECT count(), uniqExact((`time`, `id`)) FROM `audit_log_old` WHERE `time` >= toDateTime64(?, 9)",
			wantInsert: "INSERT INTO `audit_log` SELECT * FROM `audit_log_old` WHERE `time` >= toDateTime64(?, 9) AND (`time`, `id`) NOT IN (SELECT (`time`, `id`) FROM `audit_log` WHERE `time` >= toDateTime64(?, 9))",
		},
		{
			name:       "notification_log — zaman kolonu time DEĞİL",
			table:      "notification_log",
			wantOK:     true,
			wantCut:    "SELECT toString(max(`sent_at`)) FROM `notification_log_unified`",
			wantProbe:  "SELECT count(), uniqExact((`sent_at`, `id`)) FROM `notification_log_old` WHERE `sent_at` >= toDateTime64(?, 9)",
			wantInsert: "INSERT INTO `notification_log` SELECT * FROM `notification_log_old` WHERE `sent_at` >= toDateTime64(?, 9) AND (`sent_at`, `id`) NOT IN (SELECT (`sent_at`, `id`) FROM `notification_log` WHERE `sent_at` >= toDateTime64(?, 9))",
		},
		{
			name:       "ai_calls — id sıralama anahtarında DEĞİL ama tekil",
			table:      "ai_calls",
			wantOK:     true,
			wantCut:    "SELECT toString(max(`created_at`)) FROM `ai_calls_unified`",
			wantProbe:  "SELECT count(), uniqExact((`created_at`, `id`)) FROM `ai_calls_old` WHERE `created_at` >= toDateTime64(?, 9)",
			wantInsert: "INSERT INTO `ai_calls` SELECT * FROM `ai_calls_old` WHERE `created_at` >= toDateTime64(?, 9) AND (`created_at`, `id`) NOT IN (SELECT (`created_at`, `id`) FROM `ai_calls` WHERE `created_at` >= toDateTime64(?, 9))",
		},
		{
			// id kolonu YOK: anahtar sıralama anahtarının tamamı + ref_id.
			// Ölçüldü (2026-08-23) bu tabloda tuple TEKİL DEĞİL — bu yüzden
			// çalışma anındaki prob kapısı bunu reddedecek. SQL yine de
			// doğru üretilmeli; kararı prob verir, üretici değil.
			name:       "incident_events — id yok, çok kolonlu anahtar",
			table:      "incident_events",
			wantOK:     true,
			wantCut:    "SELECT toString(max(`time`)) FROM `incident_events_unified`",
			wantProbe:  "SELECT count(), uniqExact((`incident_id`, `time`, `kind`, `ref_id`)) FROM `incident_events_old` WHERE `time` >= toDateTime64(?, 9)",
			wantInsert: "INSERT INTO `incident_events` SELECT * FROM `incident_events_old` WHERE `time` >= toDateTime64(?, 9) AND (`incident_id`, `time`, `kind`, `ref_id`) NOT IN (SELECT (`incident_id`, `time`, `kind`, `ref_id`) FROM `incident_events` WHERE `time` >= toDateTime64(?, 9))",
		},
		{
			name:       "monitor_results — id yok, sıralama anahtarı",
			table:      "monitor_results",
			wantOK:     true,
			wantCut:    "SELECT toString(max(`time`)) FROM `monitor_results_unified`",
			wantProbe:  "SELECT count(), uniqExact((`monitor_id`, `time`)) FROM `monitor_results_old` WHERE `time` >= toDateTime64(?, 9)",
			wantInsert: "INSERT INTO `monitor_results` SELECT * FROM `monitor_results_old` WHERE `time` >= toDateTime64(?, 9) AND (`monitor_id`, `time`) NOT IN (SELECT (`monitor_id`, `time`) FROM `monitor_results` WHERE `time` >= toDateTime64(?, 9))",
		},
		{
			// ReplacingMergeTree: 3b yakalaması zaten idempotent, sınırlı
			// yakalamaya İHTİYACI YOK. Sözleşmede bulunmaması bilinçli.
			name:   "problems — RMT, sözleşme yok",
			table:  "problems",
			wantOK: false,
		},
		{
			name:   "bilinmeyen tablo",
			table:  "hicboyletabloyok",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sp, ok := stateCatchUp(tc.table)
			if ok != tc.wantOK {
				t.Fatalf("stateCatchUp(%q) ok = %v, beklenen %v", tc.table, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got := stateCatchUpCutSQL(sp, tc.table+"_unified"); got != tc.wantCut {
				t.Errorf("kesim SQL:\n  aldım   %s\n  bekledim %s", got, tc.wantCut)
			}
			if got := stateCatchUpProbeSQL(sp, tc.table+"_old"); got != tc.wantProbe {
				t.Errorf("prob SQL:\n  aldım   %s\n  bekledim %s", got, tc.wantProbe)
			}
			if got := stateCatchUpInsertSQL(sp, tc.table, tc.table+"_old"); got != tc.wantInsert {
				t.Errorf("insert SQL:\n  aldım   %s\n  bekledim %s", got, tc.wantInsert)
			}
		})
	}
}

// Pencerenin iki ucu da AYNI kesim noktasına bağlı olmalı. Dış WHERE ile
// anti-join'in iç WHERE'i ayrışırsa eleme penceresi kayar ve zaten
// kopyalanmış satırlar ikinci kez yazılır (append-only'de kalıcı çift).
func TestStateCatchUpInsertPinsBothWindowsToTheCut(t *testing.T) {
	for table := range stateCatchUpSpecs {
		sp, _ := stateCatchUp(table)
		sql := stateCatchUpInsertSQL(sp, table, table+"_old")

		if n := strings.Count(sql, "toDateTime64(?, 9)"); n != 2 {
			t.Errorf("%s: kesim bağlaması %d kez geçiyor, 2 olmalı (dış pencere + anti-join penceresi): %s", table, n, sql)
		}
		// Düz eşiğe düşülmemeli: `>` kısmi batch'i düşürür (v0.9.1306).
		if strings.Contains(sql, "> toDateTime64") {
			t.Errorf("%s: pencere `>` kullanıyor — bir tikin batch'i tek nanosaniyeyi paylaşabiliyor, kısmi batch düşer: %s", table, sql)
		}
		// Eleme anti-join olmalı; yoksa `>=` zaten kopyalananları çiftler.
		if !strings.Contains(sql, "NOT IN (") {
			t.Errorf("%s: anti-join yok — `>=` penceresi zaten kopyalanmış satırları çiftler: %s", table, sql)
		}
		// Anahtar tuple'ı iki tarafta da AYNI yazılmalı.
		key := stateCatchUpKeyTuple(sp)
		if n := strings.Count(sql, key); n != 2 {
			t.Errorf("%s: anahtar tuple'ı %d kez geçiyor, 2 olmalı (eleme her iki tarafta aynı anahtarla yapılmalı)", table, n)
		}
	}
}

// Zaman kolonu anahtarın İÇİNDE olmalı. Dışarıda kalırsa anti-join aynı
// anahtarlı ama farklı zamanlı satırları da eler = veri kaybı.
func TestStateCatchUpKeyContainsTimeColumn(t *testing.T) {
	for table, sp := range stateCatchUpSpecs {
		found := false
		for _, c := range sp.Key {
			if c == sp.TimeCol {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: TimeCol %q anahtarda yok (%v) — anti-join farklı zamanlı satırları elerdi", table, sp.TimeCol, sp.Key)
		}
	}
}

// Sözleşmenin tek kaynağı göç dosyasıdır. Go tablosu ile dosyadaki
// `-- @catchup` satırları ıraksarsa script yolu ile sihirbaz yolu FARKLI
// yakalama yapar — tam olarak kaçınmak istediğimiz sessiz ayrışma.
func TestStateCatchUpSpecsMatchMigration(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/0009_state_unify.sql")
	if err != nil {
		t.Fatalf("göç dosyası okunamadı: %v", err)
	}

	fromFile := map[string]stateCatchUpSpec{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "-- @catchup ") {
			continue
		}
		f := strings.Fields(strings.TrimPrefix(line, "-- @catchup "))
		if len(f) != 3 {
			t.Fatalf("bozuk @catchup satırı (3 alan bekleniyor): %q", line)
		}
		fromFile[f[0]] = stateCatchUpSpec{TimeCol: f[1], Key: strings.Split(f[2], ",")}
	}

	if len(fromFile) == 0 {
		t.Fatal("göç dosyasında hiç `-- @catchup` satırı yok — sözleşme kaynağı kaybolmuş")
	}
	if !reflect.DeepEqual(fromFile, stateCatchUpSpecs) {
		gotKeys := make([]string, 0, len(stateCatchUpSpecs))
		for k := range stateCatchUpSpecs {
			gotKeys = append(gotKeys, k)
		}
		fileKeys := make([]string, 0, len(fromFile))
		for k := range fromFile {
			fileKeys = append(fileKeys, k)
		}
		sort.Strings(gotKeys)
		sort.Strings(fileKeys)
		t.Errorf("Go tablosu göç dosyasıyla ıraksadı.\n  Go   : %v -> %+v\n  dosya: %v -> %+v",
			gotKeys, stateCatchUpSpecs, fileKeys, fromFile)
	}
}
