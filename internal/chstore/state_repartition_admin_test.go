package chstore

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// v0.9.1341 — 0010 SİHİRBAZININ kapıları.
//
// Kardeş dosya state_repartition_test.go (v0.9.1335) göçün HEDEFİNİ
// pinler: DDL'de PARTITION BY yok, ORDER BY id değişmedi, boot teşhisi
// çağrılıyor. Bu dosya göçü KOŞAN yüzeyi pinler. İkisi ayrı kalır —
// biri şemanın son hâli, diğeri oraya giden yordam hakkında.
//
// ── 1341'in ana kapısı: sihirbaz ile .sql ıraksamasın ──
//
// Bu kod tabanı bugün AYNI ŞEYİN İKİ KEZ YAZILMASINDAN üç bug üretti.
// 0009 doğru çözmüştü: şema hiç elle yazılmaz, store metodu onu canlı
// `create_table_query`den türetir. 0010 aynısını yapar ve bu test iki
// yazılışı birbirine PİNLER.
//
// Teorik değil: prod'un `problems` tablosu store.go'nun taban DDL'inde
// OLMAYAN üç kolon taşıyor (ai_summary, ai_summary_at, comparator) ve
// v0.9.1338 dördüncüyü ekledi (kind). Elle yazılmış bir şema bu dördünü
// SESSİZCE düşürürdü.

// extractGeneratorBlocks — .sql dosyasındaki `SELECT replaceOne(` …
// `FORMAT TSVRaw;` bloklarını sırayla çıkarır.
func extractGeneratorBlocks(t *testing.T, path string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("göç dosyası okunamadı: %v", err)
	}
	var blocks [][]string
	var cur []string
	in := false
	for _, line := range strings.Split(string(raw), "\n") {
		if !in && strings.HasPrefix(line, "SELECT replaceOne(") {
			in = true
			cur = nil
		}
		if !in {
			continue
		}
		if strings.HasPrefix(line, "FORMAT TSVRaw;") {
			blocks = append(blocks, cur)
			in = false
			continue
		}
		cur = append(cur, line)
	}
	if in {
		t.Fatal("kapanmamış üretici bloğu — 'FORMAT TSVRaw;' terminatörü yok")
	}
	return blocks
}

func TestStateRepartGeneratorsMatchMigration(t *testing.T) {
	blocks := extractGeneratorBlocks(t, "../../migrations/0010_state_repartition.sql")
	// TAM SAYI ŞART. `len(blocks) == 0` kapısı YETMEZ: dosyadan AŞAMA B
	// üreticisi silinse ilk blok yine bulunur ve test yeşil kalırdı.
	if len(blocks) != 2 {
		t.Fatalf("0010'da 2 üretici bloğu bekleniyordu, %d bulundu", len(blocks))
	}

	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	// Dosya prod küme adını (uptrace_all) taşır; sihirbaz onu parametre alır.
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"AŞAMA A · ADIM 1", stateRepartGeneratorSQL("uptrace_all"), strings.Join(blocks[0], "\n")},
		{"AŞAMA B · B1", stateRepartPathfixGeneratorSQL("uptrace_all"), strings.Join(blocks[1], "\n")},
	}
	for _, tc := range cases {
		if norm(tc.got) != norm(tc.want) {
			t.Errorf("%s üreticisi göç dosyasıyla ıraksadı.\n  Go   : %s\n  dosya: %s",
				tc.name, norm(tc.got), norm(tc.want))
		}
	}
	// İki blok BİRBİRİNDEN de farklı olmalı — çıkarıcı aynı bloğu iki kez
	// döndürseydi yukarıdaki iki karşılaştırmadan biri sessizce yanlış
	// şeyi doğrularadı.
	if norm(strings.Join(blocks[0], "\n")) == norm(strings.Join(blocks[1], "\n")) {
		t.Error("çıkarıcı aynı bloğu iki kez döndürdü")
	}
}

// Tablo listesi ÜÇ yerde yazılı: üretici SQL'inin `name IN (…)`ı,
// stateRepartTables ve stateRepartNameList(). Üçü ıraksarsa ön kontrol
// bir tabloyu ölçer, göç başkasını taşır.
func TestStateRepartTableListIsSingleSourced(t *testing.T) {
	list := stateRepartNameList()
	if list != "('problems', 'anomaly_events')" {
		t.Fatalf("stateRepartNameList() = %s", list)
	}
	for _, gen := range []string{
		stateRepartGeneratorSQL("c"),
		stateRepartPathfixGeneratorSQL("c"),
	} {
		if !strings.Contains(gen, "name IN "+list) {
			t.Errorf("üretici SQL'i 'name IN %s' içermiyor:\n%s", list, gen)
		}
	}
	for _, want := range []string{"problems", "anomaly_events"} {
		if !stateRepartKnownTable(want) {
			t.Errorf("stateRepartKnownTable(%q) = false", want)
		}
	}
	if stateRepartKnownTable("spans") {
		t.Error("stateRepartKnownTable allowlist'i sızdırıyor — DROP yüzeyi açılır")
	}
}

// DDL kapısı. Göç dosyası bu doğrulamayı operatörün GÖZÜNE bırakıyor
// ("⚠ 2 satır çıkmalı; `_repart` GEÇMELİ, `PARTITION BY` GEÇMEMELİ").
// Sihirbazda göz yok: replaceOne'ın tutmadığı bir DDL sessizce
// PARTITION BY'lı bir kopya kurar, göç HİÇBİR ŞEY düzeltmez ama başarılı
// görünür.
func TestStateRepartCheckDDL(t *testing.T) {
	const okA = "CREATE TABLE cm.problems_repart ON CLUSTER uptrace_all (`id` String, `kind` LowCardinality(String) DEFAULT 'service') " +
		"ENGINE = ReplicatedReplacingMergeTree('/ch/tables/cm/state/problems_repart', '{shard}-{replica}', version) ORDER BY id SETTINGS index_granularity = 8192"
	const okB = "CREATE TABLE cm.problems_pathfix ON CLUSTER uptrace_all (`id` String) " +
		"ENGINE = ReplicatedReplacingMergeTree('/ch/tables/cm/state/problems', '{shard}-{replica}', version) ORDER BY id"

	wantA := stateRepartDDLWant{Table: "problems", Suffix: "_repart", Cluster: "uptrace_all", ZKName: "problems_repart"}
	wantB := stateRepartDDLWant{Table: "problems", Suffix: "_pathfix", Cluster: "uptrace_all", ZKName: "problems"}

	tests := []struct {
		name    string
		ddl     string
		want    stateRepartDDLWant
		wantErr string // boş = geçmeli
	}{
		{"A geçerli", okA, wantA, ""},
		{"B geçerli", okB, wantB, ""},
		{"boş", "   ", wantA, "boş DDL"},
		{"CREATE TABLE değil", "ALTER TABLE cm.problems ADD COLUMN x Int", wantA, "'CREATE TABLE' ile başlamıyor"},
		{"ad enjeksiyonu tutmadı",
			strings.Replace(okA, "problems_repart ON CLUSTER uptrace_all", "problems ON CLUSTER uptrace_all", 1),
			wantA, "ad/küme enjeksiyonu tutmadı"},
		{"küme adı yanlış",
			strings.Replace(okA, "ON CLUSTER uptrace_all", "ON CLUSTER coremetry", 1),
			wantA, "ad/küme enjeksiyonu tutmadı"},
		{"PARTITION BY duruyor",
			strings.Replace(okA, "ORDER BY id", "PARTITION BY toDate(started_at) ORDER BY id", 1),
			wantA, "PARTITION BY hâlâ duruyor"},
		{"motor değişmiş",
			strings.Replace(okA, "ReplicatedReplacingMergeTree(", "ReplicatedMergeTree(", 1),
			wantA, "motor ReplicatedReplacingMergeTree değil"},
		{"ZK yolu sökülmemiş (A)",
			strings.Replace(okA, "/state/problems_repart', '", "/state/problems', '", 1),
			wantA, "ZK yolu '/state/problems_repart' değil"},
		{"ZK yolu geçici kalmış (B)",
			strings.Replace(okB, "/state/problems', '", "/state/problems_repart', '", 1),
			wantB, "ZK yolu '/state/problems' değil"},
		{"ORDER BY kaybolmuş",
			strings.Replace(okA, "ORDER BY id", "ORDER BY (id, started_at)", 1),
			wantA, "ORDER BY id kaybolmuş"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := stateRepartCheckDDL(tc.ddl, tc.want)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("geçmesi bekleniyordu, hata: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("hata bekleniyordu (%q), geçti", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("hata %q içermeliydi, gelen: %v", tc.wantErr, err)
			}
		})
	}
}

// ZK kapısı TEK yönlü çalışabilir mi — önek çakışması kontrolü.
//
// stateRepartCheckDDL'in ZK kapısı "doğru segment VAR MI" sorar; ayrı
// bir "yanlış segment YOK MU" kapısı YOK, çünkü mutasyon testi öyle bir
// kapının hiçbir girdide ısırmadığını gösterdi (tek tablonun DDL'inde
// tek ZK yolu vardır). Bu ancak iki dizeden hiçbiri diğerini İÇERMEZSE
// doğrudur: `_repart` yolu kanonik yolu içerseydi AŞAMA B'nin kapısı
// AŞAMA A çıktısını da kabul ederdi (v0.9.1021 önek dersi).
func TestStateRepartZKGuardsDoNotCollide(t *testing.T) {
	a := "/state/problems_repart', '"
	b := "/state/problems', '"
	if strings.Contains(a, b) {
		t.Errorf("%q içinde %q var — B kapısı AŞAMA A çıktısını da kabul ederdi", a, b)
	}
	if strings.Contains(b, a) {
		t.Errorf("%q içinde %q var — A kapısı AŞAMA B çıktısını da kabul ederdi", b, a)
	}
	// Pozitif kontrol: kapı GERÇEKTEN ayırt ediyor mu.
	base := "CREATE TABLE cm.problems_pathfix ON CLUSTER c (x Int) ENGINE = ReplicatedReplacingMergeTree('%s{shard}-{replica}', version) ORDER BY id"
	wantB := stateRepartDDLWant{Table: "problems", Suffix: "_pathfix", Cluster: "c", ZKName: "problems"}
	if err := stateRepartCheckDDL(fmt.Sprintf(base, "/x"+b), wantB); err != nil {
		t.Errorf("kanonik yol reddedildi: %v", err)
	}
	if err := stateRepartCheckDDL(fmt.Sprintf(base, "/x"+a), wantB); err == nil {
		t.Error("geçici (_repart) yol AŞAMA B kapısından geçti")
	}
}

// 0010'un okuma kapısı 0009'unkinin TERSİ. Ölçüldü (v0.9.1309): birleşik
// `problems` chc-0=4808, chc-1=4808, cluster() = 9616. 0009'un ADIM 2
// kestirmesini 0010'a kopyalamak satırları shard sayısı kadar KATLARDI.
func TestStateRepartSingleGroupIsTheInverseOfClusterReadSafe(t *testing.T) {
	tests := []struct {
		paths, shards int
		repartOK      bool // 0010: düz yerel okuma güvenli mi
		unifyOK       bool // 0009: cluster() okuması güvenli mi
	}{
		{1, 2, true, false},  // birleşik: 0010 evet, 0009 HAYIR (çift sayar)
		{2, 2, false, true},  // bölünmüş: 0010 HAYIR (yarısını kaybeder), 0009 evet
		{1, 1, true, true},   // tek shard: ikisi de aynı şey
		{3, 4, false, false}, // kısmen göç etmiş: ikisi de reddeder
		{0, 2, false, false}, // ölçülemedi
	}
	for _, tc := range tests {
		if got := stateRepartSingleGroup(tc.paths); got != tc.repartOK {
			t.Errorf("stateRepartSingleGroup(%d) = %v, beklenen %v", tc.paths, got, tc.repartOK)
		}
		if got := clusterReadSafe(tc.paths, tc.shards); got != tc.unifyOK {
			t.Errorf("clusterReadSafe(%d,%d) = %v, beklenen %v", tc.paths, tc.shards, got, tc.unifyOK)
		}
	}
}

// Aşama ÖLÇÜLEN üç girdiden türer; hiçbiri varsayılmaz.
func TestStateRepartStage(t *testing.T) {
	const repartZK = "/ch/tables/cm/state/problems_repart"
	const canonZK = "/ch/tables/cm/state/problems"
	tests := []struct {
		name          string
		partitionKey  string
		zk            string
		hasPathfixOld bool
		want          string
	}{
		{"göç öncesi", "toDate(started_at)", canonZK, false, "A"},
		{"partition duruyor, yol zaten geçici", "toDate(started_at)", repartZK, false, "A"},
		{"AŞAMA A bitti", "", repartZK, false, "B"},
		{"AŞAMA B bitti, yedek duruyor", "", canonZK, true, "cleanup"},
		{"tamamen bitti", "", canonZK, false, "done"},
		{"boşluklu partition_key bir anahtardır", "   ", canonZK, false, "done"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := stateRepartStage(tc.partitionKey, tc.zk, tc.hasPathfixOld); got != tc.want {
				t.Errorf("stateRepartStage(%q,%q,%v) = %q, beklenen %q",
					tc.partitionKey, tc.zk, tc.hasPathfixOld, got, tc.want)
			}
		})
	}
}

// En GERİDE kalan tablo aşamayı belirler: yarım kalmış bir koşuda iki
// tablo farklı aşamada olabilir ve sihirbaz hep en gerideki için
// düğmeyi açmalı.
func TestStateRepartOverallStageTakesTheLaggard(t *testing.T) {
	tests := []struct {
		name   string
		stages []string
		want   string
	}{
		{"ikisi de bakir", []string{"A", "A"}, "A"},
		{"biri taşındı biri kaldı", []string{"B", "A"}, "A"},
		{"sıra fark etmez", []string{"A", "B"}, "A"},
		{"ikisi de A'yı geçti", []string{"B", "B"}, "B"},
		{"biri temizlik bekliyor", []string{"done", "cleanup"}, "cleanup"},
		{"hepsi bitti", []string{"done", "done"}, "done"},
		{"tablo yok", nil, "done"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var tables []StateRepartTable
			for _, s := range tc.stages {
				tables = append(tables, StateRepartTable{Stage: s})
			}
			if got := stateRepartOverallStage(tables); got != tc.want {
				t.Errorf("stateRepartOverallStage(%v) = %q, beklenen %q", tc.stages, got, tc.want)
			}
		})
	}
}

// ADIM 0e'nin operatöre YAZILAN cümlesi. Ekranın işi SQL öğretmek değil:
// iki sayının ne anlama geldiğini düz cümleyle söylemek.
func TestStateRepartFinalSentence(t *testing.T) {
	tests := []struct {
		name         string
		def, noMerge uint64
		wantSubstr   []string
		notSubstr    []string
	}{
		{
			name: "kusur canlı", def: 4808, noMerge: 4829,
			wantSubstr: []string{"FARKLI", "+21", "21 BAYAT satır", "ACİL"},
			notSubstr:  []string{"EŞİT"},
		},
		{
			name: "kusur bugün ısırmıyor", def: 4808, noMerge: 4808,
			wantSubstr: []string{"EŞİT", "BUGÜN ısırmıyor", "Göç gerekli"},
			notSubstr:  []string{"ACİL", "FARKLI"},
		},
		{
			name: "ters fark — ölçüm anlamsız", def: 4829, noMerge: 4808,
			wantSubstr: []string{"BEKLENMEDİK", "elle incele"},
			notSubstr:  []string{"EŞİT", "ACİL"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := stateRepartFinalSentence("problems", tc.def, tc.noMerge)
			for _, w := range tc.wantSubstr {
				if !strings.Contains(got, w) {
					t.Errorf("cümlede %q yok: %s", w, got)
				}
			}
			for _, n := range tc.notSubstr {
				if strings.Contains(got, n) {
					t.Errorf("cümlede %q OLMAMALIYDI: %s", n, got)
				}
			}
			if !strings.HasPrefix(got, "problems: ") {
				t.Errorf("cümle tablo adıyla başlamalı: %s", got)
			}
		})
	}
}

func TestStateRepartTableFromDDL(t *testing.T) {
	tests := []struct{ in, suffix, want string }{
		{"CREATE TABLE cm.problems_repart ON CLUSTER x (`id` String) ENGINE = …", "_repart", "problems"},
		{"CREATE TABLE cm.anomaly_events_repart ON CLUSTER x (a Int)", "_repart", "anomaly_events"},
		{"CREATE TABLE problems_pathfix ON CLUSTER c (a Int)", "_pathfix", "problems"},
		// Yanlış ek: üretici tutmamış demektir, ad TÜRETİLMEZ. 0009'un
		// TrimSuffix'i burada sessizce "problems_repart" döndürürdü ve
		// göç var olmayan bir tabloyu taşımaya çalışırdı.
		{"CREATE TABLE cm.problems_repart ON CLUSTER x (a Int)", "_pathfix", ""},
		{"CREATE TABLE cm.problems ON CLUSTER x (a Int)", "_repart", ""},
		{"DROP TABLE x", "_repart", ""},
		{"", "_repart", ""},
	}
	for _, tc := range tests {
		if got := stateRepartTableFromDDL(tc.in, tc.suffix); got != tc.want {
			t.Errorf("stateRepartTableFromDDL(%q, %q) = %q, beklenen %q", tc.in, tc.suffix, got, tc.want)
		}
	}
}

// Nil dilim JSON'da `null` olur ve FE `.map()` üstünde patlar; React
// hata sınırı yalnız paneli değil SAYFAYI alır (v0.9.1315).
func TestStateRepartPreflightNormalize(t *testing.T) {
	r := StateRepartPreflightResult{Tables: []StateRepartTable{{Name: "problems"}}}
	r.Normalize()
	if r.Tables[0].Hosts == nil {
		t.Error("Hosts nil kaldı — 0 satırlı tablo hiç grup üretmez ve sayfayı çökertir")
	}
	var empty StateRepartPreflightResult
	empty.Normalize()
	if empty.Tables == nil {
		t.Error("Tables nil kaldı")
	}
}

// v0.9.1343 regresyonu — OPERATÖR RAPORU (prod, göç ortasında).
//
// `problems` ön kontrolde "AYRIŞIYOR" damgası yiyip 0010'u BLOKLADI, oysa
// tekilleştirilmiş sayı dört host'ta da birebir aynıydı (637.388). Sorgu
// `count()` okuyordu; ReplacingMergeTree'de fiziksel satır sayısının
// replikalar arasında eşit olması BEKLENMEZ (her replika bağımsız merge
// eder). Prod'da oran ~2,7×'ti ve merge zamanlamasıyla oynuyordu — yani
// sert durdurma rastgele tetiklenebilen bir kapıydı.
func TestStateRepartHostCheckCountsIDsNotRows(t *testing.T) {
	got := stateRepartHostIDCountSQL("problems", "'uptrace_all'")

	// ASIL İDDİA: kimlik sayılıyor.
	if !strings.Contains(got, "uniqExact(id)") {
		t.Fatalf("host kontrolü uniqExact(id) kullanmalı, aldım: %s", got)
	}
	// Ve ham satır sayımı GERİ GELMEMELİ. `count()` alt dizgisi
	// `uniqExact(...)` içinde geçmiyor, o yüzden bu ayrım keskin.
	if strings.Contains(got, "count()") {
		t.Fatalf("host kontrolü ham count() okumamalı (v0.9.1343 yanlış-pozitifi), aldım: %s", got)
	}
	// Şekil: küme sorgusu, host'a göre grup.
	for _, want := range []string{
		"hostName() AS host",
		"clusterAllReplicas('uptrace_all', currentDatabase(), `problems`)",
		"GROUP BY host",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("eksik parça %q, aldım: %s", want, got)
		}
	}
}

// Ters yön: 0009'un sihirbazı BİLİNÇLİ olarak `count()` okumaya devam
// ediyor ve bu bir unutma değil. 37 state tablosunun hepsinde `id` YOK
// (system_settings, api_tokens, service_metadata, ldap_groups — ölçüldü
// 2026-08-24), yani aynı düzeltme oraya uygulansa ön kontrol "kolon
// bulunamadı" ile tamamen kırılırdı. Bu test o gerekçenin kaynakta
// YAZILI kalmasını çiviliyor: biri "tutarlılık" diye 0009'u da
// değiştirmeye kalkarsa önce bu şerhi okur.
func TestStateUnifyHostCheckDivergenceIsDocumented(t *testing.T) {
	src, err := os.ReadFile("state_unify_admin.go")
	if err != nil {
		t.Fatalf("kaynak okunamadı: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "v0.9.1343") {
		t.Error("0009'un count() kullanımı v0.9.1343 şerhiyle gerekçelendirilmeli")
	}
	if !strings.Contains(s, "hepsinde") || !strings.Contains(s, "`id` YOK") {
		t.Error("gerekçe ölçümü (37 tablonun hepsinde id yok) kaynakta yazılı olmalı")
	}
}
