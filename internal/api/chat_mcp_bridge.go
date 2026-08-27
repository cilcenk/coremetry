package api

// chat_mcp_bridge.go — DIŞ MCP tool'larının sohbet döngüsüne köprüsü
// (v0.10.88, MCP istemci dilim ③).
//
// Dış tool mcp.Tool olarak sarılır ve yerli kataloğun yanına eklenir:
// toolsForRole süzgeci, ChatDescription diyeti ve döngünün bütçe/hata
// yolları (clampToolResultForModel, ToolErrorJSON) DEĞİŞMEDEN dış
// tool'lara da işler — keşif raporunun "Handler = uzak tools/call"
// tespiti tam buydu.
//
// ── GÜVENLİK DURUŞU ─────────────────────────────────────────────────────
//
//   - Sunucu listesi operatör izin listesi (dilim ②); model listeye üye
//     EKLEYEMEZ. Tool başına allow/deny burada uygulanır: deny KAZANIR,
//     boş allow = hepsi. Desen: tam ad ya da sondaki '*' ile önek.
//   - Her dış çağrı audit'e yazılır (mcp.call) — argümanlar KIRPILMIŞ
//     önizlemeyle: sorgu metni hassas olabilir, iz "kim neyi çağırdı"
//     sorusuna yeter, tam argüman gövdesine gerek yok.
//   - Ad çakışması yapısal olarak imkânsız: `<sunucu>__<tool>` öneki
//     (mcpclient.SplitPrefixed) ve sunucu adında "__" olamaz.
//
// ── TEKRAR MUHAFIZI ─────────────────────────────────────────────────────
//
// Aynı tool + aynı argümanların ikinci çağrısı YÜRÜTÜLMEZ (yerli dahil
// — dış'ta bedel ağ, yerlide CH sorgusu). v0.10.84 prompt'u zaten
// yasaklıyor; bu muhafız yasağı zorlamaya çevirir (devops
// huntWindows'un `tried` deseni). Anahtar KANONİK argümandan türer:
// model JSON anahtarlarını yeniden sıralarsa da aynı çağrıdır.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/mcp"
	"github.com/cilcenk/coremetry/internal/mcpclient"
)

// mcpToolAllowed — tool bu sunucunun süzgecinden geçer mi. SAF.
// deny KAZANIR; boş allow = hepsi. Desen: tam eşleşme ya da sondaki
// '*' ile önek ("search_*").
func mcpToolAllowed(allow, deny []string, tool string) bool {
	match := func(pat string) bool {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			return false
		}
		if strings.HasSuffix(pat, "*") {
			return strings.HasPrefix(tool, pat[:len(pat)-1])
		}
		return tool == pat
	}
	for _, p := range deny {
		if match(p) {
			return false
		}
	}
	if len(allow) == 0 {
		return true
	}
	for _, p := range allow {
		if match(p) {
			return true
		}
	}
	return false
}

// repeatCallKey — tekrar muhafızının anahtarı: ad + KANONİK argüman.
// Kanonikleştirme unmarshal→marshal (encoding/json map anahtarlarını
// sıralar); çözülemeyen argüman ham hâliyle anahtarlanır — muhafız
// hiçbir çağrıyı sessizce YOK sayamaz.
func repeatCallKey(name string, raw json.RawMessage) string {
	var v any
	if len(raw) > 0 && json.Unmarshal(raw, &v) == nil {
		if b, err := json.Marshal(v); err == nil {
			return name + "\x00" + string(b)
		}
	}
	return name + "\x00" + strings.TrimSpace(string(raw))
}

// markRepeatedCall — çağrıyı kaydeder; aynı (tool, kanonik-argüman)
// çiftinin İKİNCİ ve sonraki kopyalarında true döner. Döngüdeki tek
// çağrı yeri bu — karar saf ki tablo testi haritayı gerçek akışla
// sürebilsin (ChatWithTools somut tip, döngünün kendisi enjekte
// edilemiyor).
func markRepeatedCall(seen map[string]bool, name string, raw json.RawMessage) bool {
	key := repeatCallKey(name, raw)
	if seen[key] {
		return true
	}
	seen[key] = true
	return false
}

// repeatedCallJSON — muhafızın modele verdiği sonuç. mcp.ToolErrorJSON
// sözleşmesinin alanları (error/retryable/hint): model iki ayrı hata
// biçimi görmesin.
const repeatedCallJSON = `{"error":"repeated_call","retryable":false,` +
	`"hint":"Bu tool bu argümanlarla bu konuşmada zaten çağrıldı; sonucu yukarıdaki ` +
	`turda duruyor. Farklı argüman dene (aralığı genişlet, filtreyi değiştir) ya da ` +
	`eldeki veriyle cevap ver."}`

// externalChatTools — Registry kataloğunu mcp.Tool sarmalarına çevirir.
// SAF kurulum: çağrı ve audit closure'ları enjekte edilir, test ağsız
// sürer.
//
// MinRole viewer: yerli okuma tool'larının duruşu. Dış sunucunun kendi
// yazma yetkisi varsa onu operatörün allow/deny süzgeci ve sunucunun
// kendi kimliği sınırlar — rol merdiveni Coremetry verisinin merdiveni,
// dış sunucunun değil.
func externalChatTools(
	pts []mcpclient.PrefixedTool,
	rules func(server string) (allow, deny []string),
	call func(ctx context.Context, prefixed string, args []byte) (string, bool, error),
	onCall func(server, tool string, args json.RawMessage),
) []mcp.Tool {
	out := make([]mcp.Tool, 0, len(pts))
	for _, pt := range pts {
		allow, deny := rules(pt.Server)
		if !mcpToolAllowed(allow, deny, pt.Def.Name) {
			continue
		}
		pt := pt // closure kopyası
		out = append(out, mcp.Tool{
			Name: pt.Name,
			// "[dış: x]" öneki hem modele hem çip okuyan operatöre
			// kaynağın Coremetry telemetrisi OLMADIĞINI söyler — dış
			// içeriği yerli kanıtla karıştırmak künyenin yalanı olurdu.
			Description:      "[dış: " + pt.Server + "] " + pt.Def.Description,
			ShortDescription: "[dış: " + pt.Server + "] " + pt.Def.Description,
			InputSchema:      pt.Def.InputSchema,
			MinRole:          auth.RoleViewer,
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				onCall(pt.Server, pt.Def.Name, raw)
				text, isErr, err := call(ctx, pt.Name, raw)
				if err != nil {
					return nil, err // döngü ToolErrorJSON'a çevirir
				}
				res := map[string]any{"server": pt.Server, "content": text}
				if isErr {
					// Sunucunun KENDİ başarısızlık bayrağı — taşıma
					// hatası değil; model içeriği görmeli (hata metni
					// çoğu zaman cevabın kendisidir).
					res["is_error"] = true
				}
				return res, nil
			},
		})
	}
	return out
}

// mcpCallAuditDetails — dış çağrının sırsız izi: tool adı + KIRPILMIŞ
// argüman önizlemesi. Tam argüman gövdesi bilinçli olarak yazılmıyor —
// iz "kim neyi çağırdı"ya yeter, sorgu metni hassas olabilir.
func mcpCallAuditDetails(tool string, args json.RawMessage) string {
	preview := strings.TrimSpace(string(args))
	if runes := []rune(preview); len(runes) > 256 {
		preview = string(runes[:256]) + "…"
	}
	b, _ := json.Marshal(map[string]any{"tool": tool, "argsPreview": preview})
	return string(b)
}
