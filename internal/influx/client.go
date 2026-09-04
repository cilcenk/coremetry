package influx

// client.go — InfluxDB 2.x /api/v2/query istemcisi (v0.10.222, audit K2).
//
// net/http + annotated CSV: tempo/thanos/vmetrics ile aynı desen, go.mod
// değişmez. QueryAPI arayüzü tek seam — D2 poller ve D4 enrichment mock
// QueryAPI ile test edilir; influxdb-client-go'ya geçilecekse aynı
// arayüzün arkasına konur.
//
// İstek: POST {url}/api/v2/query?org=… · `Authorization: Token <t>` ·
// JSON {query, type:"flux", dialect:{annotations:[group,datatype,default],
// header:true}} · `Accept: application/csv`. 2xx dışı → hata; Influx'un
// JSON {code,message} zarfı mesaja taşınır. Gövde 16 MB ile sınırlı.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const maxBodyBytes = 16 << 20

// QueryAPI — tek sorgu çalıştırıcı.
type QueryAPI interface {
	Query(ctx context.Context, flux string) ([]Record, error)
}

type httpQueryAPI struct {
	cli   *http.Client
	base  string
	org   string
	token string
}

// NewHTTPQueryAPI — bir kaynağa bağlı istemci (token ÇÖZÜLMÜŞ değer).
func NewHTTPQueryAPI(cli *http.Client, baseURL, org, token string) QueryAPI {
	if cli == nil {
		cli = newHTTPClient(false)
	}
	return &httpQueryAPI{cli: cli, base: strings.TrimRight(baseURL, "/"), org: org, token: token}
}

func (q *httpQueryAPI) Query(ctx context.Context, flux string) ([]Record, error) {
	body, err := json.Marshal(map[string]any{
		"query": flux,
		"type":  "flux",
		"dialect": map[string]any{
			"annotations":   []string{"group", "datatype", "default"},
			"header":        true,
			"delimiter":     ",",
			"commentPrefix": "#",
		},
	})
	if err != nil {
		return nil, err
	}
	u := q.base + "/api/v2/query?org=" + url.QueryEscape(q.org)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Token "+q.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/csv")
	resp, err := q.cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("influx call: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("influx %d: %s", resp.StatusCode, influxErrMessage(data))
	}
	return ParseAnnotatedCSV(bytes.NewReader(data))
}

func influxErrMessage(b []byte) string {
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &e) == nil && e.Message != "" {
		if e.Code != "" {
			return e.Code + ": " + e.Message
		}
		return e.Message
	}
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	if s == "" {
		s = "boş gövde"
	}
	return s
}

// QueryAPIFor — kaynağın istemcisi; token BURADA seçilir/çözülür (her
// çağrıda: referans varsa rotasyon anında etkili).
func (s *Service) QueryAPIFor(src SourceConfig) (QueryAPI, error) {
	tok, err := s.tokenFor(src)
	if err != nil {
		return nil, err
	}
	return NewHTTPQueryAPI(s.clientFor(src.InsecureSkipVerify), src.URL, src.Org, tok), nil
}

// QueryProbe — test-connection'da sorgu başına sonuç.
type QueryProbe struct {
	Name      string              `json:"name"`
	Rows      int                 `json:"rows"`
	Columns   []string            `json:"columns"`
	Sample    []map[string]string `json:"sample,omitempty"`
	LatencyMs int64               `json:"latencyMs"`
	Error     string              `json:"error,omitempty"`
	// v0.10.335 — sıfır satırda İKİNCİ deneme: aynı sorgu 24 saatlik pencerede.
	// WideWindow doluysa deneme koştu; Hint operatöre gecikme mi / ad
	// uyuşmazlığı mı olduğunu söyler (prod olayı: işçi koşuyor, hata yok,
	// "0 satır → 0 nokta", kart sebebi ayırt edemiyordu).
	WideWindow string `json:"wideWindow,omitempty"`
	WideRows   int    `json:"wideRows,omitempty"`
	WideNewest string `json:"wideNewest,omitempty"` // en yeni _time (RFC3339, UTC); sum() gibi _time'sız sonuçta boş
	WideError  string `json:"wideError,omitempty"`
	Hint       string `json:"hint,omitempty"`
}

// wideProbeWindow — sıfır-satır ikinci denemesinin penceresi. 24 saat: gecikmeli
// yazan kaynağı (dakikalar) ve seyrek olayı (saatler) tek denemede yakalar;
// ad uyuşmazlığında 24 saat de boş döner — ayrım tam bu.
const wideProbeWindow = "24h"

// wideRangeRe — yalnız GÖRELİ başlangıç: `range(start: -2m`. Mutlak zaman ya
// da {{from}} placeholder'ı (kanıt sorgusu) eşleşmez → deneme atlanır.
var wideRangeRe = regexp.MustCompile(`range\(\s*start:\s*-\d+(?:ns|us|µs|ms|s|m|h|d|w|mo|y)\b`)

// widenRange — SAF: ilk göreli `range(start: -N<u>` ifadesini 24 saate
// genişletir; göreli başlangıç yoksa dokunmaz, false döner.
func widenRange(flux string) (string, bool) {
	loc := wideRangeRe.FindStringIndex(flux)
	if loc == nil {
		return flux, false
	}
	return flux[:loc[0]] + "range(start: -" + wideProbeWindow + flux[loc[1]:], true
}

// newestTime — kayıtların en büyük `_time`'ı (RFC3339 UTC); yoksa "".
func newestTime(recs []Record) string {
	var best time.Time
	for _, r := range recs {
		if t := recordTime(r); t.After(best) {
			best = t
		}
	}
	if best.IsZero() {
		return ""
	}
	return best.UTC().Format(time.RFC3339)
}

// emptyHint — SAF: sıfır-satır teşhisi. Geniş pencere de boşsa adlar,
// doluysa pencere/gecikme. Deneme koşmadıysa "" (söyleyecek kanıt yok).
func emptyHint(p QueryProbe) string {
	switch {
	case p.WideWindow == "":
		return ""
	case p.WideError != "":
		return "Geniş pencere denemesi başarısız: " + p.WideError
	case p.WideRows > 0:
		h := fmt.Sprintf("Sorgu ve adlar doğru: son %s içinde %d satır var ama poll penceresi boş. "+
			"Kaynak gecikmeli yazıyor ya da bu aralıkta olay yok — range(start: -N…) penceresini kaynağın yazım gecikmesinden büyük tut.",
			p.WideWindow, p.WideRows)
		if p.WideNewest != "" {
			h += " En yeni _time: " + p.WideNewest + "."
		}
		return h
	default:
		return fmt.Sprintf("Son %s içinde de satır yok: bucket ve token doğru ama _measurement / _field / tag adları "+
			"(büyük-küçük harf duyarlı) eşleşmiyor olabilir — aynı sorguyu Influx Data Explorer'da doğrula.", p.WideWindow)
	}
}

// wideProbe — sıfır satır dönen sorguyu 24 saatlik pencerede bir kez daha
// dener (limit 20). Sonuç p'ye yazılır; hata Test'i başarısız SAYMAZ
// (asıl sorgu çalıştı), yalnız Hint'e düşer.
func wideProbe(ctx context.Context, q QueryAPI, flux string, p *QueryProbe) {
	wide, ok := widenRange(flux)
	if !ok {
		return
	}
	p.WideWindow = wideProbeWindow
	recs, err := q.Query(ctx, strings.TrimRight(wide, " \t\r\n")+"\n  |> limit(n: 20)")
	if err != nil {
		p.WideError = err.Error()
	} else {
		p.WideRows = len(recs)
		p.WideNewest = newestTime(recs)
		if len(p.Columns) == 0 {
			p.Columns = columnsOf(recs)
		}
		if len(p.Sample) == 0 {
			p.Sample = sampleOf(recs, 3)
		}
	}
	p.Hint = emptyHint(*p)
}

// TestResult — POST /api/settings/influx/test cevabı. Bağlantı denemesinin
// başarısızlığı operatörün sorusuna BAŞARILI bir cevaptır: 200 + ok:false.
type TestResult struct {
	OK            bool         `json:"ok"`
	Error         string       `json:"error,omitempty"`
	TokenResolved bool         `json:"tokenResolved"`
	Queries       []QueryProbe `json:"queries"`
}

// Test — formdaki kaynağı KAYDETMEDEN dener: token çözümü, sonra her poll
// sorgusu `|> limit(n: 20)` ile. Örnek satırlar (≤3) operatöre tag
// adlarını ve değer biçimini gösterir (attrMap/groupBy'ı doğru yazsın).
func (s *Service) Test(ctx context.Context, src SourceConfig) TestResult {
	res := TestResult{Queries: []QueryProbe{}}
	q, err := s.QueryAPIFor(src)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.TokenResolved = true
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ok := true
	for _, qc := range src.Queries {
		flux := strings.TrimRight(qc.Flux, " \t\r\n") + "\n  |> limit(n: 20)"
		start := time.Now()
		recs, qerr := q.Query(ctx, flux)
		p := QueryProbe{Name: qc.Name, LatencyMs: time.Since(start).Milliseconds(), Columns: []string{}}
		if qerr != nil {
			p.Error = qerr.Error()
			ok = false
		} else {
			p.Rows = len(recs)
			p.Columns = columnsOf(recs)
			p.Sample = sampleOf(recs, 3)
			if len(recs) == 0 {
				wideProbe(ctx, q, qc.Flux, &p) // v0.10.335 — boşluğun sebebini ayırt et
			}
		}
		res.Queries = append(res.Queries, p)
	}
	res.OK = ok
	if !ok && res.Error == "" {
		res.Error = "bir ya da daha çok sorgu başarısız"
	}
	return res
}

func columnsOf(recs []Record) []string {
	set := map[string]bool{}
	for _, r := range recs {
		for k := range r.Values {
			set[k] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sampleOf(recs []Record, n int) []map[string]string {
	if n > len(recs) {
		n = len(recs)
	}
	out := make([]map[string]string, 0, n)
	for _, r := range recs[:n] {
		out = append(out, r.Values)
	}
	return out
}
