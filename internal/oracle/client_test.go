package oracle

// client_test.go — v0.10.580, Oracle AŞAMA 1.
//
// buildSampleQuery SAF ve testin doğrudan pinlediği seam. Üç sözleşme
// burada çivileniyor, üçü de audit §3'ün maddeleri:
//
//  1. YALNIZ SELECT — üretilen metin tek bir sorgudur.
//  2. DEĞERLER daima bind — zaman ve tip değerleri metne GİRMEZ. Testin
//     tip değeri bilerek tırnak taşıyor: interpolasyona kayarsa metin
//     bozulur ve test düşer.
//  3. Identifier'lar tırnaksız interpole olur, dolayısıyla builder
//     Normalize'ın koştuğuna GÜVENMEZ — kendisi de doğrular.

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func cfgFor(t *testing.T) SourceConfig {
	t.Helper()
	out, err := Normalize(one(base()), Settings{}, NewSourceID)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	return out.Sources[0]
}

var bindRe = regexp.MustCompile(`:(\d+)`)

// forbiddenStatements — SQL'de ASLA olmaması gereken ifade türleri.
// Sözcük sınırı şart: `delete_flag` bir kolon adıdır, DELETE değil.
var forbiddenStatements = []string{
	"insert", "update", "delete", "merge", "drop", "alter", "truncate",
	"grant", "revoke", "create", "begin", "declare", "commit", "execute",
}

func assertSingleSelect(t *testing.T, sqlText string) {
	t.Helper()
	if !strings.HasPrefix(sqlText, "SELECT ") {
		t.Fatalf("SELECT ile başlamıyor: %q", sqlText)
	}
	if strings.Contains(sqlText, ";") {
		t.Fatalf("noktalı virgül var (ikinci ifade kapısı): %q", sqlText)
	}
	if strings.Contains(sqlText, "--") || strings.Contains(sqlText, "/*") {
		t.Fatalf("yorum başlatıcı var: %q", sqlText)
	}
	low := strings.ToLower(sqlText)
	if n := strings.Count(low, "select"); n != 1 {
		t.Fatalf("tek SELECT bekleniyordu, %d bulundu: %q", n, sqlText)
	}
	for _, kw := range forbiddenStatements {
		if regexp.MustCompile(`\b` + kw + `\b`).MatchString(low) {
			t.Fatalf("%q ifadesi üretildi: %q", kw, sqlText)
		}
	}
}

// assertEveryValueIsBound — args'taki HER değer yalnız bind ile taşınmalı
// ve yer tutucu sayısı args sayısına eşit olmalı (off-by-one bind hatası
// Oracle'da ORA-01008 ile patlar, sessiz değil ama testte görmek ucuz).
func assertEveryValueIsBound(t *testing.T, sqlText string, args []any) {
	t.Helper()
	seen := map[string]bool{}
	for _, m := range bindRe.FindAllStringSubmatch(sqlText, -1) {
		seen[m[1]] = true
	}
	if len(seen) != len(args) {
		t.Fatalf("bind yer tutucusu %d, arg %d: %q", len(seen), len(args), sqlText)
	}
	for i := 1; i <= len(args); i++ {
		if !seen[strconv.Itoa(i)] {
			t.Fatalf(":%d yer tutucusu yok: %q", i, sqlText)
		}
	}
}

func TestBuildSampleQuery_Contract(t *testing.T) {
	cfg := cfgFor(t)
	cfg.ExtraWhere = "ERR_CODE NOT IN ('REDACTED')"
	from := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	to := from.Add(15 * time.Minute)

	sqlText, args, err := buildSampleQuery(cfg, from, to, testSampleLimit)
	if err != nil {
		t.Fatalf("builder: %v", err)
	}
	assertSingleSelect(t, sqlText)

	// Zaman aralığı ZORUNLU ve bind'li.
	if !strings.Contains(sqlText, "WHERE ERR_TIMESTAMP >= :1 AND ERR_TIMESTAMP < :2") {
		t.Fatalf("zaman yüklemi bind'li değil: %q", sqlText)
	}
	// v0.10.601 — bind değerleri kaynağın DUVAR SAATİ (bindTime): go-ora
	// time.Time'ı bileşenleriyle gönderir, dilimsiz kolona UTC anı bağlamak
	// 3 saat kaydırırdı. Varsayılan dilim Europe/Istanbul.
	loc, _ := time.LoadLocation(DefaultTimezone)
	if len(args) != 3 || args[0] != any(bindTime(from, loc, false)) || args[1] != any(bindTime(to, loc, false)) || args[2] != any("T") {
		t.Fatalf("arg sırası: %#v", args)
	}
	// Tip süzgeci bind'li.
	if !strings.Contains(sqlText, "AND ERR_TYPE IN (:3)") {
		t.Fatalf("tip yüklemi bind'li değil: %q", sqlText)
	}
	// FETCH FIRST — sınırsız tarama yok.
	if !strings.Contains(sqlText, "FETCH FIRST 5 ROWS ONLY") {
		t.Fatalf("FETCH FIRST yok: %q", sqlText)
	}
	// Identifier'lar metinde (bind edilemezler).
	if !strings.Contains(sqlText, "FROM REDACTED") {
		t.Fatalf("şema.tablo yok: %q", sqlText)
	}
	// ExtraWhere AND(...) olarak.
	if !strings.Contains(sqlText, "AND (ERR_CODE NOT IN ('REDACTED'))") {
		t.Fatalf("extraWhere yok: %q", sqlText)
	}
	// Zaman değeri METNE girmemeli.
	if strings.Contains(sqlText, "2026") {
		t.Fatalf("zaman değeri metne interpole edildi: %q", sqlText)
	}
}

// Tip değerleri operatör kontrolünde; tırnak taşıyan bir değer metne
// KAYARSA sorgu bozulur. Bu vaka tam olarak onu yakalar.
func TestBuildSampleQuery_ValuesNeverInterpolated(t *testing.T) {
	cfg := cfgFor(t)
	cfg.TypeFilter = []string{"T", "O'REILLY", "X') OR 1=1 --"}
	sqlText, args, err := buildSampleQuery(cfg, time.Now().Add(-time.Hour), time.Now(), 5)
	if err != nil {
		t.Fatalf("builder: %v", err)
	}
	assertSingleSelect(t, sqlText)
	assertEveryValueIsBound(t, sqlText, args)
	for _, v := range cfg.TypeFilter {
		if v != "T" && strings.Contains(sqlText, v) {
			t.Fatalf("tip değeri %q metne girdi: %q", v, sqlText)
		}
	}
	if !strings.Contains(sqlText, "IN (:3, :4, :5)") {
		t.Fatalf("üç tip için üç bind bekleniyordu: %q", sqlText)
	}
	if len(args) != 5 || args[4] != any("X') OR 1=1 --") {
		t.Fatalf("değerler arg listesinde taşınmalı: %#v", args)
	}
}

// Tip süzgeci koda gömülü DEĞİL: ayardan gelen değer sorguya geçer.
func TestBuildSampleQuery_TypeFilterComesFromSettings(t *testing.T) {
	cfg := cfgFor(t)
	cfg.TypeFilter = []string{"E"}
	_, args, err := buildSampleQuery(cfg, time.Now().Add(-time.Hour), time.Now(), 5)
	if err != nil {
		t.Fatalf("builder: %v", err)
	}
	if args[2] != any("E") {
		t.Fatalf("ayardaki tip sorguya geçmedi: %#v", args)
	}
	// Boş bırakılırsa varsayılan (ve YALNIZ o zaman).
	cfg.TypeFilter = nil
	_, args, err = buildSampleQuery(cfg, time.Now().Add(-time.Hour), time.Now(), 5)
	if err != nil {
		t.Fatalf("builder: %v", err)
	}
	if len(args) != 3 || args[2] != any("T") {
		t.Fatalf("varsayılan tip uygulanmadı: %#v", args)
	}
}

// Builder Normalize'ın koştuğuna GÜVENMEZ: doğrulanmamış bir cfg ile
// çağrılırsa (Aşama 2'de poller doğrudan çağıracak) SQL üretmez.
func TestBuildSampleQuery_RejectsUnvalidatedConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  SourceConfig
	}{
		{"şema boş", SourceConfig{Table: "T"}},
		{"tablo boş", SourceConfig{Schema: "S"}},
		{"şema enjeksiyonu", SourceConfig{Schema: "S UNION SELECT 1 FROM DUAL--", Table: "T"}},
		{"tablo noktalı virgül", SourceConfig{Schema: "S", Table: "T;X"}},
		{"zaman kolonu bozuk", SourceConfig{Schema: "S", Table: "T", TimestampColumn: "TS COL"}},
		{"tip kolonu bozuk", SourceConfig{Schema: "S", Table: "T", TypeColumn: "A'B"}},
		{"extraWhere yorumlu", SourceConfig{Schema: "S", Table: "T", ExtraWhere: "1=1 --"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if sqlText, _, err := buildSampleQuery(c.cfg, time.Now().Add(-time.Hour), time.Now(), 5); err == nil {
				t.Fatalf("doğrulanmamış cfg SQL üretti: %q", sqlText)
			}
		})
	}
}

func TestBuildSampleQuery_LimitClamped(t *testing.T) {
	cfg := cfgFor(t)
	for _, c := range []struct {
		in   int
		want string
	}{
		{0, "FETCH FIRST 1 ROWS ONLY"},
		{-5, "FETCH FIRST 1 ROWS ONLY"},
		{5, "FETCH FIRST 5 ROWS ONLY"},
		{maxSampleLimit, "FETCH FIRST 50 ROWS ONLY"},
		{10000, "FETCH FIRST 50 ROWS ONLY"},
	} {
		sqlText, _, err := buildSampleQuery(cfg, time.Now().Add(-time.Hour), time.Now(), c.in)
		if err != nil {
			t.Fatalf("builder: %v", err)
		}
		if !strings.HasSuffix(sqlText, c.want) {
			t.Errorf("limit %d → %q bekleniyordu: %q", c.in, c.want, sqlText)
		}
	}
}

// ── şifre/DSN redaksiyonu ───────────────────────────────────────

func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		name    string
		msg     string
		secrets []string
		wantNot []string
		wantHas string
	}{
		{
			name:    "düz şifre silinir",
			msg:     "ORA-01017: invalid username/password for user coremetry pw=s3cret!",
			secrets: []string{"s3cret!"},
			wantNot: []string{"s3cret!"},
			wantHas: "***",
		},
		{
			name:    "dsn credential maskelenir",
			msg:     "dial error for oracle://coremetry:s3cret@db.local:1521/ORCL",
			secrets: []string{"s3cret"},
			wantNot: []string{"s3cret"},
			wantHas: "oracle://coremetry:***@",
		},
		{
			name:    "şifre bilinmese bile dsn maskelenir",
			msg:     "connect oracle://appuser:hunter2@db.local:1521/ORCL failed",
			secrets: nil,
			wantNot: []string{"hunter2"},
			wantHas: "oracle://appuser:***@",
		},
		{
			name:    "çok kısa secret mesajı okunmaz etmez",
			msg:     "ORA-12541: TNS:no listener",
			secrets: []string{"a"},
			wantNot: nil,
			wantHas: "TNS:no listener",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redactSecrets(c.msg, c.secrets...)
			for _, bad := range c.wantNot {
				if strings.Contains(got, bad) {
					t.Fatalf("secret sızdı %q: %q", bad, got)
				}
			}
			if !strings.Contains(got, c.wantHas) {
				t.Fatalf("beklenen parça yok %q: %q", c.wantHas, got)
			}
		})
	}
}

// Test() şifre referansı çözülemediğinde AĞA HİÇ ÇIKMAZ ve ok:false döner.
func TestTest_UnresolvedPasswordRefNeverDials(t *testing.T) {
	svc := New()
	svc.getenv = func(string) string { return "" }
	svc.openDB = func(string) (sqlDB, error) {
		t.Fatal("şifre çözülemeden bağlantı açılmamalı")
		return nil, nil
	}
	src := cfgFor(t)
	src.Password, src.PasswordRef = "", "env:ORACLE_PW_YOK"
	res := svc.Test(t.Context(), src)
	if res.OK || res.PasswordResolved {
		t.Fatalf("başarısız olmalıydı: %+v", res)
	}
	if res.Error == "" || res.Columns == nil {
		t.Fatalf("hata metni + boş kolon dilimi bekleniyordu: %+v", res)
	}
	// Durum satırına da düşmeli (şifresiz).
	svc.Configure(Settings{Sources: []SourceConfig{src}})
	st := svc.Status()
	if len(st) != 1 || st[0].LastCheckAt == 0 || st[0].LastCheckOK {
		t.Fatalf("durum izi: %+v", st)
	}
}

func TestQueryTimeoutClamped(t *testing.T) {
	for _, c := range []struct {
		in   int
		want time.Duration
	}{
		{0, DefaultQueryTimeoutSec * time.Second},
		{-1, DefaultQueryTimeoutSec * time.Second},
		{MinQueryTimeoutSec, MinQueryTimeoutSec * time.Second},
		{MaxQueryTimeoutSec, MaxQueryTimeoutSec * time.Second},
		{MaxQueryTimeoutSec + 1, DefaultQueryTimeoutSec * time.Second},
	} {
		if got := queryTimeout(SourceConfig{QueryTimeoutSec: c.in}); got != c.want {
			t.Errorf("queryTimeout(%d) = %v, want %v", c.in, got, c.want)
		}
	}
}

// v0.10.580 — geniş pencere ikinci denemesinin KARARI.
//
// "Hata yok + satır yok" tek başına ikircikli bir cevaptır ve operatör
// üç farklı sorunu ayırt edemezse yanlış yerde arar. Influx bunu prod'da
// öğrendi (v0.10.335); aynı ayrımı burada doğuşta kuruyoruz.
func TestEmptyProbeHint(t *testing.T) {
	t.Run("geniş pencerede veri var → TZ/seyreklik, örnekler KULLANILIR", func(t *testing.T) {
		hint, useWide := emptyProbeHint(3, nil)
		if !useWide {
			t.Fatal("geniş pencerenin örnekleri kullanılmalıydı")
		}
		if !strings.Contains(hint, "TZ") || !strings.Contains(hint, "24 saat") {
			t.Errorf("ipucu ayrımı söylemiyor: %q", hint)
		}
	})
	t.Run("geniş pencerede de yok → süzgeç/ad, örnek YOK", func(t *testing.T) {
		hint, useWide := emptyProbeHint(0, nil)
		if useWide {
			t.Fatal("boş geniş pencere örnek olarak kullanılamaz")
		}
		if !strings.Contains(hint, "tip süzgeci") {
			t.Errorf("ipucu süzgeç/ad ihtimalini söylemeli: %q", hint)
		}
	})
	t.Run("geniş deneme hata verdi → teşhis YOK, uydurma", func(t *testing.T) {
		hint, useWide := emptyProbeHint(0, errors.New("ORA-00942"))
		if useWide {
			t.Fatal("hatalı denemenin örneği kullanılamaz")
		}
		if !strings.Contains(hint, "ORA-00942") {
			t.Errorf("gerçek hata metni ipucuda görünmeli: %q", hint)
		}
	})
}
