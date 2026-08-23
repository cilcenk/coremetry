package chstore

import (
	"encoding/json"
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

// v0.9.1312 — sihirbazın `cluster()` kestirmesinin ÇİFT SAYIM kapısı.
//
// Orijinal ölçüm (lokal, 2026-08-23): küme göçü zaten almış durumdayken
// `problems` chc-0=4808, chc-1=4808 iken `cluster()` 9616 döndürüyor.
// Yani kestirme yalnız tablo GERÇEKTEN bölünmüşken doğru; birleşik bir
// tabloya aynı INSERT'ü atmak veriyi shard sayısı kadar KATLAR.
func TestClusterReadSafe(t *testing.T) {
	tests := []struct {
		name          string
		distinctPaths int
		shardCount    int
		want          bool
	}{
		{"bölünmüş 2 shard — her shard kendi grubu", 2, 2, true},
		{"bölünmüş 4 shard", 4, 4, true},
		{"BİRLEŞİK 2 shard — cluster() 2 katına çıkarır", 1, 2, false},
		{"BİRLEŞİK 4 shard — cluster() 4 katına çıkarır", 1, 4, false},
		{"kısmen göç etmiş: 4 shard ama 3 grup", 3, 4, false},
		{"tek shard, tek grup — cluster() tek replika okur", 1, 1, true},
		{"tek shard ama iki grup — tutarsız", 2, 1, false},
		{"yol ölçülemedi", 0, 2, false},
		{"shard sayısı ölçülemedi", 2, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clusterReadSafe(tc.distinctPaths, tc.shardCount); got != tc.want {
				t.Errorf("clusterReadSafe(%d, %d) = %v, beklenen %v",
					tc.distinctPaths, tc.shardCount, got, tc.want)
			}
		})
	}
}

// Sihirbazın ADIM 1 üreticisi göç dosyasındakiyle AYNI olmalı. Iraksarsa
// sihirbaz, script'in ve dosyanın kurduğundan FARKLI bir şema kurar.
func TestStateUnifyGeneratorMatchesMigration(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/0009_state_unify.sql")
	if err != nil {
		t.Fatalf("göç dosyası okunamadı: %v", err)
	}
	var block []string
	in := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "SELECT replaceOne(") {
			in = true
		}
		if !in {
			continue
		}
		if strings.HasPrefix(line, "FORMAT TSVRaw;") {
			break
		}
		block = append(block, line)
	}
	if len(block) == 0 {
		t.Fatal("göç dosyasında ADIM 1 üretici bloğu bulunamadı")
	}

	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	// Dosya prod küme adını (uptrace_all) taşır; sihirbaz onu parametre alır.
	want := norm(strings.Join(block, "\n"))
	got := norm(stateUnifyGeneratorSQL("uptrace_all"))
	if got != want {
		t.Errorf("üretici sorgu göç dosyasıyla ıraksadı.\n  Go   : %s\n  dosya: %s", got, want)
	}
}

func TestStateUnifyTableFromDDL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"CREATE TABLE coremetry.problems_unified ON CLUSTER x (`id` String) ENGINE = …", "problems"},
		{"CREATE TABLE db.status_page_config_unified ON CLUSTER c (a Int)", "status_page_config"},
		{"CREATE TABLE problems_unified ON CLUSTER c (a Int)", "problems"},
		{"DROP TABLE x", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := stateUnifyTableFromDDL(tc.in); got != tc.want {
			t.Errorf("stateUnifyTableFromDDL(%q) = %q, beklenen %q", tc.in, got, tc.want)
		}
	}
}

// v0.9.1314 — okunamayan alan "bilinmiyor" olmalı, "sorunlu" DEĞİL.
//
// Orijinal semptom (prod, v0.9.1312): topoloji sorgusu UInt64 tarama
// hatası verdi, ön kontrol erken döndü, makro kontrolü HİÇ KOŞMADI ve
// `MacrosUnique bool` sıfır değerinde (false) kaldı. Arayüz bunu
// "MAKROLAR: ÇAKIŞIYOR" diye bastı — oysa prod'daki makrolar
// benzersizdi (01-node1, 01-node2, 02-node1, 02-node2). Fail-closed
// davranış doğruydu; YALAN SÖYLEYEN ETİKETTİ.
//
// Sabitlenen sözleşme: her verdict alanının SIFIR DEĞERİ bilinmiyordur.
// Erken dönen her yol, ölçemediği alanı otomatik olarak bilinmiyor
// bırakır — ayrıca "unknown yaz" demeyi unutmak mümkün değil.
func TestStateUnifyZeroValueVerdictsAreUnknown(t *testing.T) {
	// Ön kontrolün her erken dönüşü tam olarak bu şekli üretir: alanlar
	// hiç yazılmamıştır.
	var res StateUnifyPreflightResult

	fields := map[string]StateUnifyVerdict{
		"TopologyVerdict": res.TopologyVerdict,
		"MacrosVerdict":   res.MacrosVerdict,
		"TablesVerdict":   res.TablesVerdict,
	}
	for name, v := range fields {
		if v.Known() {
			t.Errorf("%s sıfır değeri Known() — ölçülmemiş alan kesin bir hüküm bildiriyor (%q)", name, v)
		}
		if v == VerdictBad {
			t.Errorf("%s sıfır değeri 'bad' — okunamayan alan 'ölçtüm ve bozuk' diye raporlanır", name)
		}
	}

	// Fail-closed korunmalı: bilinmiyorken göç BAŞLATILAMAZ.
	if res.Supported {
		t.Error("ölçüm yapılmamış ön kontrol Supported=true — göç bloklanmalıydı")
	}

	// Tel üzerinde boş dize olmamalı; arayüz '' ile undefined arasında
	// tahmin yürütmek zorunda kalmasın.
	b, err := json.Marshal(res.MacrosVerdict)
	if err != nil {
		t.Fatalf("marshal hatası: %v", err)
	}
	if string(b) != `"unknown"` {
		t.Errorf("bilinmiyor JSON'da %s, beklenen \"unknown\"", b)
	}
}

func TestStateUnifyVerdictKnown(t *testing.T) {
	tests := []struct {
		v        StateUnifyVerdict
		known    bool
		wantJSON string
	}{
		{VerdictUnknown, false, `"unknown"`},
		{VerdictOK, true, `"ok"`},
		{VerdictBad, true, `"bad"`},
		{StateUnifyVerdict("çöp"), false, `"unknown"`},
	}
	for _, tc := range tests {
		if got := tc.v.Known(); got != tc.known {
			t.Errorf("%q.Known() = %v, beklenen %v", tc.v, got, tc.known)
		}
		b, _ := json.Marshal(tc.v)
		if string(b) != tc.wantJSON {
			t.Errorf("%q JSON = %s, beklenen %s", tc.v, b, tc.wantJSON)
		}
	}
}
