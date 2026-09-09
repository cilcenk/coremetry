// Package oracle — Oracle hata tablosunu (MCA_TERROR_LOG ailesi) DIŞ
// KAYNAK olarak bağlar. v0.10.580, AŞAMA 1: yalnız datasource tanımı,
// credential ve bağlantı testi. Poller / metrik yazımı / Problem üretimi
// Aşama 2-3'te, ayrı sürümlerde (audit:
// docs/audit/oracle-error-log-2026-09-09.md §7).
//
// Yapı internal/influx'un birebir simetriği: system_settings["oracle_sources"]
// altında tipli Settings blobu, dar settingsStore arayüzü, boot'ta
// LoadPersisted + SavePersisted/Configure canlı takas, Settings UI için
// Snapshot. Şifre sözleşmesi de aynı (v0.10.224 operatör kararı): düz şifre
// blob'da saklanabilir ama GET ASLA geri vermez (HasPassword rozeti), boş
// girdi saklıyı KORUR; alternatif REFERANS `env:NAME` | `file:/path`
// (internal/secretref) — varsa o kazanır ve kullanım anında çözülür.
//
// Influx'tan TEK sapma: StartConfigRefresh ikizi YOK. Çok-pod yakınsaması
// main.go'daki chstore.SettingsRefresher (cfgRefresh.Add("oracle", …))
// üzerinden koşuyor; influx'taki metot main'den hiç çağrılmıyor, ölü ikizi
// taşımanın anlamı yok.
//
// SQL disiplini (audit §3): şema/tablo/kolon adları BIND EDİLEMEZ, sorguya
// identifier olarak girer — bu yüzden buradaki doğrulama bir güvenlik
// kapısıdır, kozmetik değil. Değerler (zaman, tip) daima bind (client.go).
package oracle

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/secretref"
)

const (
	// DefaultPort — Oracle TNS listener.
	DefaultPort = 1521

	// Havuz: 4 eşzamanlı bağlantı bir poll döngüsü + operatörün testi için
	// yeter; 16 üstü banka tarafındaki session kotasını yer.
	DefaultMaxOpenConns = 4
	MinMaxOpenConns     = 1
	MaxMaxOpenConns     = 16

	// Sorgu timeout'u: 5 s altı indeksli bir aralık sorgusu için bile dar,
	// 120 s üstü bir teşhis ucunu askıda bırakır.
	DefaultQueryTimeoutSec = 20
	MinQueryTimeoutSec     = 5
	MaxQueryTimeoutSec     = 120

	// Poll aralığı (Aşama 2 tüketir, Aşama 1 saklar): dakika kovası üreten
	// bir hat için 10 s altı anlamsız, 1 saat üstü baseline penceresini boşaltır.
	DefaultIntervalSec = 60
	MinIntervalSec     = 10
	MaxIntervalSec     = 3600

	// DefaultTimestampColumn / DefaultTypeColumn — VARSAYILAN, sabit değil:
	// ikisi de ayardan değiştirilebilir (audit: "şema/tablo/alan adı koda
	// gömülmeyecek"). Varsayılanlar operatörün bugünkü tablosunu tarif eder.
	DefaultTimestampColumn = "MCA_ERR_TIMESTAMP"
	DefaultTypeColumn      = "MCA_ERR_TYPE"

	maxSources       = 10
	maxTypeFilter    = 16
	maxTypeValueLen  = 32
	maxNameLen       = 64
	MaxExtraWhereLen = 500
)

// DefaultTypeFilter — varsayılan tip süzgeci. Fonksiyon, paket değişkeni
// DEĞİL: dışarıdaki bir çağıran döndürülen dilimi değiştirse bile varsayılan
// bozulmaz (paylaşılan-dilim tuzağı).
func DefaultTypeFilter() []string { return []string{"T"} }

// SourceConfig — bir Oracle hata tablosu kaynağı.
//
// Bağlantı İKİ biçimden biri: ya ham DSN (`oracle://user:pass@host:port/svc`,
// credential DSN'in içinde) ya host/port/serviceName üçlüsü + User +
// Password/PasswordRef. İkisi birden verilemez — hangisinin kazandığı
// belirsiz kalırdı.
type SourceConfig struct {
	// ID — sunucu sahipli, "o-" + 8 hex (influx "i-", thanos "c-" ailesi).
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`

	DSN         string `json:"dsn,omitempty"`
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	ServiceName string `json:"serviceName,omitempty"`

	User string `json:"user"`
	// Password — saklı düz şifre (GET'te ASLA; Snapshot HasPassword der).
	// Boş PUT saklıyı KORUR (Normalize prev'den taşır).
	Password string `json:"password,omitempty"`
	// PasswordRef — `env:NAME` | `file:/yol`; doluysa Password'a tercih edilir.
	PasswordRef string `json:"passwordRef,omitempty"`

	Schema string `json:"schema"`
	Table  string `json:"table"`
	// TimestampColumn / TypeColumn — boşsa DefaultTimestampColumn /
	// DefaultTypeColumn. Şema/tablo gibi bunlar da identifier olarak sorguya
	// girer, dolayısıyla aynı katı regex'ten geçer.
	TimestampColumn string `json:"timestampColumn,omitempty"`
	TypeColumn      string `json:"typeColumn,omitempty"`

	// ExtraWhere — operatörün ek yüklemi, sorguya AND (…) olarak girer.
	// Bind edilemez (serbest ifade), bu yüzden noktalı virgül ve yorum
	// başlatıcıları reddedilir: tek bir `--` kalan yüklemleri susturur.
	ExtraWhere string `json:"extraWhere,omitempty"`
	// TypeFilter — MCA_ERR_TYPE değerleri; boşsa DefaultTypeFilter().
	// Değerler BIND edilir, koda gömülü değildir.
	TypeFilter []string `json:"typeFilter,omitempty"`

	MaxOpenConns    int `json:"maxOpenConns,omitempty"`
	QueryTimeoutSec int `json:"queryTimeoutSec,omitempty"`
	IntervalSec     int `json:"intervalSec,omitempty"`

	Enabled bool `json:"enabled"`
}

// Settings — kalıcı blob: tüm liste atomik yazılır (influx/thanos sözleşmesi;
// N ≤ 10 ölçeğinde satır-düzeyi yarış yok).
type Settings struct {
	Sources []SourceConfig `json:"sources"`
}

// SourceSnapshot — GET görünümü. Password MASKELİ (gömülü SourceConfig'in
// Password'ü boşaltılır, HasPassword söyler); passwordRef bir REFERANS
// (secret değil), aynen görünür. PasswordResolved/PasswordError Settings
// rozeti: operatör "env yok / dosya yok"u sessiz ORA-01017 yerine
// kaydetmeden görür.
type SourceSnapshot struct {
	SourceConfig
	HasPassword      bool   `json:"hasPassword"`
	PasswordResolved bool   `json:"passwordResolved"`
	PasswordError    string `json:"passwordError,omitempty"`
}

// Snapshot — GET /api/settings/oracle cevabı.
type Snapshot struct {
	Sources []SourceSnapshot `json:"sources"`
}

// CheckResult — bu pod'da koşan son bağlantı testinin izi (bellek-içi).
type CheckResult struct {
	At    int64  `json:"at"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Service — ayar sahibi. Bağlantı kurulamazsa Coremetry'nin geri kalanı
// ETKİLENMEZ: her metot nil-safe, uçlar servis yoksa 503 döner.
type Service struct {
	mu     sync.RWMutex
	cfg    Settings
	checks map[string]CheckResult

	// getenv/readFile — passwordRef çözümü; testler enjekte eder.
	getenv   func(string) string
	readFile func(string) ([]byte, error)

	// openDB — sql.Open seam'i (client.go). Testler buraya sahte verir;
	// üretimde go-ora "oracle" sürücüsü.
	openDB func(dsn string) (sqlDB, error)
}

// New — boş liste; LoadPersisted blobu getirir.
func New() *Service {
	s := &Service{
		checks:   map[string]CheckResult{},
		getenv:   os.Getenv,
		readFile: os.ReadFile,
	}
	s.openDB = openOracleDB
	return s
}

// settingsStore — chstore.Store'un bu paket için gereken iki metodu
// (chstore/oracle.go). Dar arayüz: testler sahte store verebilir.
type settingsStore interface {
	GetOracleSettingsRaw(ctx context.Context) ([]byte, error)
	PutOracleSettingsRaw(ctx context.Context, raw []byte) error
}

// SettingsStore — dışarıya açık ad (api katmanı tipli-nil'i buradan eler).
type SettingsStore = settingsStore

// LoadPersisted — system_settings'ten hidrate eder. Blob yoksa boş liste.
func (s *Service) LoadPersisted(ctx context.Context, store settingsStore) error {
	if s == nil || store == nil {
		return nil
	}
	raw, err := store.GetOracleSettingsRaw(ctx)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var cfg Settings
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("oracle decode: %w", err)
	}
	s.Configure(cfg)
	return nil
}

// SavePersisted — blobu yazar, sonra canlı takas. Çağıran Normalize'dan
// geçmiş bir cfg vermeli (handler bunu yapar).
func (s *Service) SavePersisted(ctx context.Context, store settingsStore, cfg Settings) error {
	if s == nil {
		return nil
	}
	if store == nil {
		// Store yoksa (test/boot sırası) yalnız bellekte takas: sessizce
		// "kaydettim" demeyiz, ama canlı ayar da eskide kalmaz.
		s.Configure(cfg)
		return nil
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := store.PutOracleSettingsRaw(ctx, raw); err != nil {
		return err
	}
	s.Configure(cfg)
	return nil
}

// Configure — canlı takas (boot hidrasyonu ve refresh de buradan geçer).
func (s *Service) Configure(cfg Settings) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
}

// CurrentSettings — saklı ayar (paylaşılan dilimler; çağıran değiştirmez,
// Normalize derin kopya üretir).
func (s *Service) CurrentSettings() Settings {
	if s == nil {
		return Settings{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// HasEnabledSources — Aşama 2 poller'ın "iş var mı" sorusu.
func (s *Service) HasEnabledSources() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, src := range s.cfg.Sources {
		if src.Enabled {
			return true
		}
	}
	return false
}

// Snapshot — GET görünümü; şifre maskeli, referans çözülebilirliği rozet.
func (s *Service) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{Sources: []SourceSnapshot{}}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := Snapshot{Sources: make([]SourceSnapshot, 0, len(s.cfg.Sources))}
	for _, src := range s.cfg.Sources {
		ss := SourceSnapshot{SourceConfig: src, HasPassword: src.Password != ""}
		ss.Password = "" // maskele — GET asla geri vermez
		ss.TypeFilter = append([]string(nil), src.TypeFilter...)
		if _, err := s.passwordFor(src); err != nil {
			ss.PasswordError = err.Error()
		} else {
			ss.PasswordResolved = true
		}
		out.Sources = append(out.Sources, ss)
	}
	return out
}

// SourceStatus — GET /api/oracle/status satırı. ŞİFRESİZ: ne Password ne
// DSN buraya girer; LastError redactErr'den geçmiştir.
type SourceStatus struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Enabled          bool   `json:"enabled"`
	PasswordResolved bool   `json:"passwordResolved"`
	PasswordError    string `json:"passwordError,omitempty"`
	LastCheckAt      int64  `json:"lastCheckAt,omitempty"`
	LastCheckOK      bool   `json:"lastCheckOK"`
	LastError        string `json:"lastError,omitempty"`
}

// Status — kaynak başına durum. Aşama 1'de poller YOK: "son hata" bu
// pod'da koşmuş bağlantı testinin izidir (bellek-içi, pod-yerel) artı
// şifre referansının çözülüp çözülmediği. Uydurma bir "sağlıklı" demez.
func (s *Service) Status() []SourceStatus {
	if s == nil {
		return []SourceStatus{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SourceStatus, 0, len(s.cfg.Sources))
	for _, src := range s.cfg.Sources {
		st := SourceStatus{ID: src.ID, Name: src.Name, Enabled: src.Enabled}
		if _, err := s.passwordFor(src); err != nil {
			st.PasswordError = err.Error()
		} else {
			st.PasswordResolved = true
		}
		if c, ok := s.checks[statusKey(src)]; ok {
			st.LastCheckAt, st.LastCheckOK, st.LastError = c.At, c.OK, c.Error
		}
		out = append(out, st)
	}
	return out
}

func statusKey(src SourceConfig) string {
	if src.ID != "" {
		return src.ID
	}
	return "name:" + strings.ToLower(src.Name)
}

// recordCheck — bağlantı testinin izini bırakır (Status okur).
func (s *Service) recordCheck(src SourceConfig, res TestResult) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checks == nil {
		s.checks = map[string]CheckResult{}
	}
	s.checks[statusKey(src)] = CheckResult{At: time.Now().UnixMilli(), OK: res.OK, Error: res.Error}
}

// passwordFor — kaynağın ETKİN şifresi: referans varsa çözülür (rotasyon
// anında etkili), yoksa saklı düz şifre. İkisi de yoksa ve credential DSN'in
// içindeyse boş dönmek meşrudur; hiçbiri yoksa hata.
func (s *Service) passwordFor(src SourceConfig) (string, error) {
	if src.PasswordRef != "" {
		v, err := secretref.ResolveWith(src.PasswordRef, s.getenv, s.readFile)
		if err != nil {
			return "", fmt.Errorf("kaynak %q: %w", src.Name, err)
		}
		return v, nil
	}
	if src.Password != "" {
		return src.Password, nil
	}
	if src.DSN != "" {
		return "", nil // credential DSN'in içinde
	}
	return "", fmt.Errorf("kaynak %q: şifre yok (şifre girin ya da passwordRef verin)", src.Name)
}

// NewSourceID — "o-" + 8 hex (influx "i-" ailesi). Sunucu sahipli.
func NewSourceID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("o-%08x", time.Now().UnixNano()&0xffffffff)
	}
	return "o-" + hex.EncodeToString(b[:])
}

var (
	sourceNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.\-]{0,63}$`)
	// identRe — Oracle identifier'ı: harfle başlar, 30 karakteri geçmez
	// (11g sınırı; 12.2+ 128'e çıktı ama dar kalmak bedava). SQL'e TIRNAKSIZ
	// girer, dolayısıyla bu regex'in geçirdiği her şey enjeksiyon-güvenli
	// olmak ZORUNDA: boşluk, tırnak, noktalı virgül, tire hiçbiri geçmiyor.
	identRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_$#]{0,29}$`)
	// dsnRe — go-ora'nın kabul ettiği URL biçimi (BuildUrl da bunu üretir).
	dsnRe = regexp.MustCompile(`(?i)^oracle://`)
)

// extraWhereBanned — ExtraWhere'de YASAK diziler. Gerekçe sırayla:
// `;` ikinci bir ifade açar; `--` ve `/*` sorgunun kalanını (zaman
// yüklemi, FETCH FIRST) yorum yapıp sınırsız taramaya çevirir.
var extraWhereBanned = []string{";", "--", "/*"}

// Normalize — PUT ve test-connection'ın ORTAK kapısı: kırpar, doğrular,
// varsayılanları basar, id'leri önceki kayıttan taşır. Girdiyi DEĞİŞTİRMEZ
// (derin kopya: TypeFilter dilimi de kopyalanır). Hata mesajları Settings
// formuna aynen gider — Türkçe ve hangi kaynak olduğunu söyler.
func Normalize(in Settings, prev Settings, newID func() string) (Settings, error) {
	if newID == nil {
		newID = NewSourceID
	}
	if len(in.Sources) > maxSources {
		return Settings{}, fmt.Errorf("en çok %d kaynak tanımlanabilir", maxSources)
	}
	byID := map[string]SourceConfig{}
	byName := map[string]SourceConfig{}
	for _, p := range prev.Sources {
		if p.ID != "" {
			byID[p.ID] = p
		}
		byName[strings.ToLower(strings.TrimSpace(p.Name))] = p
	}
	out := Settings{Sources: make([]SourceConfig, 0, len(in.Sources))}
	seen := map[string]bool{}
	for i, src := range in.Sources {
		label := fmt.Sprintf("kaynak #%d", i+1)
		s := SourceConfig{
			ID:              strings.TrimSpace(src.ID),
			Name:            strings.TrimSpace(src.Name),
			DSN:             strings.TrimSpace(src.DSN),
			Host:            strings.TrimSpace(src.Host),
			Port:            src.Port,
			ServiceName:     strings.TrimSpace(src.ServiceName),
			User:            strings.TrimSpace(src.User),
			Password:        strings.TrimSpace(src.Password),
			PasswordRef:     strings.TrimSpace(src.PasswordRef),
			Schema:          strings.TrimSpace(src.Schema),
			Table:           strings.TrimSpace(src.Table),
			TimestampColumn: strings.TrimSpace(src.TimestampColumn),
			TypeColumn:      strings.TrimSpace(src.TypeColumn),
			ExtraWhere:      strings.TrimSpace(src.ExtraWhere),
			MaxOpenConns:    src.MaxOpenConns,
			QueryTimeoutSec: src.QueryTimeoutSec,
			IntervalSec:     src.IntervalSec,
			Enabled:         src.Enabled,
		}
		if s.Name == "" {
			return Settings{}, fmt.Errorf("%s: ad zorunlu", label)
		}
		if !sourceNameRe.MatchString(s.Name) {
			return Settings{}, fmt.Errorf("%s: ad yalnız harf/rakam/._- içerebilir (≤%d)", label, maxNameLen)
		}
		label = fmt.Sprintf("kaynak %q", s.Name)
		key := strings.ToLower(s.Name)
		if seen[key] {
			return Settings{}, fmt.Errorf("%s: kaynak adı tekil olmalı", label)
		}
		seen[key] = true

		// ID: sunucu sahipli. Tanınan id korunur; tanınmayan/boş id ada göre
		// saklı kayda bağlanır; hiçbiri yoksa yeni.
		prevRec, hasPrev := byID[s.ID]
		if !hasPrev {
			if p, ok := byName[key]; ok && p.ID != "" {
				s.ID, prevRec, hasPrev = p.ID, p, true
			} else {
				s.ID = newID()
			}
		}
		// Boş şifre girdisi SAKLIYI KORUR: form şifreyi geri alamaz (GET
		// maskeler), her kayıt boş yollar.
		if s.Password == "" && hasPrev {
			s.Password = prevRec.Password
		}

		if s.PasswordRef != "" && !secretref.Valid(s.PasswordRef) {
			return Settings{}, fmt.Errorf("%s: passwordRef `env:NAME` ya da `file:/path` biçiminde olmalı", label)
		}

		// Bağlantı biçimi: ya DSN ya host/port/serviceName — ikisi birden
		// verilirse hangisinin kazandığı belirsiz kalır.
		if s.DSN != "" && (s.Host != "" || s.ServiceName != "") {
			return Settings{}, fmt.Errorf("%s: ya dsn ya host/port/serviceName verin, ikisi birden değil", label)
		}
		if s.DSN != "" && !dsnRe.MatchString(s.DSN) {
			return Settings{}, fmt.Errorf("%s: dsn `oracle://kullanıcı:şifre@host:port/servis` biçiminde olmalı", label)
		}
		if s.DSN == "" {
			if s.Port == 0 {
				s.Port = DefaultPort
			}
			if s.Port < 1 || s.Port > 65535 {
				return Settings{}, fmt.Errorf("%s: port 1-65535 arasında olmalı", label)
			}
		} else if s.Port != 0 {
			return Settings{}, fmt.Errorf("%s: dsn verildiğinde port dsn'in içindedir", label)
		}

		// Identifier'lar: doluysa HER ZAMAN doğrulanır (kapalı bir taslak da
		// yarın etkinleşir); zorunluluk yalnız etkin kaynakta.
		for _, f := range []struct{ name, val string }{
			{"şema", s.Schema}, {"tablo", s.Table},
			{"timestampColumn", s.TimestampColumn}, {"typeColumn", s.TypeColumn},
		} {
			if f.val != "" && !identRe.MatchString(f.val) {
				return Settings{}, fmt.Errorf("%s: %s adı Oracle identifier'ı olmalı (harfle başlar, harf/rakam/_$# , ≤30)", label, f.name)
			}
		}
		if s.TimestampColumn == "" {
			s.TimestampColumn = DefaultTimestampColumn
		}
		if s.TypeColumn == "" {
			s.TypeColumn = DefaultTypeColumn
		}

		if err := validateExtraWhere(s.ExtraWhere, label); err != nil {
			return Settings{}, err
		}

		// TypeFilter: kırp, boşları at, tekilleştir. Boşsa varsayılan —
		// değer KODA GÖMÜLÜ DEĞİL, ayardan değiştirilebilir.
		tseen := map[string]bool{}
		for _, t := range src.TypeFilter {
			t = strings.TrimSpace(t)
			if t == "" || tseen[t] {
				continue
			}
			if len(t) > maxTypeValueLen {
				return Settings{}, fmt.Errorf("%s: tip değeri en çok %d karakter", label, maxTypeValueLen)
			}
			tseen[t] = true
			s.TypeFilter = append(s.TypeFilter, t)
		}
		if len(s.TypeFilter) == 0 {
			s.TypeFilter = DefaultTypeFilter()
		}
		if len(s.TypeFilter) > maxTypeFilter {
			return Settings{}, fmt.Errorf("%s: en çok %d tip değeri", label, maxTypeFilter)
		}

		// Kelepçeler: 0 = varsayılan, aralık dışı = HATA. Sessiz kırpma
		// operatöre 100 yazdırıp 16 uygulardı — form hatayı görsün.
		if s.MaxOpenConns == 0 {
			s.MaxOpenConns = DefaultMaxOpenConns
		}
		if s.MaxOpenConns < MinMaxOpenConns || s.MaxOpenConns > MaxMaxOpenConns {
			return Settings{}, fmt.Errorf("%s: maxOpenConns %d-%d arasında olmalı", label, MinMaxOpenConns, MaxMaxOpenConns)
		}
		if s.QueryTimeoutSec == 0 {
			s.QueryTimeoutSec = DefaultQueryTimeoutSec
		}
		if s.QueryTimeoutSec < MinQueryTimeoutSec || s.QueryTimeoutSec > MaxQueryTimeoutSec {
			return Settings{}, fmt.Errorf("%s: queryTimeoutSec %d-%d sn arasında olmalı", label, MinQueryTimeoutSec, MaxQueryTimeoutSec)
		}
		if s.IntervalSec == 0 {
			s.IntervalSec = DefaultIntervalSec
		}
		if s.IntervalSec < MinIntervalSec || s.IntervalSec > MaxIntervalSec {
			return Settings{}, fmt.Errorf("%s: aralık %d-%d sn arasında olmalı", label, MinIntervalSec, MaxIntervalSec)
		}

		if s.Enabled {
			if s.DSN == "" {
				if s.Host == "" || s.ServiceName == "" {
					return Settings{}, fmt.Errorf("%s: host ve serviceName zorunlu (ya da tek parça dsn verin)", label)
				}
				if s.User == "" {
					return Settings{}, fmt.Errorf("%s: kullanıcı adı zorunlu", label)
				}
				if s.Password == "" && s.PasswordRef == "" {
					return Settings{}, fmt.Errorf("%s: şifre zorunlu — şifre girin ya da passwordRef (env:/file:) verin", label)
				}
			}
			if s.Schema == "" || s.Table == "" {
				return Settings{}, fmt.Errorf("%s: şema ve tablo zorunlu", label)
			}
		}
		out.Sources = append(out.Sources, s)
	}
	return out, nil
}

// validateExtraWhere — SAF. Uzunluk + yasak dizi kapısı; geçen ifade sorguya
// `AND (…)` olarak girer.
func validateExtraWhere(w, label string) error {
	if w == "" {
		return nil
	}
	if len(w) > MaxExtraWhereLen {
		return fmt.Errorf("%s: extraWhere en çok %d karakter", label, MaxExtraWhereLen)
	}
	for _, bad := range extraWhereBanned {
		if strings.Contains(w, bad) {
			return fmt.Errorf("%s: extraWhere %q içeremez (ikinci ifade / yorum sorgunun kalanını susturur)", label, bad)
		}
	}
	return nil
}
