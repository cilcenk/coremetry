package chstore

// messaging_operation_dim_test.go — v0.10.563 (Messaging Faz 4b).
//
// SÖZLEŞME: messaging_summary_5m'in `operation` boyutu ÜÇ yerde aynı anda
// yaşıyor ve üçü ayrışırsa arıza SESSİZDİR:
//
//  1. MV DDL'i (store.go) — coalesce zinciri + ORDER BY + GROUP BY.
//     Zincir sırası kayarsa iki anahtarı birden basan bir span farklı
//     operasyona düşer; GROUP BY'da operation unutulursa boyut MV'ye hiç
//     yazılmaz ama DDL hatasız derlenir.
//  2. Boot geçişi (mvDimMigrations) — defterde satır yoksa mevcut
//     kurulumlar eski şemada kalır, yeni okuma sıfır satır döner ve
//     operatör "bu topic'te operasyon yok" diye okur.
//  3. Okuma sorgusu (msgOperationREDSQL) — MV kaynağı + CH sınırları.
//
// Testler bu üç yüzeyi ayrı ayrı çiviler. Bir mutasyon (zincirden bir rung
// düşürmek, GROUP BY'dan operation'ı silmek, defter satırını kaldırmak,
// FROM'u ham `spans`e çevirmek) en az bir testi ısırır.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// mvDDLBody — store.go'nun mvs kataloğundan bir MV'nin DDL metnini AST
// üzerinden çeker. Metin araması DEĞİL: yorumlar ve başka dosyalardaki
// aynı-isimli satırlar yanlış pencere açardı (bkz. mv_dim_migrations.go
// başlığındaki not).
func mvDDLBody(t *testing.T, mv string) string {
	t.Helper()
	_, f := parseStoreGo(t)
	names, ddls := mvCatalogue(t, f)
	for i, n := range names {
		if n == mv {
			return ddls[i]
		}
	}
	t.Fatalf("%s katalogda yok — MV yeniden adlandırıldıysa bu test GÜNCELLENMELİ, silinmemeli", mv)
	return ""
}

// TestMessagingMVCarriesOperationDim — DDL yüzeyi.
func TestMessagingMVCarriesOperationDim(t *testing.T) {
	ddl := mvDDLBody(t, "messaging_summary_5m")

	cases := []struct {
		name string
		want string
		why  string
	}{
		{
			name: "materyalize kolon",
			want: ") AS operation,",
			why:  "SELECT operation'ı materyalize etmiyor — okuma tarafı olmayan bir kolona GROUP BY atar",
		},
		{
			name: "sıralama anahtarı",
			want: "ORDER BY (msg_system, cluster, destination, operation, time_bucket)",
			why:  "operation sıralama anahtarında değil — AggregatingMergeTree birleşmesi farklı operasyonları TEK satıra çökertir, boyut sessizce yok olur",
		},
		{
			name: "gruplama",
			want: "GROUP BY msg_system, cluster, destination, operation, time_bucket",
			why:  "GROUP BY'da operation yok — MV boyutu hiç yazmaz, DDL yine de hatasız çalışır",
		},
		{
			name: "filtre öznesi önde",
			want: "ORDER BY (msg_system,",
			why:  "msg_system PK önekinin başında değil — mevcut (system, cluster, destination) okumaları önek taramasını kaybeder",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(ddl, tc.want) {
				t.Errorf("messaging_summary_5m DDL'inde %q yok: %s", tc.want, tc.why)
			}
		})
	}

	// Zincir SIRASI: DDL'e gömülü coalesce, dependencies.go'daki
	// msgOperationExpr sabitiyle BİREBİR aynı anahtarları AYNI sırada
	// taramalı. Sabit paylaşımı mümkün değil (DDL ham backtick metni),
	// garanti buradan geliyor — topo_cluster_test.go'nun cluster zinciri
	// için kurduğu aynılık pininin operation ikizi.
	want := msgAttrKeys(msgOperationExpr)
	if len(want) != 3 {
		t.Fatalf("msgOperationExpr'den 3 anahtar bekleniyordu, %d çıktı (%v)", len(want), want)
	}
	got := msgAttrKeys(mvOperationChain(t, ddl))
	if len(got) != len(want) {
		t.Fatalf("DDL zinciri %d anahtar tarıyor, msgOperationExpr %d: %v vs %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("anahtar %d ayrıştı: DDL %q, msgOperationExpr %q — SIRA sözleşmenin parçası; "+
				"iki anahtarı birden basan span iki yüzeyde iki operasyona düşer", i, got[i], want[i])
		}
	}
	// Terminal literal: hiçbir anahtar yoksa satır BOŞ dizeye düşmeli ve
	// KALMALI. '(unknown)' gibi bir literal buraya girerse store katmanı
	// etiketleme kararını frontend'den çalar.
	chain := mvOperationChain(t, ddl)
	if !strings.Contains(chain, "''\n") && !strings.HasSuffix(strings.TrimSpace(chain), "''") {
		if !regexp.MustCompile(`,\s*''\s*\)`).MatchString(chain + ")") {
			t.Errorf("operation zinciri boş-dize terminaline düşmüyor: %q", chain)
		}
	}
	if strings.Contains(chain, "'(unknown)'") || strings.Contains(chain, "'(bilinmiyor)'") {
		t.Error("operation zinciri bir yer-tutucu etikete düşüyor — etiketleme frontend'in işi, MV ölçtüğünü yazar")
	}
}

// msgAttrKeys — bir coalesce zincirinden attr anahtarlarını SIRAYLA çıkarır.
func msgAttrKeys(expr string) []string {
	re := regexp.MustCompile(`indexOf\(attr_keys, '([^']+)'\)`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(expr, -1) {
		out = append(out, m[1])
	}
	return out
}

// mvOperationChain — `AS operation`tan GERİYE doğru en yakın `coalesce(`e
// giderek zinciri keser (mvClusterChain'in ikizi; ileriye bakan bir pencere
// sonraki ifadenin anahtarlarını yutardı).
func mvOperationChain(t *testing.T, ddl string) string {
	t.Helper()
	j := strings.Index(ddl, "AS operation")
	if j < 0 {
		t.Fatal("DDL'de `AS operation` yok")
	}
	head := ddl[:j]
	k := strings.LastIndex(head, "coalesce(")
	if k < 0 {
		t.Fatal("`AS operation` öncesi coalesce( yok")
	}
	return head[k:]
}

// TestMessagingCallerMVUnchanged — NEGATİF pin. Faz 4b yalnız
// messaging_summary_5m'i değiştirdi. caller MV'sine operation sızarsa
// (kopyala-yapıştır) o MV de boot'ta drop+recreate'e girer ve 90 günlük
// servis/pod kırılımı gereksiz yere silinir.
func TestMessagingCallerMVUnchanged(t *testing.T) {
	ddl := mvDDLBody(t, "messaging_caller_summary_5m")
	if strings.Contains(ddl, "AS operation") {
		t.Error("messaging_caller_summary_5m operation kazanmış — Faz 4b bu MV'ye DOKUNMUYOR; " +
			"boyut buraya sızarsa geçiş 90 günlük servis/pod kovalarını da siler")
	}
	for _, m := range mvDimMigrations {
		if m.Table == "messaging_caller_summary_5m" {
			t.Error("mvDimMigrations caller MV'sini drop+recreate ediyor — o MV'nin şeması değişmedi, geçiş saf veri kaybı olur")
		}
	}
}

// TestMVDimMigrationLedger — geçiş defteri. DDL değişip defter değişmezse
// mevcut kurulumlar eski şemada kalır: yeni okuma HATASIZ ama SIFIR satır
// döner, operatör "operasyon yok" diye okur.
func TestMVDimMigrationLedger(t *testing.T) {
	want := []mvDimMigration{
		{Table: "db_summary_5m", Column: "db_name"},
		{Table: "db_caller_summary_5m", Column: "db_name"},
		{Table: "messaging_summary_5m", Column: "operation"},
	}
	for _, w := range want {
		t.Run(w.Table+"/"+w.Column, func(t *testing.T) {
			found := false
			for _, m := range mvDimMigrations {
				if m.Table == w.Table && m.Column == w.Column {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("mvDimMigrations'ta (%s, %s) çifti yok — boot geçişi bu boyutu ASLA eklemez", w.Table, w.Column)
			}
		})
	}

	// Her Table katalogda çözülmeli, yoksa findMV boş DDL döndürür ve
	// drop'un partneri olmaz: tablo GİDER, yerine hiçbir şey gelmez.
	_, f := parseStoreGo(t)
	names, _ := mvCatalogue(t, f)
	inCatalogue := map[string]bool{}
	for _, n := range names {
		inCatalogue[n] = true
	}
	seen := map[string]bool{}
	for _, m := range mvDimMigrations {
		key := m.Table + "/" + m.Column
		if seen[key] {
			t.Errorf("mvDimMigrations'ta %s iki kez — aynı MV iki kez drop edilir", key)
		}
		seen[key] = true
		if !inCatalogue[m.Table] {
			t.Errorf("mvDimMigrations %q'yi işaret ediyor ama katalogda yok — execDDL boş DDL alır, MV silinip yerine hiçbir şey gelmez", m.Table)
		}
		if m.Column == "" || m.Dim == "" {
			t.Errorf("%s satırında Column/Dim boş — probe her boot'ta yanlış cevap verir", m.Table)
		}
	}
}

// TestMVDimNeedsMigration — NO-OP sözleşmesi, saf çekirdek üzerinde.
// Kolon zaten varsa (yerinde ALTER + MODIFY QUERY ile geçmiş kurulum)
// boot HİÇ dokunmamalı; aksi hâlde her restart 90 günlük kovaları siler.
func TestMVDimNeedsMigration(t *testing.T) {
	cases := []struct {
		name      string
		hasColumn bool
		want      bool
	}{
		{"kolon yok — eski şema, geçiş şart", false, true},
		{"kolon var — taze kurulum ya da yerinde ALTER, no-op", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mvDimNeedsMigration(tc.hasColumn); got != tc.want {
				t.Errorf("mvDimNeedsMigration(%v) = %v, beklenen %v", tc.hasColumn, got, tc.want)
			}
		})
	}
}

// TestMVDimMigrationWiring — saf çekirdeğin GERÇEKTEN kullanıldığının pini
// (v0.9.1334 dersi: yeşil çekirdek, bağlanmamış çağrı).
//
// PENCERE mvDimMigrations DÖNGÜSÜNÜN GÖVDESİYLE SINIRLI ve bu zorunlu:
// migrate() içinde başka geçişler de aynı probe iskeletini ve aynı log
// cümlesini taşıyor (`AND name     = '%s'` iki, "past 5-min buckets"
// beş yerde). Fonksiyonun tamamında arayan bir gate KOMŞU geçişin
// metnini kanıt sayardı ve bu döngü tamamen sökülse bile yeşil kalırdı —
// ölçülmüş bir tuzak, varsayım değil.
func TestMVDimMigrationWiring(t *testing.T) {
	body := mvDimLoopBody(t)

	cases := []struct {
		name string
		want string
		why  string
	}{
		{
			name: "saf karar",
			want: "mvDimNeedsMigration(hasCol != 0)",
			why:  "geçiş kararı saf çekirdekten geçmiyor — TestMVDimNeedsMigration yeşil kalırken üretim yolu ayrışabilir",
		},
		{
			name: "kolon parametreli probe",
			want: `AND name     = '%s'`,
			why:  "probe kolon adını PARAMETRELEMİYOR — defterdeki Column alanı dekoratif olur, her satır aynı kolonu arar",
		},
		{
			name: "kova kaybı ilanı",
			want: "past 5-min buckets will be dropped",
			why:  "geçiş log satırı kova kaybını ilan etmiyor — operatör sessiz bir veri kaybıyla karşılaşır",
		},
		{
			name: "cluster drop hedefi",
			want: `dropTarget = m.Table + "_local"`,
			why:  "cluster modunda drop hedefi _local değil — sarmalayıcı düşer, asıl MV kalır",
		},
		{
			// DAĞITIK GÜVENLİK (v0.10.563). Cluster modunda çıplak ad
			// Distributed sarmalayıcı ve KENDİ kolon listesini taşır;
			// adaptDDL onu `CREATE TABLE IF NOT EXISTS` ile kurar, yani
			// eskisi ayakta kalırsa recreate NO-OP olur: `_local` kolonu
			// kazanır, sarmalayıcı kazanmaz, çıplak addan gelen her SELECT
			// CH kod 47 verir (v0.8.162 sınıfı).
			name: "bayat sarmalayıcı düşürülüyor",
			want: `"DROP TABLE IF EXISTS "+m.Table+s.onCluster()+" SYNC"`,
			why:  "yeni boyut yalnız _local'e iner ve çıplak addan okuma kod 47 ile patlar (dağıtık-güvenlik regresyonu)",
		},
		{
			name: "ad ile çözüm",
			want: "findMV(m.Table)",
			why:  "MV dilim POZİSYONUYLA çözülüyor — v0.8.52 / v0.9.1319 indeks-kayması sınıfı",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(body, tc.want) {
				t.Errorf("mvDimMigrations döngüsünde %q yok: %s", tc.want, tc.why)
			}
		})
	}
}

// mvDimLoopBody — store.go'daki `for _, m := range mvDimMigrations {`
// döngüsünün GÖVDESİ, süslü parantez sayarak. Pencereyi kapatmak gate'in
// kendisi kadar önemli (bkz. yukarıdaki not).
func mvDimLoopBody(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	head := "for _, m := range mvDimMigrations {"
	i := strings.Index(src, head)
	if i < 0 {
		t.Fatal("migrate() mvDimMigrations defterini DÖNMÜYOR — defter dekoratif kalır, hiçbir geçiş koşmaz")
	}
	depth := 0
	for j := i + len(head) - 1; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[i : j+1]
			}
		}
	}
	t.Fatal("mvDimMigrations döngüsünün kapanışı bulunamadı")
	return ""
}

// TestMVDimLoopWindowIsBounded — META-KAPI. Yukarıdaki pencerenin
// GERÇEKTEN dar olduğunu ölçer: migrate()'in tamamı alınsaydı komşu
// geçişlerin metni kanıt sayılırdı. Varlık değil YOKLUK ölçülüyor.
func TestMVDimLoopWindowIsBounded(t *testing.T) {
	body := mvDimLoopBody(t)
	for _, foreign := range []string{
		"apdex_satisfied_state",           // v0.5.327 öncesi apdex geçişi
		"positionUTF8(create_table_query", // peer.service fallback geçişi
		"positionUTF8(type, 'TDigest')",   // v0.8.194 TDigest geçişi
		"operation_group_summary_5m",      // op_group muhafızı
	} {
		if strings.Contains(body, foreign) {
			t.Errorf("pencere komşu geçişe taşıyor (%q görünüyor) — gate bu döngü sökülse bile yeşil kalır", foreign)
		}
	}
	if n := strings.Count(body, "past 5-min buckets will be dropped"); n != 1 {
		t.Errorf("pencerede kova-kaybı cümlesi %d kez — 1 olmalı; taşan pencere komşu geçişin log satırını sayıyor", n)
	}
}

// TestMsgOperationREDSQLBounds — okuma sorgusunun CH sınırları (CLAUDE.md:
// zaman-sınırlı WHERE + LIMIT + max_execution_time) ve MV-first daveti.
func TestMsgOperationREDSQLBounds(t *testing.T) {
	q := msgOperationREDSQL
	cases := []struct {
		name string
		want string
		why  string
	}{
		{"MV kaynağı", "FROM messaging_summary_5m", "toplam ham spans'ten okunuyor — milyar-span ölçeğinde MV-first ihlali"},
		{"zaman alt sınırı", "time_bucket >= ?", "pencere alt sınırı yok — tüm 90 günlük TTL taranır"},
		{"zaman üst sınırı", "time_bucket < ?", "pencere üst sınırı yok"},
		{"özne", "msg_system = ?", "msg_system yüklemi yok — PK öneki kaybolur"},
		{"kırılım", "GROUP BY operation", "operation kırılımı yok — sorgunun amacı bu"},
		{"satır tavanı", "LIMIT 20", "LIMIT yok ya da değişti"},
		{"süre tavanı", "SETTINGS max_execution_time = 8", "max_execution_time yok — yavaş sorgu CH'yi tutar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(q, tc.want) {
				t.Errorf("msgOperationREDSQL %q içermiyor: %s", tc.want, tc.why)
			}
		})
	}
	if strings.Contains(q, "FROM spans") {
		t.Error("msgOperationREDSQL ham `spans` okuyor — bu bir toplam, MV zorunlu")
	}
	if strings.Contains(q, "coremetry.") {
		t.Error("sorgu `coremetry.` önekiyle nitelenmiş — telemetri tabloları NİTELİKSİZ yazılır")
	}
	// Hata sayacı bu MV'de countIfState; merge'ü countMerge. countIfMerge'e
	// kayarsa CH hata verir, sumMerge'e kayarsa SESSİZCE yanlış sayı döner.
	if !strings.Contains(q, "countMerge(error_count_state)") {
		t.Error("error_count_state countMerge ile birleştirilmiyor — kardeş okumalarla (getMessaging, GetMessagingDetail) aynı topic için iki farklı hata oranı çıkar")
	}
}

// TestMessagingDetailCarriesOperations — okumanın çekmeceye BAĞLI olduğunun
// pini. Metot tek başına yeşil olup hiçbir yüzeyden çağrılmazsa Faz 4b'nin
// frontend yarısı boş dizi görür ve sebebi görünmez olur.
func TestMessagingDetailCarriesOperations(t *testing.T) {
	b, err := os.ReadFile("dependencies.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "Operations []MsgOperationStat `json:\"operations\"`") {
		t.Error("MessagingDetail.Operations alanı yok — okuma yüzeye ulaşmaz")
	}
	if !strings.Contains(src, "s.MessagingOperationRED(ctx, system, cluster, destination, from, to)") {
		t.Error("GetMessagingDetail MessagingOperationRED'i ÇAĞIRMIYOR — test edilmiş ama ulaşılamaz okuma (v0.9.1334 sınıfı)")
	}
	if !strings.Contains(src, "Operations: []MsgOperationStat{}") {
		t.Error("Operations nil ile başlatılıyor — JSON'da `null` çıkar, frontend boş durumu ayırt edemez")
	}
	// Best-effort sözleşmesi: operasyon okuması çekmeceyi BLOKLAMAMALI.
	i := strings.Index(src, "s.MessagingOperationRED(ctx")
	if i < 0 {
		t.Fatal("çağrı bulunamadı")
	}
	window := src[maxInt(0, i-200) : i+200]
	if !strings.Contains(window, "err == nil") {
		t.Error("MessagingOperationRED hatası best-effort değil — bir MV hatası TÜM çekmeceyi düşürür")
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
