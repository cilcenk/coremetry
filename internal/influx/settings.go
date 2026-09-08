// Package influx — InfluxDB 2.x'i DIŞ METRİK KAYNAĞI olarak bağlar
// (v0.10.222, audit: docs/audit/influx-integration.md, operatör onayı
// 2026-09-01).
//
// Yapı internal/thanos/client.go'nun simetriği (o da internal/tempo'nun):
// system_settings["influx_sources"] altında tipli Settings blobu, dar
// settingsStore arayüzü, boot'ta LoadPersisted + 30 s StartConfigRefresh
// (çok-pod senkron), SavePersisted + canlı Configure takası, Settings UI
// için Snapshot. Token: v0.10.224 operatör kararı ("düz token saklanabilir,
// maskeli yaparsın") — tempo/thanos/VM sözleşmesi: blob'da saklanır, GET
// asla geri vermez (HasToken), boş girdi saklıyı korur. Alternatif olarak
// REFERANS (`env:NAME` | `file:/path`, secret.go) — varsa o kazanır ve
// kullanım anında çözülür. Audit K5'in "yalnız referans" hâli v0.10.222-223
// arasında yaşadı; operatör formda token yapıştırınca reddedilmesini
// istemedi.
//
// D1 (bu dosya + client/csv/template/secret): kaynak yönetimi + test.
// D2 poller → metric_points, D3 dış anomali, D4 enrichment ayrı dilimler.
package influx

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultIntervalSec — spec: 30 s poll. Kelepçeler: 10 s altı Influx'a
	// gereksiz yük (ve dedektörün 1-dk kovası için anlamsız), 1 saat üstü
	// baseline penceresini (4 saat) boş bırakır.
	DefaultIntervalSec = 30
	MinIntervalSec     = 10
	MaxIntervalSec     = 3600
	maxSources         = 20
	maxQueries         = 10
	maxDwell           = 20
)

// Thresholds — kaynak/sorgu başına anomali eşikleri (D3'te kullanılır).
// 0 = global anomaly_sensitivity varsayılanı (audit §3 politika satırı).
type Thresholds struct {
	CriticalZ   float64 `json:"criticalZ,omitempty"`
	Dwell       int     `json:"dwell,omitempty"`
	MinAbsDelta float64 `json:"minAbsDelta,omitempty"`
	MinMAD      float64 `json:"minMAD,omitempty"`
}

// RatioSpec — v0.10.532: TÜRETİLMİŞ oran sorgusu (Flux YOK). Aynı kaynaktaki
// iki Flux sorgusunun kovaları (groupBy değerleri, _time) anahtarında bellekte
// birleşir; değer = RatioScale × pay ÷ payda (yüzde). Influx'a ek sorgu gitmez;
// seri `ext:<ad>` olarak aynı yazım/anomali/kanıt yolundan geçer (ratio.go).
// Operatör kararı 2026-09-07 (hata oranı: REDACTED ÷ toplam).
type RatioSpec struct {
	Numerator   string `json:"numerator"`
	Denominator string `json:"denominator"`
	// MinDenominator — altında kalan kova YAZILMAZ (0 değil, boşluk): 1/2 =
	// %50 gürültüsü. 0/yok = RatioDefaultMinDenominator.
	MinDenominator float64 `json:"minDenominator,omitempty"`
	// SettleBuckets — pay sorgusu hiç satır vermediğinde paydanın en yeni
	// kaç kovasının bekletileceği (kaynak gecikmeli yazar; sahte %0 olmasın).
	// Pay satır verdiyse kural sabittir: payın en yeni kovasından sonrası
	// bekler. 0/yok = RatioDefaultSettle.
	SettleBuckets int `json:"settleBuckets,omitempty"`
}

// QueryConfig — bir poll sorgusu. Name metrik adının kuyruğu olur
// (`ext:<name>`, audit §7); Flux SORGU 1 (poll), EnrichFlux SORGU 2
// ({{from}}/{{to}}/{{op}}/{{err}} yer tutucuları, template.go). GroupBy
// hangi Influx tag'lerinin SERİ BOYUTU olacağını söyler (v1: REDACTED
// + REDACTED); AttrMap tag → Coremetry attribute adı (operation,
// error.code, k8s.pod.name …). GroupBy dışı tag'ler seriye girmez,
// enrichment'ta exemplar'a biner (kardinalite kapısı, audit K4).
type QueryConfig struct {
	Name       string            `json:"name"`
	Flux       string            `json:"flux"`
	EnrichFlux string            `json:"enrichFlux,omitempty"`
	AttrMap    map[string]string `json:"attrMap,omitempty"`
	GroupBy    []string          `json:"groupBy,omitempty"`
	Thresholds Thresholds        `json:"thresholds,omitempty"`
	// Ratio — doluysa bu sorgu türetilmiştir: Flux boş, Influx'a gitmez.
	Ratio *RatioSpec `json:"ratio,omitempty"`
}

// SourceConfig — bir Influx kurulumu. ID sunucu sahipli ("i-" + 8 hex,
// thanos ClusterConfig.ID sözleşmesi): PUT istemciden gelen id'yi
// tanıyorsa korur, tanımıyorsa ada göre saklı kayıttan alır, yoksa
// türetir. Name `service_name` olur (audit K4) — tekil.
type SourceConfig struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	URL  string `json:"url"`
	Org  string `json:"org"`
	// Token — saklı düz token (GET'te ASLA; Snapshot HasToken). Boş PUT
	// saklıyı korur (Normalize prev'den taşır).
	Token string `json:"token,omitempty"`
	// TokenRef — `env:NAME` | `file:/path`; doluysa Token'a tercih edilir.
	TokenRef           string        `json:"tokenRef,omitempty"`
	IntervalSec        int           `json:"intervalSec,omitempty"`
	InsecureSkipVerify bool          `json:"insecureSkipVerify,omitempty"`
	Enabled            bool          `json:"enabled"`
	Queries            []QueryConfig `json:"queries"`
}

// Settings — kalıcı blob: tüm liste atomik yazılır (thanos/custom_roles
// sözleşmesi; N≤20 ölçeğinde satır-düzeyi yarış yok).
type Settings struct {
	Sources []SourceConfig `json:"sources"`
}

// SourceSnapshot — GET görünümü. Token MASKELİ (gömülü SourceConfig'in
// Token'ı boşaltılır, HasToken söyler); tokenRef bir REFERANS (secret
// değil), aynen görünür. TokenResolved/TokenError Settings rozeti:
// operatör "env yok / dosya yok"u sessiz 401 yerine kaydetmeden görür.
type SourceSnapshot struct {
	SourceConfig
	HasToken      bool   `json:"hasToken"`
	TokenResolved bool   `json:"tokenResolved"`
	TokenError    string `json:"tokenError,omitempty"`
}

// Snapshot — GET /api/settings/influx cevabı.
type Snapshot struct {
	Sources []SourceSnapshot `json:"sources"`
}

// Service — ayar sahibi + HTTP istemcileri. Zoom/thanos iki-tekil deseni:
// doğrulayan istemci + tembel kurulan insecure ikizi, kaynak başına
// InsecureSkipVerify bayrağıyla seçilir (thanosClientFor).
type Service struct {
	mu  sync.RWMutex
	cfg Settings

	verify       *http.Client
	insecureOnce sync.Once
	insecure     *http.Client

	// getenv/readFile — tokenRef çözümü; testler enjekte eder.
	getenv   func(string) string
	readFile func(string) ([]byte, error)
}

// New — boş liste; LoadPersisted blobu getirir.
func New() *Service {
	return &Service{
		verify:   newHTTPClient(false),
		getenv:   os.Getenv,
		readFile: os.ReadFile,
	}
}

func newHTTPClient(insecureSkipVerify bool) *http.Client {
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if insecureSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec — operatör bayrağı, kaynak başına
	}
	return &http.Client{Timeout: 20 * time.Second, Transport: tr}
}

func (s *Service) clientFor(insecureSkipVerify bool) *http.Client {
	if !insecureSkipVerify {
		return s.verify
	}
	s.insecureOnce.Do(func() { s.insecure = newHTTPClient(true) })
	return s.insecure
}

// settingsStore — chstore.Store'un bu paket için gereken iki metodu
// (chstore/influx.go). Dar arayüz: testler sahte store verebilir.
type settingsStore interface {
	GetInfluxSettingsRaw(ctx context.Context) ([]byte, error)
	PutInfluxSettingsRaw(ctx context.Context, raw []byte) error
}

// LoadPersisted — system_settings'ten hidrate eder. Blob yoksa boş liste.
func (s *Service) LoadPersisted(ctx context.Context, store settingsStore) error {
	if s == nil || store == nil {
		return nil
	}
	raw, err := store.GetInfluxSettingsRaw(ctx)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var cfg Settings
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("influx decode: %w", err)
	}
	s.Configure(cfg)
	return nil
}

// StartConfigRefresh — çok-pod senkron: admin PUT hangi pod'a düştüyse
// diğerleri interval içinde aynı blobu okur (tempo/thanos 30 s).
func (s *Service) StartConfigRefresh(ctx context.Context, store settingsStore, interval time.Duration) {
	if s == nil || store == nil {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.LoadPersisted(ctx, store); err != nil {
				log.Printf("[influx] config refresh: %v", err)
			}
		}
	}
}

// SavePersisted — blobu yazar, sonra canlı takas. Çağıran Normalize'dan
// geçmiş bir cfg vermeli (handler bunu yapar).
func (s *Service) SavePersisted(ctx context.Context, store settingsStore, cfg Settings) error {
	if s == nil || store == nil {
		return nil
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := store.PutInfluxSettingsRaw(ctx, raw); err != nil {
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

// CurrentSettings — saklı ayarın kopyası DEĞİL, paylaşılan dilimler:
// çağıran değiştirmez (Normalize derin kopya üretir).
func (s *Service) CurrentSettings() Settings {
	if s == nil {
		return Settings{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// HasEnabledSources — D2 poller'ın "iş var mı" sorusu.
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

// Snapshot — GET görünümü; tokenRef çözülebilirliği rozet olarak.
func (s *Service) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{Sources: []SourceSnapshot{}}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := Snapshot{Sources: make([]SourceSnapshot, 0, len(s.cfg.Sources))}
	for _, src := range s.cfg.Sources {
		ss := SourceSnapshot{SourceConfig: src, HasToken: src.Token != ""}
		ss.Token = "" // maskele — GET asla geri vermez
		if src.Queries == nil {
			ss.Queries = []QueryConfig{}
		}
		if _, err := s.tokenFor(src); err != nil {
			ss.TokenError = err.Error()
		} else {
			ss.TokenResolved = true
		}
		out.Sources = append(out.Sources, ss)
	}
	return out
}

// tokenFor — kaynağın ETKİN token'ı: referans varsa çözülür (rotasyon
// anında etkili), yoksa saklı düz token. İkisi de yoksa hata.
func (s *Service) tokenFor(src SourceConfig) (string, error) {
	if src.TokenRef != "" {
		return resolveTokenRef(src.TokenRef, s.getenv, s.readFile)
	}
	if src.Token != "" {
		return src.Token, nil
	}
	return "", fmt.Errorf("kaynak %q: token yok (düz token yapıştırın ya da tokenRef verin)", src.Name)
}

// NewSourceID — "i-" + 8 hex (thanos "c-" ailesi). Sunucu sahipli.
func NewSourceID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("i-%08x", time.Now().UnixNano()&0xffffffff)
	}
	return "i-" + hex.EncodeToString(b[:])
}

var (
	sourceNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.\-]{0,63}$`)
	queryNameRe  = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)
)

// Normalize — PUT ve test-connection'ın ORTAK kapısı: kırpar, doğrular,
// varsayılanları basar, id'leri önceki kayıttan taşır. Girdiyi değiştirmez
// (derin kopya). Hata mesajları Settings formuna aynen gider — Türkçe ve
// hangi kaynak/sorgu olduğunu söyler.
func Normalize(in Settings, prev Settings, newID func() string) (Settings, error) {
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
			ID:                 strings.TrimSpace(src.ID),
			Name:               strings.TrimSpace(src.Name),
			URL:                strings.TrimSpace(src.URL),
			Org:                strings.TrimSpace(src.Org),
			Token:              strings.TrimSpace(src.Token),
			TokenRef:           strings.TrimSpace(src.TokenRef),
			IntervalSec:        src.IntervalSec,
			InsecureSkipVerify: src.InsecureSkipVerify,
			Enabled:            src.Enabled,
			Queries:            []QueryConfig{},
		}
		if s.Name == "" {
			return Settings{}, fmt.Errorf("%s: ad zorunlu (service_name olur)", label)
		}
		if !sourceNameRe.MatchString(s.Name) {
			return Settings{}, fmt.Errorf("%s: ad yalnız harf/rakam/._- içerebilir (≤64)", label)
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
		// Boş token girdisi SAKLIYI KORUR (VM mergeVMSettings kuralı): form
		// token'ı geri alamaz (GET maskeler), her kayıt boş yollar.
		if s.Token == "" && hasPrev {
			s.Token = prevRec.Token
		}

		if s.IntervalSec == 0 {
			s.IntervalSec = DefaultIntervalSec
		}
		if s.IntervalSec < MinIntervalSec || s.IntervalSec > MaxIntervalSec {
			return Settings{}, fmt.Errorf("%s: aralık %d-%d sn arasında olmalı", label, MinIntervalSec, MaxIntervalSec)
		}
		// Düz-metin token blob'a HİÇ girmez — kapalı kaynakta bile.
		if s.TokenRef != "" && !ValidTokenRef(s.TokenRef) {
			return Settings{}, fmt.Errorf("%s: tokenRef `env:NAME` ya da `file:/path` biçiminde olmalı (düz token saklanmaz)", label)
		}
		if s.Enabled {
			u, err := url.Parse(s.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return Settings{}, fmt.Errorf("%s: url http(s)://host[:port] biçiminde olmalı", label)
			}
			if s.Org == "" {
				return Settings{}, fmt.Errorf("%s: org zorunlu", label)
			}
			if s.TokenRef == "" && s.Token == "" {
				return Settings{}, fmt.Errorf("%s: token zorunlu — düz token yapıştırın ya da tokenRef (env:/file:) verin", label)
			}
			if len(src.Queries) == 0 {
				return Settings{}, fmt.Errorf("%s: etkin kaynakta en az bir sorgu olmalı", label)
			}
		}
		if len(src.Queries) > maxQueries {
			return Settings{}, fmt.Errorf("%s: en çok %d sorgu", label, maxQueries)
		}
		qseen := map[string]bool{}
		for j, q := range src.Queries {
			qlabel := fmt.Sprintf("%s sorgu #%d", label, j+1)
			nq := QueryConfig{
				Name:       strings.TrimSpace(q.Name),
				Flux:       strings.TrimSpace(q.Flux),
				EnrichFlux: strings.TrimSpace(q.EnrichFlux),
				Thresholds: q.Thresholds,
				Ratio:      normalizeRatio(q.Ratio),
			}
			if !queryNameRe.MatchString(nq.Name) {
				return Settings{}, fmt.Errorf("%s: sorgu adı slug olmalı (a-z, 0-9, _; ≤40)", qlabel)
			}
			if qseen[nq.Name] {
				return Settings{}, fmt.Errorf("%s: sorgu adı tekil olmalı (%q)", label, nq.Name)
			}
			qseen[nq.Name] = true
			for _, g := range q.GroupBy {
				if g = strings.TrimSpace(g); g != "" {
					nq.GroupBy = append(nq.GroupBy, g)
				}
			}
			if len(q.AttrMap) > 0 {
				nq.AttrMap = make(map[string]string, len(q.AttrMap))
				for k, v := range q.AttrMap {
					if k, v = strings.TrimSpace(k), strings.TrimSpace(v); k != "" && v != "" {
						nq.AttrMap[k] = v
					}
				}
			}
			if nq.Ratio != nil && nq.Flux != "" {
				return Settings{}, fmt.Errorf("%s: oran sorgusunda flux boş kalır (pay/payda sorguları koşar)", qlabel)
			}
			if s.Enabled {
				if nq.Ratio == nil && nq.Flux == "" {
					return Settings{}, fmt.Errorf("%s: flux zorunlu", qlabel)
				}
				if len(nq.GroupBy) == 0 {
					return Settings{}, fmt.Errorf("%s: groupBy zorunlu (en az bir tag; v1: REDACTED, REDACTED)", qlabel)
				}
			}
			th := nq.Thresholds
			if th.CriticalZ < 0 || th.Dwell < 0 || th.MinAbsDelta < 0 || th.MinMAD < 0 {
				return Settings{}, fmt.Errorf("%s: eşik negatif olamaz", qlabel)
			}
			if th.Dwell > maxDwell {
				return Settings{}, fmt.Errorf("%s: dwell en çok %d kova", qlabel, maxDwell)
			}
			s.Queries = append(s.Queries, nq)
		}
		if err := validateRatios(s, label); err != nil {
			return Settings{}, err
		}
		out.Sources = append(out.Sources, s)
	}
	return out, nil
}

func normalizeRatio(r *RatioSpec) *RatioSpec {
	if r == nil {
		return nil
	}
	return &RatioSpec{
		Numerator: strings.TrimSpace(r.Numerator), Denominator: strings.TrimSpace(r.Denominator),
		MinDenominator: r.MinDenominator, SettleBuckets: r.SettleBuckets,
	}
}

// validateRatios — v0.10.532: oran sorgusunun girdileri AYNI kaynakta ve Flux
// türünde olmalı; aksi hâlde birleşim sessizce boş döner ve seri hiç doğmazdı.
// groupBy sözleşmesi (v0.10.548): oranın taneciği PAYDANIN taneciğidir —
// payda groupBy'ı oranınkiyle birebir + aynı sırada (fingerprint groupBy
// sırasına bağlı, BuildMetricsRequest); PAY oranın her anahtarını taşımak
// zorunda ama fazlasını da taşıyabilir — fazla boyut birleşimde toplanır
// (ratioKey yalnız oranın anahtarlarını okur, numBy[k] += v). Gerekçe: REDACTED
// üç boyutlu (REDACTED × REDACTED × REDACTED, ekibin paneli), toplam
// bucket'ında REDACTED tag'ı DOĞRULANMADI; oran kanal × operasyonda kalır.
func validateRatios(src SourceConfig, label string) error {
	byName := map[string]QueryConfig{}
	for _, q := range src.Queries {
		byName[q.Name] = q
	}
	for _, q := range src.Queries {
		r := q.Ratio
		if r == nil {
			continue
		}
		ql := fmt.Sprintf("%s sorgu %q", label, q.Name)
		if r.Numerator == "" || r.Denominator == "" {
			return fmt.Errorf("%s: oran için pay ve payda sorgu adı zorunlu", ql)
		}
		if r.Numerator == r.Denominator {
			return fmt.Errorf("%s: pay ve payda aynı sorgu olamaz", ql)
		}
		for _, side := range []struct{ role, name string }{{"pay", r.Numerator}, {"payda", r.Denominator}} {
			in, ok := byName[side.name]
			if !ok {
				return fmt.Errorf("%s: %s sorgusu %q bu kaynakta yok", ql, side.role, side.name)
			}
			if in.Ratio != nil {
				return fmt.Errorf("%s: %s sorgusu %q bir oran; oran orana bağlanamaz", ql, side.role, side.name)
			}
			if side.role == "pay" {
				if missing := missingStrings(q.GroupBy, in.GroupBy); len(missing) > 0 {
					return fmt.Errorf("%s: pay sorgusunun groupBy'ı oranın her anahtarını taşımalı; eksik %v (pay %v)", ql, missing, in.GroupBy)
				}
				continue
			}
			if !sameStrings(in.GroupBy, q.GroupBy) {
				return fmt.Errorf("%s: groupBy %s sorgusuyla aynı (ve aynı sırada) olmalı: %v ≠ %v", ql, side.role, q.GroupBy, in.GroupBy)
			}
		}
		if r.MinDenominator < 0 {
			return fmt.Errorf("%s: min payda negatif olamaz", ql)
		}
		if r.SettleBuckets < 0 || r.SettleBuckets > ratioMaxSettle {
			return fmt.Errorf("%s: bekletme 0–%d kova", ql, ratioMaxSettle)
		}
	}
	return nil
}

// missingStrings — want'ın have'de bulunmayan öğeleri (sıra önemsiz).
func missingStrings(want, have []string) []string {
	set := make(map[string]bool, len(have))
	for _, h := range have {
		set[h] = true
	}
	var out []string
	for _, w := range want {
		if !set[w] {
			out = append(out, w)
		}
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// unit — metric_points birimi: oran yüzde, Flux sorgusu birimsiz (sayım).
func (qc QueryConfig) unit() string {
	if qc.Ratio != nil {
		return "%"
	}
	return ""
}

// metricAttrKey — bir Influx tag'ının metric_points attr adı: attrMap'te
// boş-olmayan eşleme varsa o, yoksa tag'ın kendisi. BuildMetricsRequest ile
// MetricGroupBy AYNI kuralı buradan okur (sözleşme tek yerde, v0.10.228).
func (qc QueryConfig) metricAttrKey(tag string) string {
	if mapped, has := qc.AttrMap[tag]; has && mapped != "" {
		return mapped
	}
	return tag
}

// MetricGroupBy (v0.10.228) — metric_points'e GERÇEKTEN yazılan attr adları,
// GroupBy sırasıyla. Dış anomali tarayıcısı seri GroupBy'ını bununla kurar;
// ham tag adıyla kurulsaydı seri boş döner, hiçbir şey açılmazdı — sessizce.
func (qc QueryConfig) MetricGroupBy() []string {
	out := make([]string, 0, len(qc.GroupBy))
	for _, tag := range qc.GroupBy {
		out = append(out, qc.metricAttrKey(tag))
	}
	return out
}
