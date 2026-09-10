package oracle

// settings_test.go — v0.10.580, Oracle AŞAMA 1.
//
// Normalize bu paketin TEK kapısı: hem PUT hem bağlantı testi oradan
// geçiyor, dolayısıyla şifre sözleşmesi (boş = saklıyı koru) ve SQL'e
// identifier olarak GİREN alanların doğrulaması burada çivileniyor.
// Şema/tablo/kolon adları bind EDİLEMEZ; bu testlerin gevşemesi doğrudan
// bir SQL enjeksiyon yüzeyi açar.

import (
	"strings"
	"testing"
)

func base() SourceConfig {
	return SourceConfig{
		Name: "core-oracle", Host: "db.example.local", Port: 1521,
		ServiceName: "ORCLPDB", User: "coremetry", Password: "s3cret",
		Schema: "APPOWNER", Table: "MCA_TERROR_LOG", Enabled: true,
	}
}

func one(src SourceConfig) Settings { return Settings{Sources: []SourceConfig{src}} }

func mustNormalize(t *testing.T, in, prev Settings) Settings {
	t.Helper()
	out, err := Normalize(in, prev, func() string { return "o-deadbeef" })
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	return out
}

// ── şifre sözleşmesi ────────────────────────────────────────────

func TestNormalize_EmptyPasswordPreservesStored(t *testing.T) {
	prev := mustNormalize(t, one(base()), Settings{})
	if prev.Sources[0].Password != "s3cret" {
		t.Fatalf("ön koşul: %q", prev.Sources[0].Password)
	}
	// Form şifreyi geri ALAMAZ (GET maskeler) — her kayıt boş yollar.
	edited := base()
	edited.Password = ""
	edited.ID = prev.Sources[0].ID
	edited.ServiceName = "ORCLPDB2"

	out := mustNormalize(t, one(edited), prev)
	if got := out.Sources[0].Password; got != "s3cret" {
		t.Errorf("boş şifre saklıyı korumalı, got %q", got)
	}
	if out.Sources[0].ServiceName != "ORCLPDB2" {
		t.Errorf("öteki alan güncellenmeli: %q", out.Sources[0].ServiceName)
	}
	// Yeni değer YAZILIR.
	edited.Password = "yeni"
	out = mustNormalize(t, one(edited), prev)
	if out.Sources[0].Password != "yeni" {
		t.Errorf("yeni şifre yazılmalı: %q", out.Sources[0].Password)
	}
}

// Kayıtsız (prev boş) bir kaynakta boş şifre saklıyı kopyalayamaz — ve
// etkin kaynakta bu bir HATA, sessiz "şifresiz bağlan" değil.
func TestNormalize_EnabledWithoutPasswordRejected(t *testing.T) {
	src := base()
	src.Password = ""
	if _, err := Normalize(one(src), Settings{}, NewSourceID); err == nil {
		t.Fatal("şifresiz etkin kaynak kabul edilmemeli")
	}
}

func TestSnapshot_NeverLeaksPassword(t *testing.T) {
	svc := New()
	svc.Configure(mustNormalize(t, one(base()), Settings{}))
	snap := svc.Snapshot()
	if len(snap.Sources) != 1 {
		t.Fatalf("kaynak sayısı %d", len(snap.Sources))
	}
	s := snap.Sources[0]
	if s.Password != "" {
		t.Errorf("GET şifreyi geri verdi: %q", s.Password)
	}
	if !s.HasPassword {
		t.Error("hasPassword rozeti false")
	}
	if !s.PasswordResolved || s.PasswordError != "" {
		t.Errorf("düz şifre çözülmüş sayılmalı: %v %q", s.PasswordResolved, s.PasswordError)
	}
	// Maskeleme SAKLI ayarı bozmamalı (snapshot kopya üstünde çalışır).
	if svc.CurrentSettings().Sources[0].Password != "s3cret" {
		t.Error("Snapshot saklı şifreyi sildi")
	}
	// Durum satırı da şifresiz.
	for _, st := range svc.Status() {
		if strings.Contains(st.LastError, "s3cret") || strings.Contains(st.PasswordError, "s3cret") {
			t.Errorf("durum şifre sızdırdı: %+v", st)
		}
	}
}

func TestSnapshot_PasswordRefErrorIsABadge(t *testing.T) {
	svc := New()
	svc.getenv = func(string) string { return "" }
	src := base()
	src.Password, src.PasswordRef = "", "env:ORACLE_PW_YOK"
	svc.Configure(mustNormalize(t, one(src), Settings{}))
	s := svc.Snapshot().Sources[0]
	if s.PasswordResolved {
		t.Error("çözülemeyen referans resolved görünmemeli")
	}
	if s.PasswordError == "" {
		t.Error("rozet metni boş — operatör sessiz ORA-01017 görür")
	}
}

// ── identifier doğrulaması (SQL'e TIRNAKSIZ girer) ──────────────

func TestNormalize_RejectsBadIdentifiers(t *testing.T) {
	cases := []struct {
		name  string
		apply func(*SourceConfig)
	}{
		{"şema boşluklu", func(s *SourceConfig) { s.Schema = "APP OWNER" }},
		{"şema tırnaklı", func(s *SourceConfig) { s.Schema = `APP"OWNER` }},
		{"şema noktalı", func(s *SourceConfig) { s.Schema = "APP.OWNER" }},
		{"şema noktalı virgül", func(s *SourceConfig) { s.Schema = "APP;DROP" }},
		{"şema tireli", func(s *SourceConfig) { s.Schema = "APP-OWNER" }},
		{"şema rakamla başlıyor", func(s *SourceConfig) { s.Schema = "1APP" }},
		{"şema 31 karakter", func(s *SourceConfig) { s.Schema = "A" + strings.Repeat("B", 30) }},
		{"tablo parantezli", func(s *SourceConfig) { s.Table = "T(1)" }},
		{"tablo alt sorgulu", func(s *SourceConfig) { s.Table = "T UNION SELECT" }},
		{"zaman kolonu boşluklu", func(s *SourceConfig) { s.TimestampColumn = "TS COL" }},
		{"tip kolonu tırnaklı", func(s *SourceConfig) { s.TypeColumn = "T'X" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := base()
			c.apply(&src)
			if _, err := Normalize(one(src), Settings{}, NewSourceID); err == nil {
				t.Fatalf("kabul edildi: %+v", src)
			}
		})
	}
	// Geçerli olanlar geçmeli — kapı kapalı kalmasın.
	for _, ok := range []string{"APPOWNER", "App_Owner", "A$B#C", "X1"} {
		src := base()
		src.Schema = ok
		if _, err := Normalize(one(src), Settings{}, NewSourceID); err != nil {
			t.Errorf("geçerli identifier reddedildi %q: %v", ok, err)
		}
	}
}

// Kapalı bir taslak da yarın etkinleşir: doluysa doğrulama HER ZAMAN koşar.
func TestNormalize_ValidatesIdentifiersEvenWhenDisabled(t *testing.T) {
	src := base()
	src.Enabled = false
	src.Table = "T; DROP TABLE X"
	if _, err := Normalize(one(src), Settings{}, NewSourceID); err == nil {
		t.Fatal("kapalı kaynakta da identifier doğrulanmalı")
	}
}

// ── ExtraWhere ──────────────────────────────────────────────────

func TestNormalize_ExtraWhereBannedTokens(t *testing.T) {
	for _, bad := range []string{
		"1=1; DELETE FROM MCA_TERROR_LOG",
		"1=1 -- kalanı sustur",
		"1=1 /* blok yorum",
		"MCA_ERR_CODE='X'--",
	} {
		src := base()
		src.ExtraWhere = bad
		if _, err := Normalize(one(src), Settings{}, NewSourceID); err == nil {
			t.Errorf("extraWhere kabul edildi: %q", bad)
		}
	}
	// Meşru yüklem geçer.
	src := base()
	src.ExtraWhere = "MCA_ERR_CODE NOT IN ('ERR_000') AND MCA_ERR_CHANNELCODE IS NOT NULL"
	out, err := Normalize(one(src), Settings{}, NewSourceID)
	if err != nil {
		t.Fatalf("meşru extraWhere reddedildi: %v", err)
	}
	if out.Sources[0].ExtraWhere != src.ExtraWhere {
		t.Errorf("extraWhere değişti: %q", out.Sources[0].ExtraWhere)
	}
	// Uzunluk tavanı.
	src.ExtraWhere = strings.Repeat("A", MaxExtraWhereLen+1)
	if _, err := Normalize(one(src), Settings{}, NewSourceID); err == nil {
		t.Error("uzunluk tavanı uygulanmadı")
	}
}

// ── tip süzgeci: varsayılan VAR, ama koda gömülü DEĞİL ───────────

func TestNormalize_TypeFilterDefaultAndOverride(t *testing.T) {
	out := mustNormalize(t, one(base()), Settings{})
	if got := out.Sources[0].TypeFilter; len(got) != 1 || got[0] != "T" {
		t.Fatalf("varsayılan tip süzgeci %v, want [T]", got)
	}
	// Ayardan DEĞİŞTİRİLEBİLİR olması sözleşmenin yarısı: 'T' koda gömülü
	// olsaydı bu vaka sessizce ["T"]e düşerdi.
	src := base()
	src.TypeFilter = []string{"E", "W", " E ", ""}
	out = mustNormalize(t, one(src), Settings{})
	got := out.Sources[0].TypeFilter
	if len(got) != 2 || got[0] != "E" || got[1] != "W" {
		t.Fatalf("operatörün tip süzgeci %v, want [E W] (kırpılmış + tekilleştirilmiş)", got)
	}
	// Varsayılan fonksiyonu paylaşılan dilim döndürmemeli.
	d := DefaultTypeFilter()
	d[0] = "X"
	if DefaultTypeFilter()[0] != "T" {
		t.Error("DefaultTypeFilter paylaşılan dilim döndürüyor")
	}
}

// ── kelepçeler ──────────────────────────────────────────────────

func TestNormalize_Clamps(t *testing.T) {
	// 0 → varsayılan.
	out := mustNormalize(t, one(base()), Settings{})
	s := out.Sources[0]
	if s.MaxOpenConns != DefaultMaxOpenConns || s.QueryTimeoutSec != DefaultQueryTimeoutSec ||
		s.IntervalSec != DefaultIntervalSec || s.Port != DefaultPort {
		t.Fatalf("varsayılanlar: %+v", s)
	}
	if s.TimestampColumn != DefaultTimestampColumn || s.TypeColumn != DefaultTypeColumn {
		t.Fatalf("kolon varsayılanları: %q %q", s.TimestampColumn, s.TypeColumn)
	}

	cases := []struct {
		name  string
		apply func(*SourceConfig)
		bad   bool
	}{
		{"maxOpenConns 0 → varsayılan", func(s *SourceConfig) { s.MaxOpenConns = 0 }, false},
		{"maxOpenConns 1", func(s *SourceConfig) { s.MaxOpenConns = MinMaxOpenConns }, false},
		{"maxOpenConns 16", func(s *SourceConfig) { s.MaxOpenConns = MaxMaxOpenConns }, false},
		{"maxOpenConns 17", func(s *SourceConfig) { s.MaxOpenConns = MaxMaxOpenConns + 1 }, true},
		{"maxOpenConns negatif", func(s *SourceConfig) { s.MaxOpenConns = -1 }, true},
		{"queryTimeout 4", func(s *SourceConfig) { s.QueryTimeoutSec = MinQueryTimeoutSec - 1 }, true},
		{"queryTimeout 5", func(s *SourceConfig) { s.QueryTimeoutSec = MinQueryTimeoutSec }, false},
		{"queryTimeout 120", func(s *SourceConfig) { s.QueryTimeoutSec = MaxQueryTimeoutSec }, false},
		{"queryTimeout 121", func(s *SourceConfig) { s.QueryTimeoutSec = MaxQueryTimeoutSec + 1 }, true},
		{"interval 9", func(s *SourceConfig) { s.IntervalSec = MinIntervalSec - 1 }, true},
		{"interval 10", func(s *SourceConfig) { s.IntervalSec = MinIntervalSec }, false},
		{"interval 3600", func(s *SourceConfig) { s.IntervalSec = MaxIntervalSec }, false},
		{"interval 3601", func(s *SourceConfig) { s.IntervalSec = MaxIntervalSec + 1 }, true},
		{"port 0 → 1521", func(s *SourceConfig) { s.Port = 0 }, false},
		{"port 70000", func(s *SourceConfig) { s.Port = 70000 }, true},
		{"port negatif", func(s *SourceConfig) { s.Port = -1 }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := base()
			c.apply(&src)
			_, err := Normalize(one(src), Settings{}, NewSourceID)
			if c.bad && err == nil {
				t.Fatalf("aralık dışı kabul edildi: %+v", src)
			}
			if !c.bad && err != nil {
				t.Fatalf("geçerli değer reddedildi: %v", err)
			}
		})
	}
}

// ── ad tekilliği + id taşınması ─────────────────────────────────

func TestNormalize_NameUniqueAndCaseInsensitive(t *testing.T) {
	a, b := base(), base()
	b.Name = "Core-Oracle" // aynı ad, farklı yazım
	if _, err := Normalize(Settings{Sources: []SourceConfig{a, b}}, Settings{}, NewSourceID); err == nil {
		t.Fatal("büyük/küçük harf farkı tekilliği delmemeli")
	}
	b.Name = "ikinci"
	if _, err := Normalize(Settings{Sources: []SourceConfig{a, b}}, Settings{}, NewSourceID); err != nil {
		t.Fatalf("farklı adlar reddedildi: %v", err)
	}
	// Tavan.
	var many []SourceConfig
	for i := 0; i < maxSources+1; i++ {
		s := base()
		s.Name = "k" + string(rune('a'+i))
		many = append(many, s)
	}
	if _, err := Normalize(Settings{Sources: many}, Settings{}, NewSourceID); err == nil {
		t.Fatal("kaynak tavanı uygulanmadı")
	}
}

func TestNormalize_IDCarriedForward(t *testing.T) {
	prev := mustNormalize(t, one(base()), Settings{})
	id := prev.Sources[0].ID
	if id == "" || !strings.HasPrefix(id, "o-") {
		t.Fatalf("id biçimi: %q", id)
	}
	// (a) id gelirse korunur.
	edited := base()
	edited.ID = id
	if got := mustNormalize(t, one(edited), prev).Sources[0].ID; got != id {
		t.Errorf("tanınan id korunmadı: %q ≠ %q", got, id)
	}
	// (b) id gelmezse ADA göre saklı kayıttan taşınır — form id'yi
	// unutunca kaynak yeni bir kimlikle ikizlenmesin.
	edited.ID = ""
	if got := mustNormalize(t, one(edited), prev).Sources[0].ID; got != id {
		t.Errorf("ada göre id taşınmadı: %q ≠ %q", got, id)
	}
	// (c) yeni ad → YENİ id (sabit üretici değil, ayrı bir değer üretsin ki
	// "aynı kaldı" iddiası üreticinin sabitliğinden gelmesin).
	edited.Name, edited.ID = "baska", ""
	fresh, err := Normalize(one(edited), prev, func() string { return "o-11112222" })
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := fresh.Sources[0].ID; got == id || got != "o-11112222" {
		t.Errorf("yeni kaynağa yeni id verilmeli: %q (eski %q)", got, id)
	}
}

// ── derin kopya ─────────────────────────────────────────────────

func TestNormalize_DeepCopy(t *testing.T) {
	src := base()
	src.TypeFilter = []string{"T", "E"}
	in := one(src)
	out := mustNormalize(t, in, Settings{})

	// Girdi değişmemeli.
	if in.Sources[0].ID != "" || in.Sources[0].TimestampColumn != "" {
		t.Errorf("Normalize girdiyi değiştirdi: %+v", in.Sources[0])
	}
	// Çıktı girdinin dilimini PAYLAŞMAMALI.
	in.Sources[0].TypeFilter[0] = "ZEHIR"
	if out.Sources[0].TypeFilter[0] != "T" {
		t.Error("TypeFilter dilimi paylaşılıyor — girdi mutasyonu çıktıya sızdı")
	}
}

// ── bağlantı biçimi: ya DSN ya host/port/servis ──────────────────

func TestNormalize_ConnectionShape(t *testing.T) {
	// DSN tek başına yeter (credential DSN'in içinde).
	src := SourceConfig{Name: "dsnlu", DSN: "oracle://u:p@db.local:1521/ORCL",
		Schema: "APPOWNER", Table: "MCA_TERROR_LOG", Enabled: true}
	if _, err := Normalize(one(src), Settings{}, NewSourceID); err != nil {
		t.Fatalf("dsn'li kaynak reddedildi: %v", err)
	}
	// İkisi birden → belirsizlik, reddet.
	src.Host = "baska.local"
	if _, err := Normalize(one(src), Settings{}, NewSourceID); err == nil {
		t.Error("dsn + host birlikte kabul edildi")
	}
	// Şema/biçim hatası.
	src = SourceConfig{Name: "dsnlu", DSN: "jdbc:oracle:thin:@db:1521/ORCL",
		Schema: "APPOWNER", Table: "MCA_TERROR_LOG", Enabled: true}
	if _, err := Normalize(one(src), Settings{}, NewSourceID); err == nil {
		t.Error("jdbc dsn kabul edildi")
	}
	// Etkin ama host/servis eksik.
	src = SourceConfig{Name: "eksik", Schema: "A", Table: "B", Enabled: true}
	if _, err := Normalize(one(src), Settings{}, NewSourceID); err == nil {
		t.Error("hostsuz etkin kaynak kabul edildi")
	}
	// passwordRef biçimi.
	bad := base()
	bad.Password, bad.PasswordRef = "", "duz-token"
	if _, err := Normalize(one(bad), Settings{}, NewSourceID); err == nil {
		t.Error("geçersiz passwordRef kabul edildi")
	}
	good := base()
	good.Password, good.PasswordRef = "", "env:ORACLE_PW"
	if _, err := Normalize(one(good), Settings{}, NewSourceID); err != nil {
		t.Errorf("geçerli passwordRef reddedildi: %v", err)
	}
}
