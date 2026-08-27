package mcpclient

// registry_test.go — kayıt defterinin sözleşmeleri: TTL, bildirimle
// bayatlama, soft-fail, yapılandırma değişiminde taşıma kapanışı.
// Taşıma sahte: ağ yok, davranış saf sürülür.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeTr — betiklenebilir Transport.
type fakeTr struct {
	mu        sync.Mutex
	lists     int
	calls     []string
	tools     []ToolDef
	listErr   error
	notifCh   chan string
	closed    bool
	callText  string
	callIsErr bool
}

func newFakeTr(tools ...ToolDef) *fakeTr {
	return &fakeTr{tools: tools, notifCh: make(chan string, 4), callText: "ok"}
}

func (f *fakeTr) Call(_ context.Context, method string, params, result any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, method)
	switch method {
	case "initialize":
		return nil
	case "tools/list":
		if f.listErr != nil {
			return f.listErr
		}
		f.lists++
		*(result.(*struct {
			Tools      []ToolDef `json:"tools"`
			NextCursor string    `json:"nextCursor"`
		})) = struct {
			Tools      []ToolDef `json:"tools"`
			NextCursor string    `json:"nextCursor"`
		}{Tools: f.tools}
		return nil
	case "tools/call":
		*(result.(*struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		})) = struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		}{Content: []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{{Type: "text", Text: f.callText}}, IsError: f.callIsErr}
		return nil
	}
	return fmt.Errorf("beklenmeyen method %s", method)
}

func (f *fakeTr) Notify(context.Context, string, any) error { return nil }
func (f *fakeTr) Notifications() <-chan string              { return f.notifCh }
func (f *fakeTr) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func td(name string) ToolDef {
	return ToolDef{Name: name, Description: "d", InputSchema: map[string]any{"type": "object"}}
}

func regWith(t *testing.T, trs map[string]*fakeTr, cfgs ...ServerConfig) *Registry {
	t.Helper()
	r := NewRegistry(func(cfg ServerConfig) (Transport, error) {
		tr, ok := trs[cfg.Name]
		if !ok {
			return nil, errors.New("dial reddedildi: " + cfg.Name)
		}
		return tr, nil
	})
	r.Configure(cfgs)
	return r
}

func TestRegistryPrefixesAndCaches(t *testing.T) {
	tr := newFakeTr(td("search_kb"))
	r := regWith(t, map[string]*fakeTr{"kb": tr},
		ServerConfig{Name: "kb", Transport: "http", URL: "x", Enabled: true})

	got := r.Tools(context.Background())
	if len(got) != 1 || got[0].Name != "kb__search_kb" || got[0].Server != "kb" {
		t.Fatalf("önekli katalog yanlış: %+v", got)
	}
	// İkinci çağrı TTL içinde: sunucuya İKİNCİ tools/list gitmemeli.
	r.Tools(context.Background())
	if tr.lists != 1 {
		t.Errorf("TTL içinde %d kez listelendi, 1 olmalıydı", tr.lists)
	}
}

func TestRegistryNotificationInvalidates(t *testing.T) {
	tr := newFakeTr(td("a"))
	r := regWith(t, map[string]*fakeTr{"kb": tr},
		ServerConfig{Name: "kb", Transport: "http", URL: "x", Enabled: true})
	r.Tools(context.Background())
	tr.notifCh <- notifyListChanged
	r.Tools(context.Background())
	if tr.lists != 2 {
		t.Errorf("list_changed bildirimi kataloğu bayatlatmadı (list=%d)", tr.lists)
	}
	// Alakasız bildirim bayatlatmaz.
	tr.notifCh <- "notifications/progress"
	r.Tools(context.Background())
	if tr.lists != 2 {
		t.Errorf("alakasız bildirim tazeleme tetikledi (list=%d)", tr.lists)
	}
}

func TestRegistryTTLExpiryRefreshes(t *testing.T) {
	tr := newFakeTr(td("a"))
	r := regWith(t, map[string]*fakeTr{"kb": tr},
		ServerConfig{Name: "kb", Transport: "http", URL: "x", Enabled: true})
	r.Tools(context.Background())
	// TTL'i geçmişe çek — zaman enjeksiyonu yerine girdiyi yaşlandırmak
	// testin tek ihtiyacı.
	r.mu.Lock()
	r.entries["kb"].fetched = time.Now().Add(-toolCacheTTL - time.Second)
	r.mu.Unlock()
	r.Tools(context.Background())
	if tr.lists != 2 {
		t.Errorf("TTL dolunca tazelenmedi (list=%d)", tr.lists)
	}
}

func TestRegistrySoftFailKeepsOthersAndStaleCatalog(t *testing.T) {
	sag := newFakeTr(td("a"))
	bozuk := newFakeTr(td("b"))
	r := regWith(t, map[string]*fakeTr{"sag": sag, "bozuk": bozuk},
		ServerConfig{Name: "sag", Transport: "http", URL: "x", Enabled: true},
		ServerConfig{Name: "bozuk", Transport: "http", URL: "x", Enabled: true})
	if got := r.Tools(context.Background()); len(got) != 2 {
		t.Fatalf("iki sunucu iki tool vermeli: %+v", got)
	}
	// bozuk artık hata veriyor; TTL'i düşür ki tazeleme denensin.
	bozuk.listErr = errors.New("bağlantı koptu")
	r.mu.Lock()
	for _, e := range r.entries {
		e.fetched = time.Now().Add(-toolCacheTTL - time.Second)
	}
	r.mu.Unlock()
	got := r.Tools(context.Background())
	// Hata bayat kataloğu SİLMEZ: iki tool hâlâ listede, hata Status'ta.
	if len(got) != 2 {
		t.Fatalf("soft-fail bayat kataloğu düşürdü: %+v", got)
	}
	var bozukSt *EntryStatus
	for i, st := range r.Status() {
		if st.Server == "bozuk" {
			bozukSt = &r.Status()[i]
		}
	}
	if bozukSt == nil || bozukSt.Err == "" {
		t.Errorf("düşen sunucunun hatası Status'ta görünmüyor: %+v", r.Status())
	}
}

func TestRegistryConfigureClosesRemovedAndChanged(t *testing.T) {
	a1 := newFakeTr(td("a"))
	r := regWith(t, map[string]*fakeTr{"a": a1},
		ServerConfig{Name: "a", Transport: "http", URL: "x", Enabled: true})
	r.Tools(context.Background())

	// URL değişti → eski taşıma KAPANIR (eski bağlantı + yeni ayar karışmaz).
	a2 := newFakeTr(td("a"))
	r.dial = func(ServerConfig) (Transport, error) { return a2, nil }
	r.Configure([]ServerConfig{{Name: "a", Transport: "http", URL: "y", Enabled: true}})
	if !a1.closed {
		t.Error("değişen yapılandırmanın eski taşıması kapatılmadı")
	}
	// Listeden düştü → kapanır.
	r.Tools(context.Background())
	r.Configure(nil)
	if !a2.closed {
		t.Error("listeden düşen sunucunun taşıması kapatılmadı")
	}
	if got := r.Tools(context.Background()); len(got) != 0 {
		t.Errorf("boş yapılandırma katalog bırakıyor: %+v", got)
	}
}

func TestRegistryCallRoutesByPrefix(t *testing.T) {
	tr := newFakeTr(td("a"))
	tr.callText = "cevap"
	r := regWith(t, map[string]*fakeTr{"kb": tr},
		ServerConfig{Name: "kb", Transport: "http", URL: "x", Enabled: true})
	text, isErr, err := r.Call(context.Background(), "kb__a", []byte(`{}`))
	if err != nil || isErr || text != "cevap" {
		t.Fatalf("Call: text=%q isErr=%v err=%v", text, isErr, err)
	}
	if _, _, err := r.Call(context.Background(), "yok__a", nil); err == nil {
		t.Error("bilinmeyen sunucuya çağrı hata vermedi")
	}
	if _, _, err := r.Call(context.Background(), "list_services", nil); err == nil {
		t.Error("öneksiz (yerli) ada çağrı hata vermedi — yanlış yönlendirme riski")
	}
}

func TestRegistryDisabledAndUnnamedSkipped(t *testing.T) {
	tr := newFakeTr(td("a"))
	r := regWith(t, map[string]*fakeTr{"kb": tr},
		ServerConfig{Name: "kb", Transport: "http", URL: "x", Enabled: false},
		ServerConfig{Name: "  ", Transport: "http", URL: "x", Enabled: true})
	if got := r.Tools(context.Background()); len(got) != 0 {
		t.Errorf("devre dışı/adsız sunucu katalog verdi: %+v", got)
	}
}
