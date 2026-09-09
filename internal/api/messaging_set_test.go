package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/vmetrics"
)

// messaging_set_test.go — v0.10.575: /api/messaging/clients ?set= soru seti.
//
// Sözleşme (topic detay sayfası üç yerde metrik gösteriyor; hepsi bir arada
// 12 VM range sorgusu demek — set bunu istenen bloğa daraltır):
//
//   set=topic (VARSAYILAN)  — bugünkü davranış: 5 soru, HER sorguda topic
//                             süzgeci, scope="topic". Boş set = topic.
//   set=chart               — 2 soru (üst grafik), yine topic kapsamlı.
//   set=clients             — 9 soru; metrikler `topic` label'ı TAŞIMAZ,
//                             sorguya topic süzgeci GİRMEZ, scope="services",
//                             Not bunu yazar (sessiz yanlış okuma yasak).
//
// Geçersiz set 400 (sessizce varsayılana düşmek yanlış paneli çizer). Set
// cache anahtarına girer — girmezse iki set aynı gövdeyi alır (v0.5.187).

func msgSetPlan(set string) messagingClientsPlan {
	from, to := kafkaFixtureWindow()
	return messagingClientsPlan{System: "kafka", Cluster: "(default)", Destination: "orders", Set: set, From: from, To: to, Mdp: 60}
}

func msgSetCallers() []chstore.MsgCallerService {
	return []chstore.MsgCallerService{
		{Service: "producer-a", Role: "producer"},
		{Service: "consumer-b", Role: "consumer"},
		{Service: "both-c", Role: "client"},
	}
}

// topicFilterValues — sorgudaki topic süzgecinin değerleri (yoksa nil).
func topicFilterValues(f chstore.MetricQueryFilter) []string {
	for _, fe := range f.Filters {
		if fe.Key == "topic" {
			return fe.Values
		}
	}
	return nil
}

func svcFilterValues(f chstore.MetricQueryFilter) []string {
	for _, fe := range f.Filters {
		if fe.Key == "service.name" {
			return fe.Values
		}
	}
	return nil
}

func TestBuildMessagingClientsSets(t *testing.T) {
	cases := []struct {
		name      string
		set       string
		want      []vmetrics.KafkaQuestion
		wantScope string
		wantTopic bool // sorgular topic süzgeci taşımalı mı
	}{
		{"varsayılan (boş)", "", vmetrics.KafkaTopicQuestions(), "topic", true},
		{"topic", msgSetTopic, vmetrics.KafkaTopicQuestions(), "topic", true},
		{"chart", msgSetChart, vmetrics.KafkaTopicChartQuestions(), "topic", true},
		{"clients", msgSetClients, vmetrics.KafkaClientHealthQuestions(), "services", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := &fakeEPSource{name: "vm", queryFn: func(chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
				return kafkaSeries(1), nil
			}}
			resp, err := buildMessagingClients(context.Background(), src, msgSetPlan(c.set), msgSetCallers())
			if err != nil {
				t.Fatal(err)
			}
			if resp.Scope != c.wantScope {
				t.Fatalf("scope=%q, bekl. %q", resp.Scope, c.wantScope)
			}
			if len(resp.Blocks) != len(c.want) || len(src.queries) != len(c.want) {
				t.Fatalf("soru sayısı: blocks=%d queries=%d bekl.=%d", len(resp.Blocks), len(src.queries), len(c.want))
			}
			for _, q := range c.want {
				b, ok := resp.Blocks[q.Key]
				if !ok {
					t.Fatalf("blok eksik: %s", q.Key)
				}
				if b.Metric != q.Metric || b.Label != q.TR {
					t.Errorf("%s: blok sorudan sapmış: %+v", q.Key, b)
				}
			}
			if !resp.Available {
				t.Error("her soru seri döndü, available true olmalı")
			}
			// Kapsam iddiasının kanıtı: topic süzgeci VAR mı / YOK mu.
			for _, f := range src.queries {
				got := topicFilterValues(f)
				switch {
				case c.wantTopic && (len(got) != 1 || got[0] != "orders"):
					t.Errorf("%s: topic süzgeci eksik: %+v", f.Name, f.Filters)
				case !c.wantTopic && got != nil:
					t.Errorf("%s: topic süzgeci GİTMEMELİYDİ (metrikte topic label'ı yok): %+v", f.Name, f.Filters)
				}
				// Kapsam her hâlde servis kümesi; üretici sorusu üreticilerle.
				m, ok := vmetrics.KafkaMetricByName(f.Name)
				if !ok {
					t.Fatalf("katalogda yok: %s", f.Name)
				}
				want := resp.Consumers
				if m.Side == "producer" {
					want = resp.Producers
				}
				if strings.Join(svcFilterValues(f), ",") != strings.Join(want, ",") {
					t.Errorf("%s: servis kapsamı %v, bekl. %v", f.Name, svcFilterValues(f), want)
				}
			}
			// Not yalnız clients setinde süzülemezlik uyarısını taşır.
			hasCaveat := strings.Contains(resp.Note, "SÜZÜLEMEZ")
			if hasCaveat != (c.set == msgSetClients) {
				t.Errorf("not uyarısı=%v set=%q: %q", hasCaveat, c.set, resp.Note)
			}
		})
	}
}

// Varsayılan set bugünkü davranış: sorular KafkaTopicQuestions, ne eksik ne
// fazla — set alanı eklendi diye topic paneli değişmedi.
func TestBuildMessagingClientsDefaultSetUnchanged(t *testing.T) {
	want := vmetrics.KafkaTopicQuestions()
	for _, set := range []string{"", msgSetTopic} {
		src := &fakeEPSource{name: "vm", queryFn: func(chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) { return nil, nil }}
		resp, err := buildMessagingClients(context.Background(), src, msgSetPlan(set), msgSetCallers())
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.Blocks) != len(want) {
			t.Fatalf("set=%q blok sayısı %d, bekl. %d", set, len(resp.Blocks), len(want))
		}
		for _, q := range want {
			if _, ok := resp.Blocks[q.Key]; !ok {
				t.Errorf("set=%q: %s bloğu kayıp", set, q.Key)
			}
		}
		if resp.Scope != "topic" || strings.Contains(resp.Note, "SÜZÜLEMEZ") {
			t.Errorf("set=%q varsayılanda kapsam/not değişmiş: scope=%q note=%q", set, resp.Scope, resp.Note)
		}
	}
}

// Caller yokken clients setinde de sorgu atılmaz; scope yine "services" ve
// not süzülemezliği söyler (FE başlığı buna göre yazılır).
func TestBuildMessagingClientsClientsNoCallers(t *testing.T) {
	src := &fakeEPSource{name: "vm", queryFn: func(chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
		t.Fatal("caller yokken sorgu atılmamalı")
		return nil, nil
	}}
	resp, err := buildMessagingClients(context.Background(), src, msgSetPlan(msgSetClients), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Scope != "services" || !strings.Contains(resp.Note, "SÜZÜLEMEZ") || !strings.Contains(resp.Note, "üretici/tüketici") {
		t.Fatalf("caller yok: %+v", resp)
	}
}

// Ayrıştırma — SAF; boş = topic, bilinmeyen 400 (handler bunu yayar).
func TestParseMessagingSet(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"", msgSetTopic, true},
		{"  ", msgSetTopic, true},
		{"topic", msgSetTopic, true},
		{" chart ", msgSetChart, true},
		{"clients", msgSetClients, true},
		{"Clients", "", false},
		{"all", "", false},
		{"topic,chart", "", false},
	} {
		got, ok := parseMessagingSet(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseMessagingSet(%q) = %q,%v — bekl. %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// Plan kurucusu gerçek istekle: set plana GİRER (kablolama parametreye
// kaçmasın), geçersiz set hata döner.
func TestMessagingClientsPlanFrom(t *testing.T) {
	base := "/api/messaging/clients?system=kafka&destination=orders&from=1&to=2"
	for _, c := range []struct {
		q    string
		want string
	}{
		{"", msgSetTopic},
		{"&set=topic", msgSetTopic},
		{"&set=chart", msgSetChart},
		{"&set=clients", msgSetClients},
	} {
		p, err := messagingClientsPlanFrom(httptest.NewRequest(http.MethodGet, base+c.q, nil))
		if err != nil {
			t.Fatalf("%q: %v", c.q, err)
		}
		if p.Set != c.want {
			t.Errorf("%q: plan.Set=%q bekl. %q", c.q, p.Set, c.want)
		}
		if p.Cluster != "(default)" || p.Destination != "orders" {
			t.Errorf("%q: plan alanları: %+v", c.q, p)
		}
	}
	if _, err := messagingClientsPlanFrom(httptest.NewRequest(http.MethodGet, base+"&set=all", nil)); err == nil || !strings.Contains(err.Error(), "set") {
		t.Fatalf("geçersiz set hata vermeli: %v", err)
	}
	if _, err := messagingClientsPlanFrom(httptest.NewRequest(http.MethodGet, "/api/messaging/clients?system=kafka", nil)); err == nil {
		t.Fatal("destination zorunlu")
	}
}

// Handler sınırı: geçersiz set 400 — store'a dokunmadan döner, &Server{} yeter
// (filters_reject_handler_test.go deseni). Kanıt: kod + gövde seti adlandırır.
func TestGetMessagingClientsRejectsUnknownSet(t *testing.T) {
	s := &Server{}
	for _, url := range []string{
		"/api/messaging/clients?system=kafka&destination=orders&set=all",
		"/api/messaging/clients?system=kafka&destination=orders&set=CLIENTS",
	} {
		rr := httptest.NewRecorder()
		s.getMessagingClients(rr, httptest.NewRequest(http.MethodGet, url, nil))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s → code=%d body=%s", url, rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "set") {
			t.Fatalf("%s → gövde parametreyi adlandırmıyor: %s", url, rr.Body.String())
		}
	}
}

// Cache anahtarı — set GİRMEZSE üç set aynı gövdeyi paylaşırdı (v0.5.187).
func TestMessagingClientsKeySeparatesSets(t *testing.T) {
	seen := map[string]string{}
	for _, set := range []string{msgSetTopic, msgSetChart, msgSetClients} {
		k := messagingClientsKey(msgSetPlan(set), "vm", "mx0")
		if prev, dup := seen[k]; dup {
			t.Fatalf("set=%q anahtarı set=%q ile aynı: %s", set, prev, k)
		}
		seen[k] = set
		if !strings.Contains(k, "set="+set) {
			t.Errorf("anahtar seti taşımıyor: %s", k)
		}
		if k != messagingClientsKey(msgSetPlan(set), "vm", "mx0") {
			t.Errorf("set=%q anahtarı kararsız", set)
		}
	}
}

// Ölçek: set=clients açılışta koşmaz; chart seti VM'e giden range sorgusu
// sayısını topic setinin altında tutar (maliyet disiplini iddiası).
func TestMessagingSetQuestionCounts(t *testing.T) {
	chart, topic, clients := len(messagingSetQuestions(msgSetChart)), len(messagingSetQuestions(msgSetTopic)), len(messagingSetQuestions(msgSetClients))
	if chart >= topic {
		t.Fatalf("chart seti dar olmalı: chart=%d topic=%d", chart, topic)
	}
	if chart+topic+clients <= topic {
		t.Fatalf("setler ayrışmamış: %d/%d/%d", chart, topic, clients)
	}
	if len(messagingSetQuestions("")) != topic || len(messagingSetQuestions("bilinmeyen")) != topic {
		t.Fatal("bilinmeyen/boş set topic setine düşer (handler zaten 400 verir)")
	}
	if messagingSetScope(msgSetClients) != "services" || messagingSetScope(msgSetTopic) != "topic" || messagingSetScope(msgSetChart) != "topic" {
		t.Fatal("scope eşlemesi")
	}
}
