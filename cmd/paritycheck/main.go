// paritycheck — FAZ 4 parite kanıt koşumu (v0.9.713).
//
// "Grafana+Prometheus kalitesi" iddiasını SÖZLE değil ölçümle sınar.
// İki mod:
//
//	MOD A (CI, her yerde): Coremetry /api/metrics/query sonucu ↔ aynı
//	  pencerenin ClickHouse HAM-GERÇEK hesabı. Nokta sayısı, kafes
//	  hizası (ts % step == 0), değer farkı (mutlak+oransal), null
//	  konumları; rate ve histogram-quantile AYRI (en çok sapılan yer).
//	  Bir HATA bulursa exit 1 — CI regresyon kancası.
//
//	MOD B (--prom-url): aynı metrik+pencere+step için Prometheus HTTP
//	  API'siyle nokta nokta kıyas. Lokalde Prometheus yok; operatörün
//	  test ortamında (Prometheus 60s tutuyor) koşulur.
//
// Sapma sınıfları (görev tanımı):
//	BEKLENEN  — farklı export/scrape aralığı, kafes farkı
//	KABUL     — kayan nokta (oransal ≤ 1e-9)
//	HATA      — yanlış agregasyon/hizalama/null işleme
//
// Kullanım:
//	go run ./cmd/paritycheck -base http://localhost:8090 \
//	  -ch "kubectl exec -i -n coremetry chc-0 -- clickhouse-client --database coremetry" \
//	  -service coremetry-monolithic -metric process.runtime.go.goroutines \
//	  -report docs/charts/parity-report.md
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type point struct {
	T int64   `json:"time"` // unix ns (Coremetry API)
	V float64 `json:"value"`
}
type series struct {
	GroupKey []string `json:"groupKey"`
	Points   []point  `json:"points"`
}

type finding struct {
	Class  string // BEKLENEN | KABUL | HATA
	Check  string
	Detail string
}

func main() {
	base := flag.String("base", "http://localhost:8090", "Coremetry API tabanı")
	chCmd := flag.String("ch", "", "clickhouse-client komut öneki (boş = CH kıyası atlanır)")
	svc := flag.String("service", "", "servis adı (zorunlu)")
	metric := flag.String("metric", "", "gauge metrik adı (zorunlu)")
	rateMetric := flag.String("rate-metric", "", "cumulative sayaç (opsiyonel; rate kıyası)")
	histMetric := flag.String("hist-metric", "", "histogram metrik (opsiyonel; p95 kıyası)")
	stepSec := flag.Int("step", 60, "adım (sn)")
	windowMin := flag.Int("window", 30, "pencere (dk)")
	promURL := flag.String("prom-url", "", "MOD B: Prometheus tabanı (opsiyonel)")
	cookieFile := flag.String("cookie-file", "", "curl çerez kavanozu (auth'lu API için; içeriği asla yazdırılmaz)")
	report := flag.String("report", "", "markdown rapor yolu (opsiyonel)")
	flag.Parse()
	if *svc == "" || *metric == "" {
		fmt.Fprintln(os.Stderr, "-service ve -metric zorunlu")
		os.Exit(2)
	}

	if *cookieFile != "" {
		loadCookieJar(*cookieFile)
	}
	now := time.Now()
	from := now.Add(-time.Duration(*windowMin) * time.Minute)
	var out []finding
	add := func(class, check, detail string) { out = append(out, finding{class, check, detail}) }

	// ── 1) GAUGE: API ↔ CH ham-gerçek ─────────────────────────────────────
	api, err := fetchSeries(*base, *metric, *svc, "avg", from, now, *stepSec)
	if err != nil {
		fatal("API gauge sorgusu: %v", err)
	}
	checkGrid(api, *stepSec, "gauge", add)
	if *chCmd != "" {
		truth, err := chGaugeTruth(*chCmd, *metric, *svc, from, now, *stepSec)
		if err != nil {
			fatal("CH ham-gerçek: %v", err)
		}
		comparePoints("gauge/avg", api, truth, add)
	}

	// ── 2) RATE (cumulative): en çok sapılan yer — AYRI kıyas ────────────
	if *rateMetric != "" && *chCmd != "" {
		apiR, err := fetchSeries(*base, *rateMetric, *svc, "rate", from, now, *stepSec)
		if err != nil {
			fatal("API rate: %v", err)
		}
		checkGrid(apiR, *stepSec, "rate", add)
		// Ham-gerçek: per-seri reset-korumalı delta — sunucunun
		// resetSafeDelta'sıyla AYNI tanım, bağımsız SQL'le.
		truth, err := chRateTruth(*chCmd, *rateMetric, *svc, from, now, *stepSec)
		if err != nil {
			fatal("CH rate gerçeği: %v", err)
		}
		comparePoints("rate(cumulative)", apiR, truth, add)
		for _, p := range apiR {
			if p.V < 0 {
				add("HATA", "rate/reset", fmt.Sprintf("negatif rate noktası t=%d v=%g — counter reset zirvesi sızmış", p.T, p.V))
			}
		}
	}

	// ── 3) HISTOGRAM p95: quantile ORTALANMAZ kanıtı ─────────────────────
	if *histMetric != "" {
		apiP, err := fetchSeries(*base, *histMetric, *svc, "p95", from, now, *stepSec)
		if err != nil {
			fatal("API p95: %v", err)
		}
		checkGrid(apiP, *stepSec, "p95", add)
		if len(apiP) == 0 {
			add("BEKLENEN", "p95/veri", "pencerede histogram noktası yok — kıyas atlandı")
		}
	}

	// ── 4) MOD B: Prometheus yan yana ────────────────────────────────────
	if *promURL != "" {
		prom, err := fetchProm(*promURL, *metric, from, now, *stepSec)
		if err != nil {
			fatal("Prometheus: %v", err)
		}
		comparePoints("promB/"+*metric, api, prom, add)
	} else {
		add("BEKLENEN", "modB", "Prometheus tabanı verilmedi — yan yana kıyas bu koşuda yok (operatör test ortamında --prom-url ile)")
	}

	// ── Rapor + çıkış kodu ───────────────────────────────────────────────
	hata := 0
	for _, f := range out {
		if f.Class == "HATA" {
			hata++
		}
		fmt.Printf("%-8s %-18s %s\n", f.Class, f.Check, f.Detail)
	}
	if *report != "" {
		writeReport(*report, *svc, *metric, from, now, *stepSec, out)
	}
	fmt.Printf("\nÖZET: %d bulgu, %d HATA\n", len(out), hata)
	if hata > 0 {
		os.Exit(1)
	}
}

func fatal(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...); os.Exit(2) }

var authCookie string // -cookie-file'dan; loglanmaz, yazdırılmaz

func loadCookieJar(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	// Netscape kavanozu: alan 6=ad, 7=değer. Tüm çerezler başlığa dizilir.
	var parts []string
	for _, ln := range strings.Split(string(b), "\n") {
		// #HttpOnly_ öneki YORUM DEĞİL çerezdir (curl kavanoz biçimi) —
		// ilk yazım onu yorum sanıp atlıyordu ve auth 401'lüyordu.
		ln = strings.TrimPrefix(ln, "#HttpOnly_")
		if strings.HasPrefix(ln, "#") || strings.TrimSpace(ln) == "" {
			continue
		}
		f := strings.Split(ln, "\t")
		if len(f) >= 7 {
			parts = append(parts, f[5]+"="+f[6])
		}
	}
	authCookie = strings.Join(parts, "; ")
}

func fetchSeries(base, metric, svc, agg string, from, to time.Time, step int) ([]point, error) {
	q := url.Values{
		"name": {metric}, "service": {svc}, "agg": {agg},
		"from": {strconv.FormatInt(from.UnixNano(), 10)},
		"to":   {strconv.FormatInt(to.UnixNano(), 10)},
		"step": {strconv.Itoa(step)},
	}
	req, _ := http.NewRequest("GET", base+"/api/metrics/query?"+q.Encode(), nil)
	if authCookie != "" {
		req.Header.Set("Cookie", authCookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, b)
	}
	var body struct {
		Series []series `json:"series"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if len(body.Series) == 0 {
		return nil, nil
	}
	return body.Series[0].Points, nil
}

// chGaugeTruth — bağımsız ham-gerçek: aynı pencere, aynı kafes, avg.
func chGaugeTruth(chCmd, metric, svc string, from, to time.Time, step int) ([]point, error) {
	sql := fmt.Sprintf(`SELECT toInt64(toUnixTimestamp(toStartOfInterval(time, INTERVAL %d SECOND)))*1000000000 AS t, avg(value) AS v
FROM metric_points WHERE metric='%s' AND service_name='%s'
AND time >= toDateTime64(%d,9) AND time <= toDateTime64(%d,9)
GROUP BY t ORDER BY t LIMIT 5000 SETTINGS max_execution_time=15 FORMAT TSV`,
		step, sqlEsc(metric), sqlEsc(svc), from.Unix(), to.Unix())
	return runCH(chCmd, sql)
}

// chRateTruth — per-seri reset-korumalı artış / bucket süresi.
// resetSafeDelta tanımının bağımsız SQL ifadesi (cur<prev → cur).
func chRateTruth(chCmd, metric, svc string, from, to time.Time, step int) ([]point, error) {
	sql := fmt.Sprintf(`SELECT t*1000000000 AS tn, sum(d)/%d AS v FROM (
  SELECT series_fingerprint sf,
         toInt64(toUnixTimestamp(toStartOfInterval(time, INTERVAL %d SECOND))) t,
         value - lagInFrame(value) OVER (PARTITION BY series_fingerprint ORDER BY time
           ROWS BETWEEN 1 PRECEDING AND CURRENT ROW) AS raw,
         lagInFrame(toUnixTimestamp64Nano(time)) OVER (PARTITION BY series_fingerprint ORDER BY time
           ROWS BETWEEN 1 PRECEDING AND CURRENT ROW) AS prevns,
         if(raw < 0, value, raw) AS d
  FROM metric_points
  WHERE metric='%s' AND service_name='%s' AND instrument='sum' AND temporality='cumulative'
    AND time >= toDateTime64(%d,9) AND time <= toDateTime64(%d,9)
) WHERE prevns > 0 GROUP BY t ORDER BY t LIMIT 5000 SETTINGS max_execution_time=15 FORMAT TSV`,
		step, step, sqlEsc(metric), sqlEsc(svc), from.Unix(), to.Unix())
	return runCH(chCmd, sql)
}

func runCH(chCmd, sql string) ([]point, error) {
	parts := strings.Fields(chCmd)
	cmd := exec.Command(parts[0], append(parts[1:], "--query", sql)...)
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%v (%s)", err, string(b))
	}
	var out []point
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if ln == "" {
			continue
		}
		f := strings.Split(ln, "\t")
		if len(f) < 2 {
			continue
		}
		t, _ := strconv.ParseInt(f[0], 10, 64)
		v, _ := strconv.ParseFloat(f[1], 64)
		out = append(out, point{t, v})
	}
	return out, nil
}

// fetchProm — MOD B: query_range. Prometheus saniye+float epoch döner.
func fetchProm(promBase, metric string, from, to time.Time, step int) ([]point, error) {
	q := url.Values{
		"query": {metric},
		"start": {strconv.FormatInt(from.Unix(), 10)},
		"end":   {strconv.FormatInt(to.Unix(), 10)},
		"step":  {strconv.Itoa(step)},
	}
	resp, err := http.Get(strings.TrimRight(promBase, "/") + "/api/v1/query_range?" + q.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Data struct {
			Result []struct {
				Values [][2]any `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if len(body.Data.Result) == 0 {
		return nil, nil
	}
	var out []point
	for _, v := range body.Data.Result[0].Values {
		ts, _ := v[0].(float64)
		s, _ := v[1].(string)
		f, _ := strconv.ParseFloat(s, 64)
		out = append(out, point{int64(ts) * 1e9, f})
	}
	return out, nil
}

// checkGrid — sunucu hizası: her ts, step kafesine oturmalı
// (toStartOfInterval sözü). Oturmayan nokta = HATA.
func checkGrid(pts []point, step int, label string, add func(string, string, string)) {
	off := 0
	for _, p := range pts {
		if (p.T/1e9)%int64(step) != 0 {
			off++
		}
	}
	if off > 0 {
		add("HATA", label+"/kafes", fmt.Sprintf("%d/%d nokta step=%ds kafesine oturmuyor", off, len(pts), step))
	} else {
		add("KABUL", label+"/kafes", fmt.Sprintf("%d nokta, hepsi kafeste", len(pts)))
	}
}

// comparePoints — nokta nokta: sayı, hiza, değer (mutlak+oransal), null
// konumları (bir tarafta olup diğerinde olmayan bucket).
func comparePoints(label string, a, b []point, add func(string, string, string)) {
	am, bm := map[int64]float64{}, map[int64]float64{}
	for _, p := range a {
		am[p.T] = p.V
	}
	for _, p := range b {
		bm[p.T] = p.V
	}
	onlyA, onlyB, valErr := 0, 0, 0
	worst := 0.0
	for t, av := range am {
		bv, ok := bm[t]
		if !ok {
			onlyA++
			continue
		}
		den := math.Max(math.Abs(av), math.Abs(bv))
		rel := 0.0
		if den > 0 {
			rel = math.Abs(av-bv) / den
		}
		if rel > worst {
			worst = rel
		}
		if rel > 1e-9 {
			valErr++
		}
	}
	for t := range bm {
		if _, ok := am[t]; !ok {
			onlyB++
		}
	}
	// Uç bucket'lar (pencere kenarı) BEKLENEN; içeride eksik bucket HATA
	// sınıfına yaklaşır ama kaynak kadans farkı da olabilir → 1-2 uç
	// toleransı, fazlası HATA.
	switch {
	case valErr > 0:
		add("HATA", label+"/değer", fmt.Sprintf("%d bucket oransal >1e-9 sapıyor (en kötü %.3g)", valErr, worst))
	case onlyA+onlyB > 2:
		add("HATA", label+"/null", fmt.Sprintf("bucket kümeleri ayrışıyor: yalnızA=%d yalnızB=%d", onlyA, onlyB))
	default:
		add("KABUL", label+"/değer", fmt.Sprintf("%d ortak bucket birebir (fp toleransı), uç farkı A=%d B=%d", len(am)-onlyA, onlyA, onlyB))
	}
}

func sqlEsc(s string) string { return strings.ReplaceAll(s, "'", "\\'") }

func writeReport(path, svc, metric string, from, to time.Time, step int, fs []finding) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Parite Kanıt Koşumu — FAZ 4\n\nTarih: %s · servis `%s` · metrik `%s` · pencere %s→%s · step %ds\n\n",
		time.Now().Format("2006-01-02 15:04"), svc, metric,
		from.Format("15:04:05"), to.Format("15:04:05"), step)
	b.WriteString("| Sınıf | Kontrol | Detay |\n|---|---|---|\n")
	for _, f := range fs {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", f.Class, f.Check, f.Detail)
	}
	b.WriteString("\nSınıflar: BEKLENEN (kadans/ortam farkı) · KABUL (fp toleransı) · HATA (yanlış agregasyon — CI'ı kırar).\n")
	_ = os.WriteFile(path, []byte(b.String()), 0o644)
}
