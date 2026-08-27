// cmd/perfcheck — performans bütçesi koşucusu (v0.10.116).
//
// docs/perf/perf-budget-2026-08-28.md AŞAMA 3: budget.json'daki ölçüm
// noktalarını canlı bir Coremetry'ye karşı ARDIŞIK koşar (soğuk =
// refresh=1 → X-Cache BYPASS beklenir; sıcak = aynı istek tekrar), TTFB'yi
// httptrace ile ölçer, medyan/p95 hesaplar, JSON yazar, önceki koşuyla
// kıyaslar ve eşik aşımında exit 1 verir. Karar çekirdeği saf:
// internal/perfcheck. Yeni bağımlılık YOK.
//
// Ortam: docs/perf/perf-budget-2026-08-28.md §0 (minikube port-forward
// 8090→8088, demo yükü ≥2 saat ısınma). Prod'a hiçbir şey YAZMAZ; yalnız
// GET/POST okuma uçları.
//
//	go run ./cmd/perfcheck -budget scripts/perf/budget.json -out perf/out/run.json [-compare perf/out/prev.json]
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/perfcheck"
)

// budgetFile — scripts/perf/budget.json şekli.
type budgetFile struct {
	Schema  int               `json:"schema"`
	BaseURL string            `json:"baseUrl"`
	Runs    int               `json:"runs"`
	Warmup  int               `json:"warmup"`
	Rules   perfcheck.Rules   `json:"rules"`
	Points  []perfcheck.Point `json:"points"`
}

type runner struct {
	base   string
	cli    *http.Client
	runs   int
	warmup int
	warm   bool
	// çözümlenmiş yer tutucular
	service  string
	traceIDs string
}

func main() {
	var (
		budgetPath = flag.String("budget", "scripts/perf/budget.json", "ölçüm noktaları")
		base       = flag.String("base", "", "Coremetry taban adresi (budget.baseUrl'i ezer)")
		runs       = flag.Int("runs", 0, "koşu sayısı (budget.runs'ı ezer)")
		out        = flag.String("out", "", "JSON çıktı yolu (boş = perf/out/<zaman>.json)")
		compare    = flag.String("compare", "", "önceki koşunun JSON'u (kıyas)")
		warm       = flag.Bool("warm", true, "sıcak koşuları da ölç")
		timeout    = flag.Duration("timeout", 90*time.Second, "istek başına tavan")
		email      = flag.String("email", envOr("COREMETRY_PERF_EMAIL", "admin@coremetry.local"), "giriş e-postası")
		password   = flag.String("password", envOr("COREMETRY_PERF_PASSWORD", "admin"), "giriş parolası")
	)
	flag.Parse()

	raw, err := os.ReadFile(*budgetPath)
	if err != nil {
		die("bütçe dosyası: %v", err)
	}
	var bf budgetFile
	if err := json.Unmarshal(raw, &bf); err != nil {
		die("bütçe JSON: %v", err)
	}
	if *base != "" {
		bf.BaseURL = *base
	}
	if *runs > 0 {
		bf.Runs = *runs
	}
	if bf.Runs <= 0 {
		bf.Runs = 5
	}
	if bf.Rules.TolerancePct <= 0 {
		bf.Rules.TolerancePct = 25
	}
	if bf.Rules.DatasetDriftWarnPct <= 0 {
		bf.Rules.DatasetDriftWarnPct = 20
	}
	jar, _ := cookiejar.New(nil)
	r := &runner{base: strings.TrimRight(bf.BaseURL, "/"), cli: &http.Client{Jar: jar, Timeout: *timeout},
		runs: bf.Runs, warmup: bf.Warmup, warm: *warm}
	if err := r.login(*email, *password); err != nil {
		die("giriş: %v", err)
	}
	ds, err := r.dataset()
	if err != nil {
		die("veri-seti parmak izi: %v", err)
	}
	if err := r.resolvePlaceholders(); err != nil {
		die("yer tutucu: %v", err)
	}

	var prev *perfcheck.Report
	if *compare != "" {
		pb, err := os.ReadFile(*compare)
		if err != nil {
			die("kıyas dosyası: %v", err)
		}
		prev = &perfcheck.Report{}
		if err := json.Unmarshal(pb, prev); err != nil {
			die("kıyas JSON: %v", err)
		}
	}
	rep := perfcheck.Report{Schema: 1, StartedAt: time.Now().UTC().Format(time.RFC3339), BaseURL: r.base, Runs: bf.Runs, Dataset: ds}
	driftWarn := false
	if prev != nil {
		if d := perfcheck.DatasetDriftPct(ds, prev.Dataset); d > bf.Rules.DatasetDriftWarnPct {
			driftWarn = true
			rep.Notes = append(rep.Notes, fmt.Sprintf("veri-seti sapması %%%.0f (24h span %d → %d) — kıyas güvenilmez", d, prev.Dataset.Spans24h, ds.Spans24h))
		}
	}
	prevIdx := perfcheck.IndexPrev(prev)
	for _, p := range bf.Points {
		res := r.measure(p)
		var pp *perfcheck.Result
		if pr, ok := prevIdx[p.Key()]; ok {
			pp = &pr
		}
		res = perfcheck.Evaluate(res, pp, bf.Rules, driftWarn)
		rep.Points = append(rep.Points, res)
		fmt.Println(perfcheck.Lines(perfcheck.Report{Points: []perfcheck.Result{res}})[0])
	}
	rep.Summary = perfcheck.Tally(rep.Points)

	if *out == "" {
		*out = filepath.Join("perf", "out", time.Now().UTC().Format("20060102-150405")+".json")
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		die("çıktı dizini: %v", err)
	}
	js, _ := json.MarshalIndent(rep, "", "  ")
	if err := os.WriteFile(*out, js, 0o644); err != nil {
		die("çıktı: %v", err)
	}
	for _, n := range rep.Notes {
		fmt.Println("not:", n)
	}
	fmt.Printf("özet: %d geçti · %d uyarı · %d başarısız · %d ölçülemedi → %s\n",
		rep.Summary.Pass, rep.Summary.Warn, rep.Summary.Fail, rep.Summary.Invalid, *out)
	if !rep.Summary.OK {
		os.Exit(1)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "perfcheck: "+f+"\n", a...)
	os.Exit(2)
}

func (r *runner) login(email, password string) error {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	resp, err := r.cli.Post(r.base+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// getJSON — küçük yardımcı: GET + JSON çöz.
func (r *runner) getJSON(path string, v any) error {
	resp, err := r.cli.Get(r.base + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// dataset — parmak izi: sürüm + servis listesi (varsayılan pencere) toplam
// span'ı. Aynı ölçümün iki koşusu farklı veriyle kıyaslanırsa Notes'a düşer.
func (r *runner) dataset() (perfcheck.Dataset, error) {
	var ds perfcheck.Dataset
	var ver struct {
		Version string `json:"version"`
	}
	if err := r.getJSON("/api/version", &ver); err != nil {
		return ds, err
	}
	ds.Version = ver.Version
	var svc struct {
		Services []struct {
			Name      string `json:"name"`
			SpanCount int64  `json:"spanCount"`
		} `json:"services"`
	}
	if err := r.getJSON("/api/services?limit=1000", &svc); err != nil {
		return ds, err
	}
	ds.Services = len(svc.Services)
	for _, s := range svc.Services {
		ds.Spans24h += s.SpanCount
	}
	sort.Slice(svc.Services, func(i, j int) bool { return svc.Services[i].SpanCount > svc.Services[j].SpanCount })
	if len(svc.Services) > 0 {
		r.service = svc.Services[0].Name
	}
	return ds, nil
}

// resolvePlaceholders — {traceIds}: son 1 saatin ilk 50 trace'i.
func (r *runner) resolvePlaceholders() error {
	from, to := windowNs("1h")
	var tr struct {
		Traces []struct {
			TraceID string `json:"traceId"`
		} `json:"traces"`
	}
	if err := r.getJSON(fmt.Sprintf("/api/traces?limit=50&offset=0&count=skip&from=%d&to=%d", from, to), &tr); err != nil {
		return err
	}
	ids := make([]string, 0, len(tr.Traces))
	for _, t := range tr.Traces {
		ids = append(ids, t.TraceID)
	}
	r.traceIDs = strings.Join(ids, ",")
	if r.service == "" || r.traceIDs == "" {
		return fmt.Errorf("servis/trace bulunamadı — fixture boş mu? (servis=%q, trace=%d)", r.service, len(ids))
	}
	return nil
}

func windowNs(w string) (int64, int64) {
	d, err := time.ParseDuration(w)
	if err != nil || d <= 0 {
		d = time.Hour
	}
	now := time.Now()
	return now.Add(-d).UnixNano(), now.UnixNano()
}

// dashboardBody — bundle gövdesi: dashboard'un spanmetric panelleri
// (Dashboard.tsx'in ürettiği şeklin aynısı: id/type/agg/field/groupBy/
// step/dsl), 1 dk adım.
func (r *runner) dashboardBody(id string, from, to int64) ([]byte, error) {
	var d struct {
		Panels []struct {
			ID     string          `json:"id"`
			Type   string          `json:"type"`
			Config json.RawMessage `json:"config"`
		} `json:"panels"`
	}
	if err := r.getJSON("/api/dashboards/"+url.PathEscape(id), &d); err != nil {
		return nil, err
	}
	type req struct {
		ID      string   `json:"id"`
		Type    string   `json:"type"`
		Agg     string   `json:"agg"`
		Field   string   `json:"field,omitempty"`
		GroupBy []string `json:"groupBy,omitempty"`
		Step    int      `json:"step"`
		DSL     string   `json:"dsl,omitempty"`
	}
	var reqs []req
	for _, p := range d.Panels {
		var cfg struct {
			Source  string `json:"source"`
			Agg     string `json:"agg"`
			Field   string `json:"field"`
			GroupBy string `json:"groupBy"`
			DSL     string `json:"dsl"`
			Span    *struct {
				Agg   string `json:"agg"`
				Field string `json:"field"`
				DSL   string `json:"dsl"`
			} `json:"span"`
		}
		_ = json.Unmarshal(p.Config, &cfg)
		var rq *req
		switch {
		case cfg.Source == "spanmetric" && cfg.Span != nil:
			rq = &req{ID: p.ID, Type: "spanMetric", Agg: cfg.Span.Agg, Field: cfg.Span.Field, DSL: cfg.Span.DSL}
		case p.Type == "spanmetric":
			rq = &req{ID: p.ID, Type: "spanMetric", Agg: cfg.Agg, Field: cfg.Field, DSL: cfg.DSL}
		}
		if rq == nil {
			continue
		}
		if rq.Agg == "" {
			rq.Agg = "count"
		}
		if cfg.GroupBy != "" {
			rq.GroupBy = []string{cfg.GroupBy}
		}
		rq.Step = 60
		reqs = append(reqs, *rq)
	}
	if len(reqs) == 0 {
		return nil, fmt.Errorf("dashboard %s: bundle'a girecek spanmetric panel yok", id)
	}
	return json.Marshal(map[string]any{"from": from, "to": to, "requests": reqs})
}

// measure — bir nokta: ısınma (atılır) + K soğuk + (opsiyonel) K sıcak.
//
// v0.10.117 — HER SOĞUK KOŞU FARKLI PENCERE: aynı SQL metni iki kez
// koşunca ikincisi 0 satır okuyordu (taban koşusunda P2 25 ms, P1 148 ms
// — gerçek okuma değil). `refresh=1` yalnız Coremetry cache'ini atlar;
// sunucu tarafında aynı metni yeniden hesaplamayan katman kalıyor. Pencere
// her koşuda 61 sn geriye kayar (uzunluk aynı, veri yoğunluğu aynı,
// SQL metni farklı) → her koşu gerçek bir okuma.
func (r *runner) measure(p perfcheck.Point) perfcheck.Result {
	res := perfcheck.Result{Name: p.Name, Scenario: p.Scenario, Budget: p.Budget}
	pathFor := func(shift int) (string, int64, int64) {
		from, to := windowNs(p.Window)
		d := int64(shift) * 61 * int64(time.Second)
		from, to = from-d, to-d
		return strings.NewReplacer(
			"{from}", fmt.Sprint(from), "{to}", fmt.Sprint(to),
			"{service}", url.QueryEscape(r.service), "{traceIds}", r.traceIDs,
		).Replace(p.Path), from, to
	}
	path, from, to := pathFor(0)
	method := p.Method
	if method == "" {
		method = http.MethodGet
	}
	var body []byte
	if p.BodyFromDashboard != "" {
		b, err := r.dashboardBody(p.BodyFromDashboard, from, to)
		if err != nil {
			res.Status, res.Reason = "invalid", err.Error()
			return res
		}
		body = b
	}
	coldPath := func(shift int) string {
		cp, _, _ := pathFor(shift)
		if p.Cold {
			sep := "?"
			if strings.Contains(cp, "?") {
				sep = "&"
			}
			cp += sep + "refresh=1"
		}
		return cp
	}
	for i := 0; i < r.warmup; i++ {
		_ = r.one(method, coldPath(100+i), body)
	}
	var cold []perfcheck.Sample
	var ttfb []float64
	for i := 0; i < r.runs; i++ {
		if body != nil && p.BodyFromDashboard != "" {
			// POST gövdesi de pencereyi taşır — her koşuda kaydır.
			_, f2, t2 := pathFor(i + 1)
			if b, err := r.dashboardBody(p.BodyFromDashboard, f2, t2); err == nil {
				body = b
			}
		}
		s := r.one(method, coldPath(i+1), body)
		cold = append(cold, s)
		ttfb = append(ttfb, s.TTFBMs)
		res.XCache = append(res.XCache, s.XCache)
		if s.Bytes > res.Bytes {
			res.Bytes = s.Bytes
		}
	}
	res.Samples = cold
	res.Cold = perfcheck.Summarize(ttfb)
	if st, why := perfcheck.ValidateCold(cold, p.Cold); st != "" {
		res.Status, res.Reason = st, why
		return res
	}
	if r.warm && p.Cold && method == http.MethodGet {
		var wt []float64
		for i := 0; i < r.runs; i++ {
			wt = append(wt, r.one(method, path, nil).TTFBMs)
		}
		ws := perfcheck.Summarize(wt)
		res.Warm = &ws
	}
	return res
}

// one — tek istek: TTFB (ilk yanıt baytı), durum, gövde boyutu, X-Cache.
func (r *runner) one(method, path string, body []byte) perfcheck.Sample {
	var s perfcheck.Sample
	var t0, tFirst time.Time
	trace := &httptrace.ClientTrace{
		GotFirstResponseByte: func() { tFirst = time.Now() },
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), method, r.base+path, rd)
	if err != nil {
		return s
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	t0 = time.Now()
	resp, err := r.cli.Do(req)
	if err != nil {
		s.Status = 0
		return s
	}
	defer resp.Body.Close()
	n, _ := io.Copy(io.Discard, resp.Body)
	if tFirst.IsZero() {
		tFirst = time.Now()
	}
	s.TTFBMs = float64(tFirst.Sub(t0).Microseconds()) / 1000
	s.Status = resp.StatusCode
	s.Bytes = n
	s.XCache = resp.Header.Get("X-Cache")
	return s
}
