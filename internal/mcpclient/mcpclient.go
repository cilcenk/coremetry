// Package mcpclient — DIŞ MCP sunucularının istemcisi (v0.10.86,
// MCP istemci planı dilim ①: protokol + taşıma + kayıt defteri).
//
// internal/mcp Coremetry'nin KENDİ MCP sunucusudur (gelen yön); bu paket
// tam tersini konuşur: operatörün izin listesine yazdığı dış sunuculara
// bağlanır, tool kataloglarını çeker ve çağrı yapar. İki paket aynı
// JSON-RPC zarfını taşır ama sunucunun initialize/tools şekilleri
// unexported olduğu için istemci şekilleri burada yeniden tanımlı —
// bilinçli kopya: iki yön ayrı evrilir, paylaşım iki tarafı birbirine
// zincirlerdi.
//
// Dilim sınırı: burada AĞ ve PROTOKOL var. Ayar kalıcılığı (dilim ②),
// sohbet köprüsü + rol/bütçe/audit (dilim ③) ve selfobs span'leri
// (dilim ④) sonraki sürümlerde. api katmanına tek giriş Registry'dir.
//
// Güvenlik duruşu: sunucu listesi YALNIZ operatör ayarından gelir
// (izin listesi); bu paket kendi başına hiçbir adrese bağlanmaz.
package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/cilcenk/coremetry/internal/selfobs"
)

// protocolVersion — konuşulan MCP sürümü; internal/mcp sunucusuyla aynı.
const protocolVersion = "2024-11-05"

// callTimeout — TEK tool çağrısının istemci tavanı. Sohbet döngüsünün
// runChatTool 20s tavanından BİLİNÇLİ kısa: dış sunucu ağ gecikmesi o
// bütçeyi yerken pay kalsın; hangi tavanın dolduğu hata metninden
// okunabilsin.
const callTimeout = 15 * time.Second

// listToolsPageCap / listToolsCap — katalog sayfalama tavanları.
// Kaçak bir sunucu sonsuz nextCursor döndürebilir; tavana çarpınca
// kalan sayfalar ATILIR ve bu sessiz kalmaz (Client.ListTools notu).
const (
	listToolsPageCap = 10
	listToolsCap     = 200
)

// ServerConfig — tek dış sunucunun yapılandırması. Kalıcılık dilim ②'de
// devops Settings şablonuyla gelir; şekil şimdiden burada ki dilimler
// arasında tip kaymasın.
type ServerConfig struct {
	Name      string   `json:"name"`
	Transport string   `json:"transport"` // "http" | "stdio"
	URL       string   `json:"url,omitempty"`
	Token     string   `json:"token,omitempty"` // http: Authorization Bearer
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
	Enabled   bool     `json:"enabled"`
	// AllowTools / DenyTools — tool bazlı süzgeç; UYGULAMASI dilim ③'te
	// (rol kapısıyla aynı yerde). Şekil burada ki ayar blob'u tek sefer
	// tanımlansın.
	AllowTools []string `json:"allowTools,omitempty"`
	DenyTools  []string `json:"denyTools,omitempty"`
	// InsecureSkipVerify — banka içi self-signed uçlar için; devops
	// istemcisiyle aynı bayrak.
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
}

// Transport — iki taşımanın (stdio, streamable HTTP) ortak yüzü.
//
// Call bir istek/yanıt turu; Notify tek yönlü bildirim (initialized).
// Notifications, SUNUCUDAN gelen bildirimlerin metot adlarını taşır —
// kanal dolarsa bildirim DÜŞER (tamponlu, bloklamaz): bildirimler
// yalnız önbellek tazeleme ipucudur, TTL zaten emniyet ağıdır.
type Transport interface {
	Call(ctx context.Context, method string, params, result any) error
	Notify(ctx context.Context, method string, params any) error
	Notifications() <-chan string
	Close() error
}

// notifyListChanged — kataloğu bayatlatan bildirim.
const notifyListChanged = "notifications/tools/list_changed"

// ── JSON-RPC zarfı (istemci yönü) ───────────────────────────────────────

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("mcp sunucu hatası %d: %s", e.Code, e.Message)
}

// rpcEnvelope — istek + yanıt + bildirim tek şekilde okunur; alanların
// hangisinin dolu olduğu türü söyler (ID'siz method = bildirim).
type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// ── İstemci ─────────────────────────────────────────────────────────────

// ToolDef — dış sunucunun ilan ettiği tool. InputSchema aynen taşınır:
// provider.ToolSpec.InputSchema ile 1:1 (köprü dilim ③).
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Client — tek sunucuya bağlı ince istemci. Taşıma enjekte edilir;
// testler sahte Transport ile protokol katmanını ağsız sürer.
type Client struct {
	tr          Transport
	initialized bool
	// server — span attribute'u için sunucu adı; boş olabilir (test).
	server string
}

func NewClient(tr Transport) *Client { return &Client{tr: tr} }

// NewNamedClient — span'lere sunucu adını taşıyan kurucu (dilim ④).
// Registry ve Test probu bunu kullanır; adsız NewClient testlerde kalır.
func NewNamedClient(server string, tr Transport) *Client {
	return &Client{tr: tr, server: server}
}

// startSpan — giden MCP çağrısının selfobs span'i (v0.10.89, dilim ④).
//
// Depoda BUGÜNE DEK hiçbir dış HTTP istemcisi span'lenmiyordu (keşif
// bulgusu: otelhttp.NewTransport 0 kullanım); MCP çağrısı modelin cevap
// süresine DOĞRUDAN girer, yani "sohbet niye 12 sn" sorusunun cevabı
// tam buradadır. selfobs kapalıyken Tracer() noop döner — bedel sıfır
// (traced_conn deseni). Hata mesajı SafeAttr'dan geçer.
func (c *Client) startSpan(ctx context.Context, op string, extra ...attribute.KeyValue) (context.Context, func(error)) {
	ctx, span := selfobs.Tracer().Start(ctx, op)
	span.SetAttributes(append([]attribute.KeyValue{
		attribute.String("mcp.server", c.server),
	}, extra...)...)
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(fmt.Errorf("%s", selfobs.SafeAttr(err.Error())))
			span.SetStatus(codes.Error, selfobs.SafeAttr(err.Error()))
		}
		span.End()
	}
}

// Initialize — el sıkışma + initialized bildirimi. İkinci çağrı no-op:
// Registry tembel başlatır ve aynı istemciyi yeniden kullanır.
func (c *Client) Initialize(ctx context.Context) (err error) {
	if c.initialized {
		return nil
	}
	ctx, end := c.startSpan(ctx, "mcpclient.initialize")
	defer func() { end(err) }()
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	params := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "coremetry", "version": "1"},
	}
	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := c.tr.Call(ctx, "initialize", params, &res); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	// Sürüm uyuşmazlığı KAPI değil, not: MCP sürümleri geriye uyumlu
	// evriliyor ve sert kapı her yeni sunucu sürümünde operatörü
	// kilitlemek olurdu. Uyumsuzluk gerçek bir kırılım üretirse hata
	// zaten tools/list'te görünür.
	if err := c.tr.Notify(ctx, "notifications/initialized", map[string]any{}); err != nil {
		return fmt.Errorf("initialized bildirimi: %w", err)
	}
	c.initialized = true
	return nil
}

// ListTools — katalog; nextCursor sayfalarını tavana kadar toplar.
// Tavana çarpıldıysa truncated=true döner — sessiz kesme yok.
func (c *Client) ListTools(ctx context.Context) (tools []ToolDef, truncated bool, err error) {
	if err := c.Initialize(ctx); err != nil {
		return nil, false, err
	}
	ctx, end := c.startSpan(ctx, "mcpclient.list_tools")
	defer func() { end(err) }()
	cursor := ""
	for page := 0; page < listToolsPageCap; page++ {
		cctx, cancel := context.WithTimeout(ctx, callTimeout)
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var res struct {
			Tools      []ToolDef `json:"tools"`
			NextCursor string    `json:"nextCursor"`
		}
		callErr := c.tr.Call(cctx, "tools/list", params, &res)
		cancel()
		if callErr != nil {
			return nil, false, fmt.Errorf("tools/list: %w", callErr)
		}
		tools = append(tools, res.Tools...)
		if len(tools) >= listToolsCap {
			return tools[:listToolsCap], true, nil
		}
		if res.NextCursor == "" {
			return tools, false, nil
		}
		cursor = res.NextCursor
	}
	return tools, true, nil
}

// CallTool — tek çağrı. Dönen metin content[].text parçalarının
// birleşimidir; metin-dışı parça atlanır ve ATLANDIĞI SÖYLENİR (model
// eksik kanıtı tam sanmasın). isError sunucunun kendi bayrağıdır —
// taşıma hatası değil, tool'un "başarısız sonuç" sözleşmesi; ikisini
// ayırmak dilim ③'ün ToolErrorJSON köprüsüne lazım.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (text string, isError bool, err error) {
	if err := c.Initialize(ctx); err != nil {
		return "", false, err
	}
	ctx, end := c.startSpan(ctx, "mcpclient.call",
		attribute.String("mcp.tool", name))
	defer func() { end(err) }()
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	params := map[string]any{"name": name}
	if len(args) > 0 {
		params["arguments"] = json.RawMessage(args)
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := c.tr.Call(ctx, "tools/call", params, &res); err != nil {
		return "", false, err
	}
	var sb strings.Builder
	skipped := 0
	for _, part := range res.Content {
		if part.Type == "text" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(part.Text)
			continue
		}
		skipped++
	}
	if skipped > 0 {
		fmt.Fprintf(&sb, "\n[%d metin-dışı içerik parçası atlandı]", skipped)
	}
	return sb.String(), res.IsError, nil
}

// Close — taşımayı kapatır (stdio'da süreci indirir).
func (c *Client) Close() error { return c.tr.Close() }
