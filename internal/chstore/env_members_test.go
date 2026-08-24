package chstore

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// v0.8.387 — env-separation Phase 3: /problems consumes the global
// ?env= picker. Problems carry no env dimension, so the filter means
// "problems whose SERVICE ran in the selected env" via the 60s-cached
// 1h service→env map (the env twin of clusterMemberServices,
// v0.8.386). These tests pin (a) the map inversion, (b) the pure
// service-scope conjunct, and (c) the resolve-and-apply wrapper's
// soft-fail — all conn-less against a seeded map cache, the
// cluster_narrow_test.go pattern.

func seededEnvStore(m map[string][]string) *Store {
	s := &Store{}
	s.envMapVal = m
	s.envMapFor = time.Hour
	s.envMapAt = time.Now()
	return s
}

func TestEnvMemberServicesInversion(t *testing.T) {
	s := seededEnvStore(map[string][]string{
		"mobile-bff": {"int", "prep", "uat"}, // multi-env — member of each
		"payments":   {"uat"},
		"batch":      {"int"},
		// env-less infra (e.g. shared Oracle RAC) never enters the map:
		// GetServiceEnvMap excludes deploy_env = ''.
	})
	ctx := context.Background()

	cases := []struct {
		env  string
		want []string
	}{
		{"uat", []string{"mobile-bff", "payments"}}, // sorted
		{"int", []string{"batch", "mobile-bff"}},
		{"prep", []string{"mobile-bff"}},
		{"prod", []string{}}, // unknown env → authoritative EMPTY, not error
	}
	for _, tc := range cases {
		got, err := s.EnvMemberServices(ctx, tc.env)
		if err != nil {
			t.Fatalf("env=%s: unexpected error %v", tc.env, err)
		}
		if got == nil {
			t.Fatalf("env=%s: members must be non-nil (empty = authoritative)", tc.env)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("env=%s: got %v want %v", tc.env, got, tc.want)
		}
	}

	// Conn-less cold cache → ERROR (callers soft-fail to unfiltered),
	// never a silent empty set that would blank the triage page.
	bare := &Store{}
	if _, err := bare.EnvMemberServices(ctx, "uat"); err == nil {
		t.Fatal("cold conn-less store must return an error, not an empty set")
	}
}

func TestApplyEnvServiceScope(t *testing.T) {
	cases := []struct {
		name       string
		members    []string
		hasKindCol bool
		wantSQL    string
		wantArgs   []any
	}{
		// ── kind kolonu YOK (v0.9.1338 öncesi / kolonu ekleyen boot) ──
		// Davranış v0.8.387'nin birebir aynısı: o boot'ta db özneli
		// SATIR da yoktur, dolayısıyla istisnayı yazmamak doğrudur.
		{
			name:    "kind kolonu yok, üye yok — yalnız global satırlar",
			members: nil,
			wantSQL: "WHERE service = ''",
		},
		{
			name:     "kind kolonu yok, tek üye",
			members:  []string{"payments"},
			wantSQL:  "WHERE (service = '' OR service IN (?))",
			wantArgs: []any{"payments"},
		},
		{
			name:     "kind kolonu yok, iki üye (sıra yukarıda korunmuş)",
			members:  []string{"mobile-bff", "payments"},
			wantSQL:  "WHERE (service = '' OR service IN (?,?))",
			wantArgs: []any{"mobile-bff", "payments"},
		},
		// ── kind kolonu VAR (v0.9.1358 db kaçış kapısı) ──
		{
			name:       "kind kolonu var, üye yok — global + db",
			members:    nil,
			hasKindCol: true,
			wantSQL:    "WHERE (service = '' OR kind = 'db')",
		},
		{
			name:       "kind kolonu var, tek üye — üç disjunkt",
			members:    []string{"payments"},
			hasKindCol: true,
			wantSQL:    "WHERE (service = '' OR service IN (?) OR kind = 'db')",
			wantArgs:   []any{"payments"},
		},
		{
			name:       "kind kolonu var, iki üye",
			members:    []string{"mobile-bff", "payments"},
			hasKindCol: true,
			wantSQL:    "WHERE (service = '' OR service IN (?,?) OR kind = 'db')",
			wantArgs:   []any{"mobile-bff", "payments"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var wc whereClause
			applyEnvServiceScope(&wc, tc.members, tc.hasKindCol)
			if got := wc.sql(); got != tc.wantSQL {
				t.Fatalf("sql: got %q want %q", got, tc.wantSQL)
			}
			if len(tc.wantArgs) == 0 {
				if len(wc.args) != 0 {
					t.Fatalf("args: got %v want none", wc.args)
				}
				return
			}
			if !reflect.DeepEqual(wc.args, tc.wantArgs) {
				t.Fatalf("args: got %v want %v", wc.args, tc.wantArgs)
			}
		})
	}
}

// v0.9.1358 — /problems + /inbox'ın env süzgeci db ÖZNELİ problemleri
// sessizce eliyordu (database-entity-detail denetimi §2.4).
//
// SEMPTOM: `?env=prod` seçili her sayfada, `service` alanı bir
// DBSubjectID olan (`db:oracle@corebank-scan.prod`, v0.9.1338) TÜM
// problemler kayboluyordu — listeden de, rozetten de, şerit çipinden de.
// Kural "service boş VEYA env üyesi" idi; bir db öznesi ikisi de
// olamaz.
//
// Bu test AYIRT EDİCİ: db satırı GEÇMELİ, haritada olmayan gerçek bir
// SERVİS (env-siz altyapı) GİZLİ KALMALI. Yalnız birinci yön test
// edilseydi, muafiyeti "haritada olmayan her satır geçsin" diye
// gevşetmek de yeşil verirdi — o ise eski yorumun bilerek kapattığı
// hâli geri açardı.
func TestEnvScopeKeepsDBSubjectsHidesUnknownServices(t *testing.T) {
	members := map[string]bool{"payments": true, "mobile-bff": true}

	cases := []struct {
		name    string
		service string
		kind    string
		keep    bool
	}{
		{"db öznesi env seçiliyken GEÇER", "db:oracle@corebank-scan.prod", ProblemKindDB, true},
		{"db öznesi başka bir env adı taşısa da GEÇER", "db:oracle@corebank-scan.uat", ProblemKindDB, true},
		{"haritada olmayan SERVİS gizli kalır", "oracle-rac", ProblemKindService, false},
		{"haritada olmayan servis, kind boş (eski satır) — gizli", "oracle-rac", "", false},
		{"global (servissiz) satır geçer", "", "", true},
		{"üye servis geçer", "payments", ProblemKindService, true},
		{"çok-env'li üye geçer", "mobile-bff", ProblemKindService, true},
		// Kritik: db BİÇİMLİ bir ad taşıyıp kind'ı service olan satır
		// GEÇMEZ. Ayırt edici olan TİPLİ ÖZNE, dizenin öneki değil —
		// `db:` önekini elle sınamak ikinci bir kimlik yazımı olurdu.
		{"db biçimli ad ama kind=service — gizli", "db:oracle@x", ProblemKindService, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EnvScopeKeepsRow(tc.service, tc.kind, members); got != tc.keep {
				t.Errorf("EnvScopeKeepsRow(%q, %q) = %v, want %v",
					tc.service, tc.kind, got, tc.keep)
			}
		})
	}

	// Env hiçbir servise çözülmedi: global VE db satırları yine geçer,
	// servis satırları gitmiş olur.
	empty := map[string]bool{}
	if !EnvScopeKeepsRow("db:oracle@x", ProblemKindDB, empty) {
		t.Error("boş üye kümesi db öznesini gizlememeli — bir ortamın span'i olmaması veritabanı alarmını yok saymaz")
	}
	if !EnvScopeKeepsRow("", "", empty) {
		t.Error("boş üye kümesi global satırı gizlememeli")
	}
	if EnvScopeKeepsRow("payments", ProblemKindService, empty) {
		t.Error("boş üye kümesi servis satırlarını gizlemeli")
	}
}

// evalEnvConjunct — üretilen WHERE parçasını DİSJUNKT kümesine ayırır ve
// satırı o kümeden karara bağlar. Kuralı elle TEKRAR YAZMAZ: SQL'in kendi
// metninden okur, yani metinden bir disjunkt düşerse karar da düşer.
//
// Bilinmeyen bir disjunkt testi DÜŞÜRÜR — env kapsamına dördüncü bir
// kaçış kapısı eklemek, Go ikiziyle eşitlik kanıtını da güncellemeyi
// zorunlu kılsın diye.
func evalEnvConjunct(t *testing.T, conj string, members []string, service, kind string) bool {
	t.Helper()
	body := conj
	if strings.HasPrefix(body, "(") && strings.HasSuffix(body, ")") {
		body = body[1 : len(body)-1]
	}
	keep := false
	for _, d := range strings.Split(body, " OR ") {
		switch {
		case d == "service = ''":
			keep = keep || service == ""
		case strings.HasPrefix(d, "service IN ("):
			for _, m := range members {
				if m == service {
					keep = true
				}
			}
		case d == "kind = '"+ProblemKindDB+"'":
			// CH'nin gördüğü anlam: TAM dize eşitliği. Go ikizinin
			// ProblemSubjectKind normalizasyonu burada bilerek YOK —
			// varsa, eşitlik iddiası totolojiye düşerdi.
			keep = keep || kind == ProblemKindDB
		default:
			t.Fatalf("bilinmeyen disjunkt %q — env kapsamına yeni bir kaçış "+
				"kapısı eklendi ama Go ikiziyle (EnvScopeKeepsRow) eşitlik "+
				"kanıtı güncellenmedi", d)
		}
	}
	return keep
}

// TestEnvScopeSQLAndGoAgree — İKİ uygulamanın eşitliğini KANITLAR.
//
// v0.8.387'den beri iki yorum da "ayrışamazlar" yazıyordu; ayrışmayı
// engelleyen tek şey o düzyazıydı ve v0.9.1338'de db özneleri gelince
// İKİSİ BİRDEN yanlışa düştü (v0.9.1358 denetimi). Artık Go tarafı TEK
// gövde (EnvScopeKeepsRow) ve SQL tarafı bu testte ona karşı ölçülüyor.
func TestEnvScopeSQLAndGoAgree(t *testing.T) {
	memberSets := [][]string{
		nil,
		{"payments"},
		{"mobile-bff", "payments"},
	}
	rows := []struct{ service, kind string }{
		{"", ""},
		{"", ProblemKindService},
		{"payments", ProblemKindService},
		{"mobile-bff", ProblemKindService},
		{"oracle-rac", ProblemKindService},
		{"oracle-rac", ""},
		{"db:oracle@corebank-scan.prod", ProblemKindDB},
		{"db:oracle@x", ProblemKindService},
	}
	for _, hasKindCol := range []bool{true, false} {
		for _, members := range memberSets {
			conj := envScopeConjunct(len(members), hasKindCol)
			set := map[string]bool{}
			for _, m := range members {
				set[m] = true
			}
			for _, r := range rows {
				// hasKindCol=false = kolonu EKLEYEN boot. O boot'ta db
				// özneli SATIR yazılamaz (db_capacity.go kind'ı hiç
				// yazmaz), yani SQL'in db satırını gizlemesi ölçülebilir
				// bir fark değil — var olmayan satır üstünde eşitlik
				// iddia etmek uydurma olurdu.
				if !hasKindCol && ProblemSubjectKind(r.kind) == ProblemKindDB {
					continue
				}
				sqlKeep := evalEnvConjunct(t, conj, members, r.service, r.kind)
				goKeep := EnvScopeKeepsRow(r.service, r.kind, set)
				if sqlKeep != goKeep {
					t.Errorf("AYRIŞMA hasKindCol=%v members=%v satır=(%q,%q): SQL=%v Go=%v\nconj=%s",
						hasKindCol, members, r.service, r.kind, sqlKeep, goKeep, conj)
				}
			}
		}
	}
}

// TestProblemCountWhere — rozet (CountProblemsNotInStatuses) + şerit
// çipi (CountProblemsBySubject) sayımlarının ORTAK WHERE gövdesi
// (v0.9.1358). Bu ikisi whereClause kullanmıyor; listeyi düzeltip
// sayıları eski hâlinde bırakmak, operatöre açtığı sayfayla çelişen bir
// rozet verirdi.
//
// Probe'un (hasProblemKindCol) BURADA okunması bilinçli ve bu test onu
// pinliyor: mutasyon turu, probe çağıranın parametresiyken iki sayım
// yüzeyinin de `false` geçebildiğini ve HİÇBİR testin ısırmadığını
// ölçtü.
func TestProblemCountWhere(t *testing.T) {
	withKind := &Store{hasProblemKindCol: true}
	noKind := &Store{}

	// nil envServices = kısıt yok → yalnız statü kısmı.
	if sql, args := withKind.problemCountWhere(nil, nil); sql != "1" || len(args) != 0 {
		t.Fatalf("kısıtsız sayım: %q %v", sql, args)
	}
	if sql, _ := withKind.problemCountWhere([]string{"resolved", "ignored"}, nil); sql != "status NOT IN (?,?)" {
		t.Fatalf("yalnız statü: %q", sql)
	}

	// Boş dilim KISITTIR ve db kaçış kapısını taşır.
	sql, args := withKind.problemCountWhere(nil, []string{})
	if sql != "1 AND (service = '' OR kind = 'db')" {
		t.Fatalf("boş env için: %q", sql)
	}
	if len(args) != 0 {
		t.Fatalf("boş env için arg olmamalı: %v", args)
	}

	// Üyeli hâl: sayımın env kısmı /problems'ın WHERE'iyle AYNI dize.
	members := []string{"mobile-bff", "payments"}
	sql, args = withKind.problemCountWhere([]string{"resolved"}, members)
	var wc whereClause
	applyEnvServiceScope(&wc, members, true)
	envPart := strings.TrimPrefix(wc.sql(), "WHERE ")
	if want := "status NOT IN (?) AND " + envPart; sql != want {
		t.Fatalf("sayım WHERE'i listeden ayrışıyor:\n sayım: %q\n liste: %q", sql, want)
	}
	// Argüman SIRASI: önce statü dışlamaları, sonra env üyeleri.
	if !reflect.DeepEqual(args, []any{"resolved", "mobile-bff", "payments"}) {
		t.Fatalf("arg sırası: %v", args)
	}

	// Kind kolonu yokken (iki-boot sözleşmesi) eski davranış birebir.
	if sql, _ := noKind.problemCountWhere(nil, []string{"payments"}); sql != "1 AND (service = '' OR service IN (?))" {
		t.Fatalf("kind kolonu yokken eski yazım korunmalı: %q", sql)
	}
	if sql, _ := noKind.problemCountWhere(nil, []string{}); sql != "1 AND service = ''" {
		t.Fatalf("kind kolonu yok + üyesiz env: %q", sql)
	}
}

func TestEnvScopeProblems(t *testing.T) {
	ctx := context.Background()
	s := seededEnvStore(map[string][]string{
		"payments": {"uat"},
	})

	// env unset → no-op.
	var wc whereClause
	wc.add("status = ?", "open")
	s.envScopeProblems(ctx, &wc, "")
	if got := wc.sql(); got != "WHERE status = ?" {
		t.Fatalf("env-less filter must be untouched: %q", got)
	}

	// env set → conjunct ANDed after the existing filters, service=''
	// carve-out included (global log-query problems always show).
	var wc2 whereClause
	wc2.add("status = ?", "open")
	s.envScopeProblems(ctx, &wc2, "uat")
	want := "WHERE status = ? AND (service = '' OR service IN (?))"
	if got := wc2.sql(); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if wc2.args[len(wc2.args)-1] != "payments" {
		t.Fatalf("member arg missing: %v", wc2.args)
	}

	// Unknown env → authoritative empty membership: only global rows.
	var wc3 whereClause
	s.envScopeProblems(ctx, &wc3, "prod")
	if got := wc3.sql(); got != "WHERE service = ''" {
		t.Fatalf("unknown env must keep only global rows: %q", got)
	}

	// Cold conn-less map (resolve error) → SOFT-FAIL unfiltered:
	// a transient map blip must never hide firing problems, and
	// list/count both soft-fail the same way so they still agree.
	bare := &Store{}
	var wc4 whereClause
	bare.envScopeProblems(ctx, &wc4, "uat")
	if strings.Contains(wc4.sql(), "service") {
		t.Fatalf("map error must not filter: %q", wc4.sql())
	}

	// v0.9.1358 — kind kolonu VARSA envScopeProblems db kaçış kapısını
	// SQL'e indirmeli. Probe'u okumayı unutan bir sürüm burada düşer;
	// yukarıdaki dallar hasProblemKindCol=false olduğu için sessiz
	// kalırdı (varsayılan girdinin kusurlu dalı gizlemesi sınıfı).
	withKind := seededEnvStore(map[string][]string{"payments": {"uat"}})
	withKind.hasProblemKindCol = true
	var wc5 whereClause
	withKind.envScopeProblems(ctx, &wc5, "uat")
	if got := wc5.sql(); got != "WHERE (service = '' OR service IN (?) OR kind = 'db')" {
		t.Fatalf("kind kolonu varken db kaçış kapısı SQL'e inmiyor: %q", got)
	}
	// Ve env hiçbir servise çözülmediğinde de.
	var wc6 whereClause
	withKind.envScopeProblems(ctx, &wc6, "prod")
	if got := wc6.sql(); got != "WHERE (service = '' OR kind = 'db')" {
		t.Fatalf("üyesiz env db satırlarını da öldürüyor: %q", got)
	}
}

// v0.8.389 — operator-reported: feature-branch envs pushed the set
// past the alphabetical LIMIT 50, so "release" never surfaced and
// search found nothing. Pins the enumeration window rule: cheap 1h
// clamp unsearched, 24h reach for explicit searches.
func TestEnvEnumWindow(t *testing.T) {
	to := time.Now()
	deepFrom := to.Add(-72 * time.Hour)

	if got := envEnumWindow(deepFrom, to, ""); !got.Equal(to.Add(-time.Hour)) {
		t.Fatalf("unsearched clamp: got %v, want to-1h", got)
	}
	if got := envEnumWindow(deepFrom, to, "release"); !got.Equal(to.Add(-24 * time.Hour)) {
		t.Fatalf("searched clamp: got %v, want to-24h", got)
	}
	// A window narrower than the clamp is respected as-is.
	narrow := to.Add(-10 * time.Minute)
	if got := envEnumWindow(narrow, to, "release"); !got.Equal(narrow) {
		t.Fatalf("narrow window must pass through: got %v", got)
	}
	// Whitespace-only q = no search.
	if got := envEnumWindow(deepFrom, to, "   "); !got.Equal(to.Add(-time.Hour)) {
		t.Fatalf("blank q must keep the 1h clamp: got %v", got)
	}
}
