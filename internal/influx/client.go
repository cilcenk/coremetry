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

// QueryAPIFor — kaynağın istemcisi; tokenRef BURADA çözülür (her
// çağrıda: rotasyon anında etkili, saklanan değer yok).
func (s *Service) QueryAPIFor(src SourceConfig) (QueryAPI, error) {
	tok, err := resolveTokenRef(src.TokenRef, s.getenv, s.readFile)
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
