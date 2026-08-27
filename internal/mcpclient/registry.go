package mcpclient

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// registry.go — sunucu başına tool kataloğu önbelleği.
//
// api katmanının tek girişi: Tools (önekli katalog) + Call (önekli ada
// çağrı). Katalog sunucu başına TTL'li tutulur; sunucunun
// tools/list_changed bildirimi TTL'i beklemeden bayatlatır.
//
// ── ÖNEK: ÇAKIŞMA YAPISAL OLARAK İMKÂNSIZ ───────────────────────────────
//
// Dış tool adı `<sunucu>__<tool>` olur. Yerli mcptools adları tekli
// snake_case (list_services) ve HİÇBİRİ "__" içermez; sunucu adı da
// sanitize edilirken alt çizgiden arındırılır. Yani önekli bir ad yerli
// bir adla ya da başka sunucununkiyle çakışamaz — ad-alanı ayrımını
// politika değil biçim taşır.

// toolCacheTTL — katalog tazelik süresi. Bildirim gelmeyen (ya da POST
// akışına binemeyen) değişikliklerin emniyet ağı.
const toolCacheTTL = 5 * time.Minute

// prefixSep — sunucu/tool ayracı.
const prefixSep = "__"

// PrefixedTool — dış tool'un api katmanına görünen hâli.
type PrefixedTool struct {
	Server string  // sanitize edilmiş sunucu adı
	Name   string  // Server + "__" + Def.Name
	Def    ToolDef // sunucunun ilan ettiği tanım (şema aynen)
}

// EntryStatus — sunucu başına sağlık görünümü (dilim ②'nin Settings
// probu bunu gösterecek).
type EntryStatus struct {
	Server    string    `json:"server"`
	Tools     int       `json:"tools"`
	Truncated bool      `json:"truncated,omitempty"`
	FetchedAt time.Time `json:"fetchedAt,omitempty"`
	Err       string    `json:"err,omitempty"`
}

type regEntry struct {
	cfg     ServerConfig
	cl      *Client
	tr      Transport
	tools   []ToolDef
	trunc   bool
	fetched time.Time
	lastErr string
	stale   bool
}

// Registry — eşzamanlı-güvenli katalog. dial enjekte edilir; testler
// sahte Transport verir, prod DialTransport kullanır.
type Registry struct {
	mu      sync.Mutex
	entries map[string]*regEntry
	dial    func(ServerConfig) (Transport, error)
}

func NewRegistry(dial func(ServerConfig) (Transport, error)) *Registry {
	if dial == nil {
		dial = DialTransport
	}
	return &Registry{entries: map[string]*regEntry{}, dial: dial}
}

// DialTransport — cfg.Transport'a göre taşıma kurar.
func DialTransport(cfg ServerConfig) (Transport, error) {
	switch cfg.Transport {
	case "stdio":
		return newStdioTransport(cfg)
	case "http", "":
		if strings.TrimSpace(cfg.URL) == "" {
			return nil, fmt.Errorf("mcp sunucusu %q: url boş", cfg.Name)
		}
		return newHTTPTransport(cfg, nil), nil
	default:
		return nil, fmt.Errorf("mcp sunucusu %q: bilinmeyen taşıma %q", cfg.Name, cfg.Transport)
	}
}

// Configure — sunucu listesini değiştirir. Listeden düşen ya da
// yapılandırması DEĞİŞEN sunucunun taşıması kapatılır ve girdisi
// sıfırdan kurulur — eski bağlantıyla yeni ayarın sessizce karışması,
// ayar ekranının en pahalı yalanı olurdu.
func (r *Registry) Configure(servers []ServerConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	keep := map[string]bool{}
	for _, cfg := range servers {
		name := sanitizeServerName(cfg.Name)
		if name == "" || !cfg.Enabled {
			continue
		}
		keep[name] = true
		if old, ok := r.entries[name]; ok {
			if configEqual(old.cfg, cfg) {
				continue
			}
			closeEntry(old)
		}
		r.entries[name] = &regEntry{cfg: cfg}
	}
	for name, e := range r.entries {
		if !keep[name] {
			closeEntry(e)
			delete(r.entries, name)
		}
	}
}

func closeEntry(e *regEntry) {
	if e.tr != nil {
		_ = e.tr.Close()
	}
}

// configEqual — bağlantıyı etkileyen alanlar üzerinden eşitlik.
// Allow/Deny listeleri bağlantıyı DEĞİŞTİRMEZ (süzgeç dilim ③'te çağrı
// yerinde uygulanır) — onlar için taşımayı yeniden kurmak gereksiz kopuş.
func configEqual(a, b ServerConfig) bool {
	return a.Transport == b.Transport && a.URL == b.URL && a.Token == b.Token &&
		a.Command == b.Command && strings.Join(a.Args, "\x00") == strings.Join(b.Args, "\x00") &&
		a.InsecureSkipVerify == b.InsecureSkipVerify
}

// Tools — tüm etkin sunucuların önekli kataloğu. Sunuculardan biri
// düşse de kalanların kataloğu döner; düşenin hatası Status'ta görünür
// (soft-fail — GetSystemStats panel deseni).
func (r *Registry) Tools(ctx context.Context) []PrefixedTool {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []PrefixedTool
	for _, name := range r.sortedNames() {
		e := r.entries[name]
		r.refreshLocked(ctx, name, e)
		for _, td := range e.tools {
			out = append(out, PrefixedTool{Server: name, Name: name + prefixSep + td.Name, Def: td})
		}
	}
	return out
}

// Call — önekli ada çağrı. Ad çözülemezse hata: sessizce yanlış
// sunucuya gitmek, hiç gitmemekten kötü.
func (r *Registry) Call(ctx context.Context, prefixed string, args []byte) (string, bool, error) {
	server, tool, ok := SplitPrefixed(prefixed)
	if !ok {
		return "", false, fmt.Errorf("mcp tool adı çözülemedi: %q", prefixed)
	}
	r.mu.Lock()
	e := r.entries[server]
	var cl *Client
	if e != nil {
		r.ensureClientLocked(e)
		cl = e.cl
	}
	r.mu.Unlock()
	if e == nil || cl == nil {
		return "", false, fmt.Errorf("mcp sunucusu %q kayıtlı/erişilebilir değil", server)
	}
	return cl.CallTool(ctx, tool, args)
}

// Status — sunucu başına sağlık; Settings probu için.
func (r *Registry) Status() []EntryStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]EntryStatus, 0, len(r.entries))
	for _, name := range r.sortedNames() {
		e := r.entries[name]
		out = append(out, EntryStatus{
			Server: name, Tools: len(e.tools), Truncated: e.trunc,
			FetchedAt: e.fetched, Err: e.lastErr,
		})
	}
	return out
}

// refreshLocked — gerekiyorsa kataloğu tazeler (kilit çağıranda).
//
// Bildirim kanalı önce boşaltılır: list_changed görüldüyse TTL dolmasa
// da tazelenir. Hata katalogu SİLMEZ — bayat katalog, hiç katalogdan
// iyidir (arama kutusu ekranı boşaltmaz; hata Status'ta görünür).
func (r *Registry) refreshLocked(ctx context.Context, name string, e *regEntry) {
	r.ensureClientLocked(e)
	if e.cl == nil {
		return
	}
	if e.tr != nil {
	drain:
		for {
			select {
			case m := <-e.tr.Notifications():
				if m == notifyListChanged {
					e.stale = true
				}
			default:
				break drain
			}
		}
	}
	if !e.stale && !e.fetched.IsZero() && time.Since(e.fetched) < toolCacheTTL {
		return
	}
	tools, trunc, err := e.cl.ListTools(ctx)
	if err != nil {
		e.lastErr = err.Error()
		return
	}
	e.tools, e.trunc, e.fetched, e.stale, e.lastErr = tools, trunc, time.Now(), false, ""
}

// ensureClientLocked — taşıma + istemci tembel kurulur; kuruluş hatası
// girdiye yazılır ve sunucu atlanır (soft-fail).
func (r *Registry) ensureClientLocked(e *regEntry) {
	if e.cl != nil {
		return
	}
	tr, err := r.dial(e.cfg)
	if err != nil {
		e.lastErr = err.Error()
		return
	}
	e.tr, e.cl = tr, NewClient(tr)
}

func (r *Registry) sortedNames() []string {
	names := make([]string, 0, len(r.entries))
	for n := range r.entries {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Close — tüm taşımaları kapatır (stdio süreçleri iner).
func (r *Registry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, e := range r.entries {
		closeEntry(e)
		delete(r.entries, name)
	}
}

// SplitPrefixed — "<sunucu>__<tool>" → (sunucu, tool). Tool adının
// KENDİSİ "__" içerebilir (spec yasaklamıyor); ilk ayraçtan bölünür —
// sunucu adı sanitize edildiği için hiç "__" içermez, yani ilk ayraç
// her zaman sınırın kendisidir.
func SplitPrefixed(name string) (server, tool string, ok bool) {
	i := strings.Index(name, prefixSep)
	if i <= 0 || i+len(prefixSep) >= len(name) {
		return "", "", false
	}
	return name[:i], name[i+len(prefixSep):], true
}

// SanitizedName — sanitizeServerName'in dışa açık yüzü: api katmanının
// token-merge eşlemesi Registry'yle AYNI kimliği kullanmak zorunda
// (iki ayrı yazım, aynı sunucuya iki kimlik demekti).
func SanitizedName(name string) string { return sanitizeServerName(name) }

// sanitizeServerName — ayar adını önek-güvenli biçime indirger:
// küçük harf, [a-z0-9-] dışı her şey '-'. Alt çizgi de '-' olur —
// önekteki "__" ayracının tekliği buna dayanıyor.
func sanitizeServerName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var sb strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteRune('-')
		}
	}
	return strings.Trim(sb.String(), "-")
}
