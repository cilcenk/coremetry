package oracle

// client.go — Oracle bağlantısı + salt-okunur örnek sorgu (v0.10.580,
// Aşama 1; audit docs/audit/oracle-error-log-2026-09-09.md §1, §3).
//
// Sürücü: github.com/sijms/go-ora/v2 — TNS'in SAF Go uygulaması, CGO
// istemez. godror değerlendirildi ve REDDEDİLDİ: CGO + Oracle Instant
// Client ister, Instant Client'ın musl build'i yok, Dockerfile
// CGO_ENABLED=0 + alpine:3.20 ile doğrudan çelişir ve imaja 35-80 MB
// bindirir ("tek binary, tek imaj" kısıtı).
//
// SQL disiplini (audit §3), üçü de bu dosyada zorunlu:
//   1. YALNIZ SELECT üretilir — başka ifade türü kurulmaz.
//   2. DEĞERLER daima bind (:1, :2, …). String concat ile yüklem
//      kurulmaz; Grafana'daki LIKE '<operationcode>' kalıbı taşınmadı.
//   3. Identifier'lar (şema/tablo/kolon) bind EDİLEMEZ; interpolasyona
//      yalnız identRe'den geçmiş adlar girer ve builder Normalize'ın
//      koştuğuna GÜVENMEZ, kendisi de doğrular.
//
// Şifre ve DSN hiçbir log satırına, hiçbir hata metnine, hiçbir API
// cevabına girmez: dışarı çıkan her hata redactSecrets'tan geçer.

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	go_ora "github.com/sijms/go-ora/v2"
)

// sqlDB — database/sql seam'i. Üretimde *sql.DB; testler sahte verebilir
// (Service.openDB alanı).
type sqlDB interface {
	PingContext(ctx context.Context) error
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	SetMaxOpenConns(n int)
	SetMaxIdleConns(n int)
	SetConnMaxLifetime(d time.Duration)
	Close() error
}

// openOracleDB — go-ora sürücüsü ("oracle" adıyla kendi init'inde
// sql.Register'lanır). DSN LOG'A BASILMAZ.
func openOracleDB(dsn string) (sqlDB, error) {
	db, err := sql.Open("oracle", dsn)
	if err != nil {
		return nil, err
	}
	return db, nil
}

const (
	// testSampleLimit — bağlantı testinin FETCH FIRST tavanı.
	testSampleLimit = 5
	// testWindow — testin baktığı pencere: son 15 dakika. Zaman yüklemi
	// ZORUNLU (audit §3: sınırsız tarama yok).
	testWindow = 15 * time.Minute
	// wideTestWindow — dar pencere boş dönerse ikinci deneme. "Trafik yok"
	// ile "TZ kaymış" ayrımını yapan tek şey bu (v0.10.335 Influx dersi).
	wideTestWindow = 24 * time.Hour
	// sampleValueMax — örnek hücre kırpması (CLOB'lu tabloda cevap şişmesin).
	sampleValueMax = 200
	// maxSampleLimit — builder'ın kabul ettiği en büyük satır tavanı.
	maxSampleLimit = 50
)

// buildSampleQuery — SAF ve testin doğrudan pinlediği seam. Üretilen metin
// TEK bir SELECT'tir; zaman ve tip değerleri bind, identifier'lar
// doğrulanmış. limit bir int'tir ve %d ile basılır — enjekte edilecek bir
// dize yoktur (bind edilmemesinin sebebi: FETCH FIRST bind'i sürücüden
// sürücüye değişiyor, sayıysa hiçbir riski yok).
func buildSampleQuery(cfg SourceConfig, from, to time.Time, limit int) (string, []any, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > maxSampleLimit {
		limit = maxSampleLimit
	}
	tsCol := cfg.TimestampColumn
	if tsCol == "" {
		tsCol = DefaultTimestampColumn
	}
	typCol := cfg.TypeColumn
	if typCol == "" {
		typCol = DefaultTypeColumn
	}
	for _, f := range []struct{ name, val string }{
		{"şema", cfg.Schema}, {"tablo", cfg.Table},
		{"timestampColumn", tsCol}, {"typeColumn", typCol},
	} {
		if !identRe.MatchString(f.val) {
			return "", nil, fmt.Errorf("%s adı Oracle identifier'ı olmalı: %q", f.name, f.val)
		}
	}
	if err := validateExtraWhere(cfg.ExtraWhere, "extraWhere"); err != nil {
		return "", nil, err
	}
	types := cfg.TypeFilter
	if len(types) == 0 {
		types = DefaultTypeFilter()
	}

	args := []any{from, to}
	var b strings.Builder
	// Kolon listesi yerine * : Aşama 2'nin alan eşlemesini yazacak operatör
	// tablonun GERÇEK kolonlarını testte görmeli (FETCH FIRST tavanı zaten var).
	fmt.Fprintf(&b, "SELECT * FROM %s.%s\nWHERE %s >= :1 AND %s < :2", cfg.Schema, cfg.Table, tsCol, tsCol)
	binds := make([]string, 0, len(types))
	for _, t := range types {
		args = append(args, t)
		binds = append(binds, fmt.Sprintf(":%d", len(args)))
	}
	fmt.Fprintf(&b, "\n  AND %s IN (%s)", typCol, strings.Join(binds, ", "))
	if cfg.ExtraWhere != "" {
		fmt.Fprintf(&b, "\n  AND (%s)", cfg.ExtraWhere)
	}
	fmt.Fprintf(&b, "\nORDER BY %s DESC\nFETCH FIRST %d ROWS ONLY", tsCol, limit)
	return b.String(), args, nil
}

// dsnFor — bağlantı dizesi. ASLA log'lanmaz, ASLA API cevabına girmez;
// dönen dize yalnız sql.Open'a gider. İkinci dönüş şifre (redaksiyon için).
func (s *Service) dsnFor(src SourceConfig) (dsn, secret string, err error) {
	pass, err := s.passwordFor(src)
	if err != nil {
		return "", "", err
	}
	if src.DSN != "" {
		return src.DSN, pass, nil
	}
	port := src.Port
	if port == 0 {
		port = DefaultPort
	}
	return go_ora.BuildUrl(src.Host, port, src.ServiceName, src.User, pass, nil), pass, nil
}

// open — havuz ayarlı bağlantı. Havuz kelepçeleri Normalize'dan gelir;
// gelmediyse (çıplak cfg) varsayılana düşer.
func (s *Service) open(src SourceConfig) (sqlDB, string, error) {
	dsn, secret, err := s.dsnFor(src)
	if err != nil {
		return nil, "", err
	}
	openFn := s.openDB
	if openFn == nil {
		openFn = openOracleDB
	}
	db, err := openFn(dsn)
	if err != nil {
		return nil, secret, fmt.Errorf("bağlantı açılamadı: %s", redactSecrets(err.Error(), secret, dsn))
	}
	maxOpen := src.MaxOpenConns
	if maxOpen < MinMaxOpenConns || maxOpen > MaxMaxOpenConns {
		maxOpen = DefaultMaxOpenConns
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxOpen)
	db.SetConnMaxLifetime(5 * time.Minute)
	return db, secret, nil
}

// queryTimeout — kaynağın sorgu bütçesi (kelepçe dışıysa varsayılan).
func queryTimeout(src SourceConfig) time.Duration {
	sec := src.QueryTimeoutSec
	if sec < MinQueryTimeoutSec || sec > MaxQueryTimeoutSec {
		sec = DefaultQueryTimeoutSec
	}
	return time.Duration(sec) * time.Second
}

// Ping — yalnız erişilebilirlik. Bağlantı kurulamazsa Coremetry'nin geri
// kalanı etkilenmez: hata döner, hiçbir global durum değişmez.
func (s *Service) Ping(ctx context.Context, src SourceConfig) error {
	if s == nil {
		return fmt.Errorf("oracle servisi yok")
	}
	db, secret, err := s.open(src)
	if err != nil {
		return err
	}
	defer db.Close()
	pctx, cancel := context.WithTimeout(ctx, queryTimeout(src))
	defer cancel()
	if err := db.PingContext(pctx); err != nil {
		return fmt.Errorf("bağlantı: %s", redactSecrets(err.Error(), secret))
	}
	return nil
}

// TestResult — POST /api/settings/oracle/test cevabı. Bağlantı denemesinin
// BAŞARISIZLIĞI operatörün sorusuna BAŞARILI bir cevaptır: uç 200 + ok:false
// döner (influx TestResult sözleşmesi).
type TestResult struct {
	OK               bool                `json:"ok"`
	Error            string              `json:"error,omitempty"`
	PasswordResolved bool                `json:"passwordResolved"`
	Columns          []string            `json:"columns"`
	Sample           []map[string]string `json:"sample,omitempty"`
	RowCount         int                 `json:"rowCount"`
	LatencyMs        int64               `json:"latencyMs"`
	// Query — koşan SELECT'in metni. Şifre/DSN içermez (yalnız identifier'lar
	// ve bind yer tutucuları); operatör Aşama 2'nin sorgusunu burada görür.
	Query string `json:"query,omitempty"`
	// WideWindow — örnek satırlar 24 saatlik ikinci denemeden geldi (dar
	// pencere boştu). Arayüz bunu SÖYLEMELİ: aksi hâlde operatör 15 dakikalık
	// pencerede veri var sanır.
	WideWindow bool `json:"wideWindow,omitempty"`
	// Hint — "hata yok + satır yok" ikircikliğinin okunabilir açıklaması.
	Hint string `json:"hint,omitempty"`
}

// Test — formdaki kaynağı KAYDETMEDEN dener: şifre çözümü → Ping → son 15
// dakikadan en çok 5 satır. Dönen kolon listesi ve örnek satırlar Aşama 2'nin
// alan eşlemesini yazacak operatörün elindeki tek gerçek kanıt.
func (s *Service) Test(ctx context.Context, src SourceConfig) TestResult {
	res := TestResult{Columns: []string{}}
	if s == nil {
		res.Error = "oracle servisi yok"
		return res
	}
	defer func() { s.recordCheck(src, res) }()

	// Şifre çözümü ÖNCE ve ayrı: passwordResolved rozeti "env yok / dosya
	// yok"u sürücü hatasından ayırt etmek için var; open'ın içinde kalsaydı
	// bozuk bir DSN de çözülmemiş şifre gibi görünürdü.
	if _, err := s.passwordFor(src); err != nil {
		res.Error = err.Error()
		return res
	}
	res.PasswordResolved = true

	db, secret, err := s.open(src)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer db.Close()

	sqlText, args, err := buildSampleQuery(src, time.Now().Add(-testWindow), time.Now(), testSampleLimit)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Query = sqlText

	budget := queryTimeout(src)
	start := time.Now()
	pctx, pcancel := context.WithTimeout(ctx, budget)
	err = db.PingContext(pctx)
	pcancel()
	if err != nil {
		res.LatencyMs = time.Since(start).Milliseconds()
		res.Error = "bağlantı: " + redactSecrets(err.Error(), secret)
		return res
	}

	cols, sample, qerr := runSample(ctx, db, sqlText, args, budget, secret)
	res.LatencyMs = time.Since(start).Milliseconds()
	if qerr != nil {
		res.Error = qerr.Error()
		return res
	}
	res.Columns, res.Sample, res.RowCount = cols, sample, len(sample)

	// v0.10.580 — GENİŞ PENCERE İKİNCİ DENEMESİ. Hata yok + satır yok
	// İKİRCİKLİ bir cevaptır: trafik mi yok, zaman kolonu başka bir dilimde
	// mi (TZ), tip süzgeci mi tutmadı — operatör ayıramaz. Influx bunu
	// prod'da öğrendi (v0.10.335). İkinci deneme ayrımı METNE döker ve
	// örnek satırları da getirir: alan eşlemesini yazacak operatör
	// tablonun gerçek zaman damgası biçimini burada görür.
	if len(sample) == 0 {
		wideSQL, wideArgs, werr := buildSampleQuery(src, time.Now().Add(-wideTestWindow), time.Now(), testSampleLimit)
		if werr == nil {
			wcols, wsample, werr2 := runSample(ctx, db, wideSQL, wideArgs, budget, secret)
			hint, useWide := emptyProbeHint(len(wsample), werr2)
			res.Hint = hint
			if useWide {
				res.Columns, res.Sample, res.WideWindow = wcols, wsample, true
			}
		}
	}

	res.OK = true
	return res
}

// emptyProbeHint — SAF: dar pencere boş dönünce ne söyleneceğine karar verir.
// İkinci dönüş, geniş pencerenin örneklerinin KULLANILACAĞINI söyler.
//
// Üç durumun üçü de FARKLI bir eylem gerektirir ve operatör bunları ayırt
// edemezse yanlış yerde arar: geniş pencerede veri VARSA sorun zaman
// dilimi ya da seyrek trafiktir; geniş pencerede de yoksa sorun süzgeç ya
// da ad eşleşmesidir; geniş deneme HATA verirse ortada bir teşhis yoktur.
func emptyProbeHint(wideRows int, wideErr error) (string, bool) {
	switch {
	case wideErr != nil:
		return "son 15 dakikada satır yok; geniş pencere denemesi de başarısız (" + wideErr.Error() + ")", false
	case wideRows > 0:
		return "Son 15 dakikada satır YOK ama son 24 saatte var — trafik seyrek olabilir ya da " +
			"zaman kolonu beklenenden farklı bir dilimde (TZ). Aşağıdaki örnek satırlar 24 saatlik pencereden.", true
	default:
		return "Son 24 saatte de satır yok — tip süzgeci, şema/tablo adı ya da zaman kolonu eşleşmiyor olabilir.", false
	}
}

// runSample — SORGUYU KOŞAR ve satırları dizeye çevirir. Test iki kez
// çağırıyor (dar pencere, sonra geniş); tek gövde olması iki denemenin
// aynı kırpma ve redaksiyon kurallarını paylaşmasını garanti eder.
func runSample(ctx context.Context, db sqlDB, sqlText string, args []any, budget time.Duration, secret string) ([]string, []map[string]string, error) {
	qctx, qcancel := context.WithTimeout(ctx, budget)
	defer qcancel()
	rows, err := db.QueryContext(qctx, sqlText, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("sorgu: %s", redactSecrets(err.Error(), secret))
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, fmt.Errorf("kolonlar: %s", redactSecrets(err.Error(), secret))
	}
	var out []map[string]string
	for rows.Next() {
		cells := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, fmt.Errorf("satır: %s", redactSecrets(err.Error(), secret))
		}
		row := make(map[string]string, len(cols))
		for i, c := range cols {
			row[c] = formatCell(cells[i])
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("satır akışı: %s", redactSecrets(err.Error(), secret))
	}
	return cols, out, nil
}

// formatCell — SAF: bir hücreyi görüntülenebilir dizeye çevirir ve kırpar.
func formatCell(v any) string {
	var s string
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		s = string(t)
	case time.Time:
		s = t.UTC().Format(time.RFC3339Nano)
	case string:
		s = t
	default:
		s = fmt.Sprint(t)
	}
	if len(s) > sampleValueMax {
		return s[:sampleValueMax] + "…"
	}
	return s
}

// dsnCredRe — `oracle://kullanıcı:şifre@host` içindeki şifreyi yakalar.
var dsnCredRe = regexp.MustCompile(`(?i)(oracle://[^:@/\s]*):[^@\s]*@`)

// redactSecrets — SAF: dışarı çıkan her hata metninden şifreyi ve DSN'i
// siler. go-ora bazı hatalara bağlantı dizesini ekliyor; o dize kullanıcı
// adı ve şifre taşır. "Şifre log'a ASLA girmez" bir temenni değil, bu
// fonksiyonun sözleşmesi.
func redactSecrets(msg string, secrets ...string) string {
	out := dsnCredRe.ReplaceAllString(msg, "$1:***@")
	for _, sec := range secrets {
		if len(sec) < 3 {
			continue // çok kısa: metnin her yerine rastlar, mesajı okunmaz eder
		}
		out = strings.ReplaceAll(out, sec, "***")
	}
	return out
}
