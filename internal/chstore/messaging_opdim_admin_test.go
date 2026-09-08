package chstore

// messaging_opdim_admin_test.go — v0.10.564 (messaging_summary_5m
// `operation` boyutunun YERİNDE geçiş sihirbazı).
//
// SÖZLEŞME: bu sihirbaz kolonu boot geçişinden ÖNCE var etmek için var.
// Ürettiği ifadelerden biri düşerse ya da yanlış üretilirse maliyet
// sessiz DEĞİL ama pahalı: bir sonraki deploy MV'yi DROP+RECREATE eder ve
// 90 günlük messaging kovaları gider. Testler dört ayrı yanlış-üretimi
// çiviliyor:
//
//  1. MODIFY ORDER BY'ın ALTER'dan düşmesi → AggregatingMergeTree
//     birleşmesi farklı operasyonları TEK satıra çökertir, boyut yok olur.
//  2. Küme kipinde Distributed sarmalayıcı ALTER'ının düşmesi → çıplak ad
//     kolonu görmez, boot probe'u YİNE drop eder (v0.10.563 dersi).
//  3. MODIFY QUERY'nin SELECT'inin küme kipinde `FROM spans` kalması →
//     her shard'ın MV'si global tabloyu okur, çift sayım.
//  4. Apply'ın ön kontrolden GEÇMEDEN koşması → desteklenmeyen kurulumda
//     ham DDL basılır.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/config"
)

// identityAdapt — tek-node adaptDDL'in ikizi (girdi aynen döner).
func identityAdapt(sql string) []string { return []string{sql} }

// fakeMVDDL — saf üretici testleri için kısa, kanonikle AYNI ŞEKİLLİ DDL.
// Gerçek katalog metni ayrı bir testte (TestMessagingOpDimUsesCanonicalDDL)
// kullanılıyor; burada kısa metin kasıtlı: üreticinin ŞEKLİNİ ölçüyoruz.
const fakeMVDDL = "CREATE MATERIALIZED VIEW IF NOT EXISTS messaging_summary_5m\n" +
	" ENGINE = AggregatingMergeTree\n" +
	" ORDER BY (msg_system, cluster, destination, operation, time_bucket)\n" +
	" AS SELECT\n" +
	"   msg_system,\n" +
	"   coalesce(nullIf(attr_values[indexOf(attr_keys, 'messaging.operation.type')], ''), '') AS operation,\n" +
	"   countState() AS span_count_state\n" +
	" FROM spans\n" +
	" WHERE msg_system != ''\n" +
	" GROUP BY msg_system, cluster, destination, operation, time_bucket"

func TestMessagingOpDimStatements(t *testing.T) {
	cases := []struct {
		name        string
		onCluster   string
		mvStorage   string
		wrapper     string
		inners      []string
		innerDone   map[string]bool
		queryDone   bool
		wrapperDone bool
		wantCount   int
		wantAll     []string // her ifadede aranmayacak; birleşik metinde aranır
		wantNone    []string
	}{
		{
			name:      "tek-node: 1 inner ALTER + 1 MODIFY QUERY",
			onCluster: "",
			mvStorage: "messaging_summary_5m",
			wrapper:   "", // tek-node'da Distributed sarmalayıcı YOK
			inners:    []string{".inner_id.aaa"},
			wantCount: 2,
			wantAll: []string{
				"ALTER TABLE `.inner_id.aaa` ADD COLUMN IF NOT EXISTS operation String DEFAULT '' AFTER destination",
				"MODIFY ORDER BY (msg_system, cluster, destination, time_bucket, operation)",
				"ALTER TABLE messaging_summary_5m MODIFY QUERY SELECT",
				"AS operation",
				"FROM spans",
			},
			wantNone: []string{"ON CLUSTER"},
		},
		{
			name:      "küme: 2 inner ALTER + MODIFY QUERY + sarmalayıcı ALTER",
			onCluster: " ON CLUSTER `c1`",
			mvStorage: "messaging_summary_5m_local",
			wrapper:   "messaging_summary_5m",
			inners:    []string{".inner_id.aaa", ".inner_id.bbb"},
			wantCount: 4,
			wantAll: []string{
				"ALTER TABLE `.inner_id.aaa` ON CLUSTER `c1` ADD COLUMN",
				"ALTER TABLE `.inner_id.bbb` ON CLUSTER `c1` ADD COLUMN",
				"ALTER TABLE messaging_summary_5m_local ON CLUSTER `c1` MODIFY QUERY SELECT",
				"ALTER TABLE messaging_summary_5m ON CLUSTER `c1` ADD COLUMN IF NOT EXISTS operation String DEFAULT '' AFTER destination",
			},
		},
		{
			name:      "innerDone: uygulanmış uuid atlanır",
			onCluster: "",
			mvStorage: "messaging_summary_5m",
			inners:    []string{".inner_id.aaa", ".inner_id.bbb"},
			innerDone: map[string]bool{".inner_id.aaa": true},
			wantCount: 2, // yalnız bbb + MODIFY QUERY
			wantNone:  []string{".inner_id.aaa"},
		},
		{
			name:      "queryDone: MODIFY QUERY atlanır",
			onCluster: "",
			mvStorage: "messaging_summary_5m",
			inners:    []string{".inner_id.aaa"},
			queryDone: true,
			wantCount: 1,
			wantNone:  []string{"MODIFY QUERY"},
		},
		{
			name:        "wrapperDone: sarmalayıcı ALTER atlanır",
			onCluster:   " ON CLUSTER `c1`",
			mvStorage:   "messaging_summary_5m_local",
			wrapper:     "messaging_summary_5m",
			inners:      []string{".inner_id.aaa"},
			wrapperDone: true,
			wantCount:   2,
			wantNone:    []string{"ALTER TABLE messaging_summary_5m ON CLUSTER"},
		},
		{
			name:        "hepsi uygulanmış: sıfır ifade",
			onCluster:   " ON CLUSTER `c1`",
			mvStorage:   "messaging_summary_5m_local",
			wrapper:     "messaging_summary_5m",
			inners:      []string{".inner_id.aaa"},
			innerDone:   map[string]bool{".inner_id.aaa": true},
			queryDone:   true,
			wrapperDone: true,
			wantCount:   0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := messagingOpDimStatements(tc.onCluster, tc.mvStorage, tc.wrapper, tc.inners,
				fakeMVDDL, identityAdapt, tc.innerDone, tc.queryDone, tc.wrapperDone)
			if err != nil {
				t.Fatalf("beklenmeyen hata: %v", err)
			}
			if len(got) != tc.wantCount {
				t.Fatalf("%d ifade bekleniyordu, %d geldi:\n%s", tc.wantCount, len(got), strings.Join(got, "\n---\n"))
			}
			all := strings.Join(got, "\n")
			for _, want := range tc.wantAll {
				if !strings.Contains(all, want) {
					t.Errorf("üretilen SQL %q içermiyor:\n%s", want, all)
				}
			}
			for _, none := range tc.wantNone {
				if strings.Contains(all, none) {
					t.Errorf("üretilen SQL %q İÇERİYOR (içermemeliydi):\n%s", none, all)
				}
			}
		})
	}
}

// TestMessagingOpDimStatementOrder — sıra sözleşmesi. Sarmalayıcının kolonu
// MODIFY QUERY'den ÖNCE ilan etmesi, `_local`'in henüz yayınlamadığı bir
// kolonu duyurmak demektir; kolonun depo tablosuna eklenmesi de MODIFY
// QUERY'den önce gelmek ZORUNDA (yoksa CH kod 47).
func TestMessagingOpDimStatementOrder(t *testing.T) {
	got, err := messagingOpDimStatements(" ON CLUSTER `c1`", "messaging_summary_5m_local",
		"messaging_summary_5m", []string{".inner_id.aaa"}, fakeMVDDL, identityAdapt, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("3 ifade bekleniyordu, %d", len(got))
	}
	if !strings.Contains(got[0], ".inner_id.aaa") {
		t.Errorf("1. ifade depo tablosu ALTER'ı değil: %s", got[0])
	}
	if !strings.Contains(got[1], "MODIFY QUERY") {
		t.Errorf("2. ifade MODIFY QUERY değil: %s", got[1])
	}
	if !strings.HasPrefix(got[2], "ALTER TABLE messaging_summary_5m ON CLUSTER") {
		t.Errorf("3. ifade Distributed sarmalayıcı ALTER'ı değil: %s", got[2])
	}
}

// TestMessagingOpDimUsesCanonicalDDL — SELECT metni store.go kataloğundan
// gelmeli, elle yazılmamalı. İkinci bir kopya yazılırsa iki gövde ayrışır
// ve MODIFY QUERY, taze kurulumun MV'sinden FARKLI bir MV üretir
// (aynı ada sahip iki şema — kimse fark etmez).
func TestMessagingOpDimUsesCanonicalDDL(t *testing.T) {
	canonical := canonicalMVDDL(messagingOpDimMV)
	if canonical == "" {
		t.Fatal("canonicalMVDDL(messaging_summary_5m) boş — katalog erişimi kırılmış")
	}
	// Katalogla AST'ten okunan gövde AYNI olmalı: iki erişim yolu tek gövdeyi
	// göstermezse sihirbaz ile boot geçişi farklı SQL basar.
	if astBody := mvDDLBody(t, messagingOpDimMV); astBody != canonical {
		t.Error("canonicalMVDDL ile store.go kataloğundaki gövde AYRIŞTI — sihirbaz ve boot farklı SELECT basar")
	}

	sel, err := messagingOpDimSelect(identityAdapt(canonical))
	if err != nil {
		t.Fatalf("SELECT çıkarılamadı: %v", err)
	}
	if !strings.HasPrefix(sel, "SELECT") {
		t.Errorf("çıkarılan gövde SELECT ile başlamıyor: %.60q", sel)
	}
	if strings.Contains(sel, "CREATE MATERIALIZED VIEW") {
		t.Error("çıkarılan gövde hâlâ CREATE başlığını taşıyor — MODIFY QUERY sözdizimi hatası verir")
	}
	if strings.Contains(sel, "ENGINE =") || strings.Contains(sel, "PARTITION BY") {
		t.Error("çıkarılan gövde motor/partition yan tümcelerini taşıyor — `AS SELECT` ayırıcısı kaymış")
	}
	for _, want := range []string{
		") AS operation,",
		"GROUP BY msg_system, cluster, destination, operation, time_bucket",
		"FROM spans",
	} {
		if !strings.Contains(sel, want) {
			t.Errorf("çıkarılan SELECT %q içermiyor — yeni boyut MV'ye yazılmaz", want)
		}
	}
}

// TestMessagingOpDimSelectPicksTheMVStatement — küme kipinde GERÇEK
// adaptDDL iki ifade döner; SELECT MV olanından çıkmalı ve `FROM
// spans_local`e dönmüş olmalı. "İlk elemanı al" kısayolu bugün doğru
// cevabı verir ama adaptDDL sırası değişirse sessizce sarmalayıcıyı seçer.
func TestMessagingOpDimSelectPicksTheMVStatement(t *testing.T) {
	s := &Store{cfg: config.CHConfig{ClusterName: "c1", ReplicaPath: "/ch/tbl"}}
	canonical := canonicalMVDDL(messagingOpDimMV)
	adapted := s.adaptDDL(canonical)
	if len(adapted) != 2 {
		t.Fatalf("küme kipinde 2 ifade bekleniyordu (MV + Distributed sarmalayıcı), %d geldi", len(adapted))
	}
	sel, err := messagingOpDimSelect(adapted)
	if err != nil {
		t.Fatalf("SELECT çıkarılamadı: %v", err)
	}
	if !strings.Contains(sel, "FROM spans_local") {
		t.Error("küme kipinde SELECT `FROM spans_local` okumuyor — her shard'ın MV'si global tabloyu okur, çift sayım")
	}
	if strings.Contains(sel, "ENGINE = Distributed") {
		t.Error("SELECT Distributed sarmalayıcı ifadesinden çıkarılmış — MV ifadesi seçilmeliydi")
	}

	// Ters sıra da aynı sonucu vermeli: seçim SIRAYA değil ŞEKLE bakıyor.
	rev := []string{adapted[1], adapted[0]}
	sel2, err := messagingOpDimSelect(rev)
	if err != nil || sel2 != sel {
		t.Errorf("ifade sırası değişince SELECT değişti — seçim pozisyona bağlı (err=%v)", err)
	}

	// ÇATIŞAN İKİNCİ GİRDİ (ölçüldü: bu vaka olmadan "CREATE MATERIALIZED
	// VIEW" muhafızını silen mutasyon HAYATTA KALIYOR — bugünkü Distributed
	// sarmalayıcısında `AS SELECT` yok, yani `AS SELECT` regex'i muhafızı
	// gölgeliyor). `AS SELECT` taşıyan MV-OLMAYAN bir ifade önde durduğunda
	// muhafızın gerçekten ısırdığını ölçer.
	decoy := "CREATE TABLE IF NOT EXISTS decoy ENGINE = MergeTree ORDER BY a AS SELECT 1 AS yanlis"
	sel3, err := messagingOpDimSelect([]string{decoy, adapted[0]})
	if err != nil {
		t.Fatalf("çeldirici önde: %v", err)
	}
	if sel3 != sel {
		t.Errorf("MV-olmayan ama `AS SELECT` taşıyan ifade seçildi — MODIFY QUERY MV'nin SELECT'i yerine yabancı bir gövde basar:\n%.80q", sel3)
	}
}

// TestMessagingOpDimSelectMissing — `AS SELECT` yoksa sessizce boş dize
// dönmemeli; MODIFY QUERY boş gövdeyle basılırsa MV'nin SELECT'i silinir.
func TestMessagingOpDimSelectMissing(t *testing.T) {
	if _, err := messagingOpDimSelect([]string{"CREATE TABLE x (a String) ENGINE = MergeTree ORDER BY a"}); err == nil {
		t.Error("MV içermeyen listede hata bekleniyordu")
	}
	if _, err := messagingOpDimSelect([]string{"CREATE MATERIALIZED VIEW IF NOT EXISTS m TO t"}); err == nil {
		t.Error("`AS SELECT` içermeyen MV'de hata bekleniyordu")
	}
	// Üretici de hatayı YUTMAMALI.
	if _, err := messagingOpDimStatements("", "m", "", []string{".inner_id.a"},
		"CREATE MATERIALIZED VIEW IF NOT EXISTS m TO t", identityAdapt, nil, false, false); err == nil {
		t.Error("messagingOpDimStatements SELECT çıkarma hatasını yutuyor — boş MODIFY QUERY basılırdı")
	}
}

func TestMessagingOpDimOnCluster(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"boş — tek-node", "", "", false},
		{"boşluklu boş", "   ", "", false},
		{"normal ad", "ch_cluster", " ON CLUSTER `ch_cluster`", false},
		{"nokta ve tire", "prod.ch-1", " ON CLUSTER `prod.ch-1`", false},
		{"noktalı virgül — ifade enjeksiyonu", "c1; DROP TABLE spans", "", true},
		{"backtick", "c`1", "", true},
		{"boşluk", "c 1", "", true},
		{"65 karakter", strings.Repeat("a", 65), "", true},
		{"64 karakter — sınır", strings.Repeat("a", 64), " ON CLUSTER `" + strings.Repeat("a", 64) + "`", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := messagingOpDimOnCluster(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Errorf("hata bekleniyordu, %q geldi", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("beklenmeyen hata: %v", err)
			}
			if got != tc.want {
				t.Errorf("= %q, beklenen %q", got, tc.want)
			}
		})
	}
}

// TestMessagingOpDimHalfDone — "kolon var, anahtar yok" YENİDEN
// KOŞULAMAZ bir durumdur ve ayrı raporlanmalı. CH mevcut bir kolonu
// sıralama anahtarına almaz, yani birleşik ALTER'ı yeniden basmak da
// düşer; sihirbaz bunu ön kontrolde söylemezse operatör apply ortasında
// ham bir CH hatasıyla karşılaşır ve neden düştüğünü bilemez.
func TestMessagingOpDimHalfDone(t *testing.T) {
	cases := []struct {
		name      string
		hasCol    bool
		keyHasCol bool
		want      bool
	}{
		{"hiç dokunulmamış — normal geçiş", false, false, false},
		{"tamamlanmış", true, true, false},
		{"YARIM: kolon var, anahtar yok", true, false, true},
		{"anahtar var kolon yok — imkânsız, yarım sayılmaz", false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := messagingOpDimHalfDone(tc.hasCol, tc.keyHasCol); got != tc.want {
				t.Errorf("messagingOpDimHalfDone(%v, %v) = %v, beklenen %v", tc.hasCol, tc.keyHasCol, got, tc.want)
			}
		})
	}
}

func TestMessagingOpDimState(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"boş", nil, "unknown"},
		{"hepsi ok", []string{"ok", "ok", "ok", "ok"}, "done"},
		{"hepsi missing", []string{"missing", "missing"}, "missing"},
		{"karışık", []string{"ok", "missing"}, "partial"},
		{"partial içeren", []string{"ok", "partial", "ok"}, "partial"},
		{"tek unknown her şeyi unknown yapar", []string{"ok", "ok", "unknown"}, "unknown"},
		{"unknown + missing", []string{"missing", "unknown"}, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := messagingOpDimState(tc.in); got != tc.want {
				t.Errorf("messagingOpDimState(%v) = %q, beklenen %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestMessagingOpDimApplyRunsPreflightFirst — KAYNAK PİNİ (v0.9.1334 sınıfı:
// saf çekirdek yeşil, çağrıldığı yer pinlenmemiş). Apply, ön kontrolden
// GEÇMEDEN hiçbir DDL basmamalı: HTTP katmanı da ayrıca kontrol ediyor ama
// bu metot test/MCP gibi başka çağrı yollarına da açık.
//
// AST kullanılıyor, metin penceresi değil: `execStmtsStopOnError` bu pakette
// üç sihirbaz tarafından çağrılıyor, dosya-geneli bir grep komşu fonksiyonun
// çağrısını kanıt sayardı ([[feedback-gate-window-runs-into-next-func]]).
func TestMessagingOpDimApplyRunsPreflightFirst(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "messaging_opdim_admin.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var body *ast.BlockStmt
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if ok && fd.Name.Name == "MessagingOpDimApply" && fd.Recv != nil {
			body = fd.Body
		}
	}
	if body == nil {
		t.Fatal("MessagingOpDimApply bulunamadı — yeniden adlandırıldıysa bu pin GÜNCELLENMELİ, silinmemeli")
	}
	prePos, execPos := token.NoPos, token.NoPos
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "MessagingOpDimPreflight":
			if !prePos.IsValid() {
				prePos = call.Pos()
			}
		case "execStmtsStopOnError":
			if !execPos.IsValid() {
				execPos = call.Pos()
			}
		}
		return true
	})
	if !prePos.IsValid() {
		t.Fatal("MessagingOpDimApply ön kontrolü ÇAĞIRMIYOR — desteklenmeyen kurulumda ham DDL basar")
	}
	if !execPos.IsValid() {
		t.Fatal("MessagingOpDimApply hiçbir ifade koşmuyor — sihirbaz dekoratif")
	}
	if execPos < prePos {
		t.Error("DDL yürütmesi ön kontrolden ÖNCE geliyor — kapı ısırmıyor")
	}
	// Ön kontrolün SONUCU okunmalı; yalnız çağırıp yok saymak da geçer görünür.
	src := body
	found := false
	ast.Inspect(src, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "Supported" {
			found = true
		}
		return true
	})
	if !found {
		t.Error("ön kontrolün Supported alanı okunmuyor — sonuç yok sayılıyor")
	}
}

// TestMessagingOpDimConstantsMatchTheMV — sabitler MV'nin GERÇEK şemasına
// bağlı. `destination` çapası ya da anahtar öneki DDL'de değişirse ALTER
// çalışma anında düşer; burada derleme anında yakalanır.
func TestMessagingOpDimConstantsMatchTheMV(t *testing.T) {
	ddl := canonicalMVDDL(messagingOpDimMV)
	if !strings.Contains(ddl, ") AS "+messagingOpDimAnchor+",") {
		t.Errorf("MV'de `AS %s` kolonu yok — `ADD COLUMN ... AFTER %s` düşer", messagingOpDimAnchor, messagingOpDimAnchor)
	}
	if !strings.Contains(ddl, messagingOpDimQueryMark) {
		t.Errorf("MV DDL'i %q taşımıyor — MODIFY QUERY probe'u HİÇBİR ZAMAN tamam demez", messagingOpDimQueryMark)
	}
	// MODIFY ORDER BY yalnız SONA ekleyebilir: yerinde geçen anahtarın
	// ÖNEKİ taze DDL'inkiyle aynı kalmalı, yoksa mevcut okumalar önek
	// taramasını kaybeder.
	if !strings.HasPrefix(messagingOpDimKey, "(msg_system, cluster, destination,") {
		t.Errorf("yerinde anahtar %q (msg_system, cluster, destination, …) önekiyle başlamıyor — mevcut okumalar önek taramasını kaybeder", messagingOpDimKey)
	}
	if !strings.HasSuffix(messagingOpDimKey, ", "+messagingOpDimCol+")") {
		t.Errorf("yerinde anahtar %q `%s` ile BİTMİYOR — MODIFY ORDER BY yalnız sona ekleyebilir", messagingOpDimKey, messagingOpDimCol)
	}
	if !strings.Contains(ddl, "ORDER BY (msg_system, cluster, destination, operation, time_bucket)") {
		t.Error("taze DDL'in anahtarı değişmiş — yerinde geçişin bilinen ayrışma notu (dosya başlığı) güncellenmeli")
	}
}
