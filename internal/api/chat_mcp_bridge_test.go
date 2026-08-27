package api

// chat_mcp_bridge_test.go — v0.10.88 (MCP istemci dilim ③).
// Köprünün saf sözleşmeleri + döngüye ULAŞILABİLİRLİK pinleri
// ([[feedback-tested-but-unreachable]] — saf çekirdek yeşilken çağrı
// yolu unutulursa dış tool'lar sessizce hiç görünmez).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/mcpclient"
)

func TestMcpToolAllowed(t *testing.T) {
	cases := []struct {
		name        string
		allow, deny []string
		tool        string
		want        bool
	}{
		{"boş listeler → hepsi", nil, nil, "search_kb", true},
		{"allow tam eşleşme", []string{"search_kb"}, nil, "search_kb", true},
		{"allow dışı düşer", []string{"search_kb"}, nil, "get_doc", false},
		{"allow önek deseni", []string{"search_*"}, nil, "search_kb", true},
		{"deny KAZANIR", []string{"search_*"}, []string{"search_kb"}, "search_kb", false},
		{"deny önek deseni", nil, []string{"admin_*"}, "admin_reset", false},
		{"boş desen eşleşmez", []string{""}, nil, "x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpToolAllowed(tc.allow, tc.deny, tc.tool); got != tc.want {
				t.Errorf("allowed(%v,%v,%q)=%v", tc.allow, tc.deny, tc.tool, got)
			}
		})
	}
}

func TestRepeatCallKeyIsCanonical(t *testing.T) {
	// Anahtar sırası değişse de AYNI çağrı.
	a := repeatCallKey("t", json.RawMessage(`{"x":1,"y":"z"}`))
	b := repeatCallKey("t", json.RawMessage(`{"y":"z","x":1}`))
	if a != b {
		t.Errorf("kanonikleştirme yok: %q != %q", a, b)
	}
	// Farklı değer farklı çağrıdır.
	c := repeatCallKey("t", json.RawMessage(`{"x":2,"y":"z"}`))
	if a == c {
		t.Error("farklı argüman aynı anahtara indi")
	}
	// Farklı tool aynı argümanla farklı çağrıdır.
	if repeatCallKey("t2", json.RawMessage(`{"x":1,"y":"z"}`)) == a {
		t.Error("tool adı anahtara girmiyor")
	}
	// Bozuk JSON ham hâliyle anahtarlanır — muhafız çağrıyı YOK sayamaz.
	if repeatCallKey("t", json.RawMessage(`{bozuk`)) != "t\x00{bozuk" {
		t.Error("bozuk argüman ham anahtarlanmadı")
	}
}

func TestMarkRepeatedCall(t *testing.T) {
	seen := map[string]bool{}
	if markRepeatedCall(seen, "t", json.RawMessage(`{"x":1,"y":2}`)) {
		t.Fatal("ilk çağrı tekrar sayıldı")
	}
	// İkinci kopya — anahtar sırası FARKLI yazılmış olsa da yakalanır.
	if !markRepeatedCall(seen, "t", json.RawMessage(`{"y":2,"x":1}`)) {
		t.Fatal("kanonik ikinci kopya yakalanmadı")
	}
	if markRepeatedCall(seen, "t", json.RawMessage(`{"x":9}`)) {
		t.Fatal("farklı argüman tekrar sayıldı")
	}
	if markRepeatedCall(seen, "t2", json.RawMessage(`{"x":1,"y":2}`)) {
		t.Fatal("farklı tool tekrar sayıldı")
	}
}

func TestExternalChatToolsWrap(t *testing.T) {
	pts := []mcpclient.PrefixedTool{
		{Server: "kb", Name: "kb__search", Def: mcpclient.ToolDef{
			Name: "search", Description: "bilgi ara",
			InputSchema: map[string]any{"type": "object"},
		}},
		{Server: "kb", Name: "kb__admin_reset", Def: mcpclient.ToolDef{
			Name: "admin_reset", Description: "sıfırla",
		}},
	}
	rules := func(server string) ([]string, []string) { return nil, []string{"admin_*"} }
	var calledName string
	var audited []string
	call := func(_ context.Context, prefixed string, args []byte) (string, bool, error) {
		calledName = prefixed
		return "cevap", false, nil
	}
	onCall := func(server, tool string, _ json.RawMessage) {
		audited = append(audited, server+"/"+tool)
	}

	ext := externalChatTools(pts, rules, call, onCall)
	// deny süzgeci köprüde: admin_reset modele HİÇ görünmez.
	if len(ext) != 1 || ext[0].Name != "kb__search" {
		t.Fatalf("süzgeç yanlış: %+v", ext)
	}
	// Kaynak etiketi: dış içerik yerli kanıtla karışmasın.
	if !strings.HasPrefix(ext[0].Description, "[dış: kb]") ||
		!strings.HasPrefix(ext[0].ChatDescription(), "[dış: kb]") {
		t.Errorf("dış etiketi yok: %q", ext[0].Description)
	}
	if ext[0].MinRole != auth.RoleViewer {
		t.Errorf("MinRole=%v, viewer olmalı", ext[0].MinRole)
	}

	out, err := ext[0].Handler(context.Background(), json.RawMessage(`{"q":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["content"] != "cevap" || m["server"] != "kb" || m["is_error"] != nil {
		t.Errorf("sonuç zarfı: %+v", m)
	}
	if calledName != "kb__search" {
		t.Errorf("çağrı önekli ada gitmedi: %q", calledName)
	}
	if len(audited) != 1 || audited[0] != "kb/search" {
		t.Errorf("audit closure çağrılmadı: %v", audited)
	}
}

func TestExternalChatToolsErrorPaths(t *testing.T) {
	pts := []mcpclient.PrefixedTool{{Server: "kb", Name: "kb__t",
		Def: mcpclient.ToolDef{Name: "t", Description: "d"}}}
	rules := func(string) ([]string, []string) { return nil, nil }

	t.Run("sunucunun isError bayrağı zarfa geçer", func(t *testing.T) {
		ext := externalChatTools(pts, rules,
			func(context.Context, string, []byte) (string, bool, error) {
				return "kayıt yok", true, nil
			}, func(string, string, json.RawMessage) {})
		out, err := ext[0].Handler(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		m := out.(map[string]any)
		if m["is_error"] != true || m["content"] != "kayıt yok" {
			t.Errorf("isError zarfı: %+v", m)
		}
	})
	t.Run("taşıma hatası error olarak döner (ToolErrorJSON yolu)", func(t *testing.T) {
		ext := externalChatTools(pts, rules,
			func(context.Context, string, []byte) (string, bool, error) {
				return "", false, errors.New("bağlantı koptu")
			}, func(string, string, json.RawMessage) {})
		if _, err := ext[0].Handler(context.Background(), nil); err == nil {
			t.Error("taşıma hatası yutuldu")
		}
	})
}

func TestMcpCallAuditDetailsCapsArgs(t *testing.T) {
	long := strings.Repeat("a", 1000)
	raw := mcpCallAuditDetails("t", json.RawMessage(`{"q":"`+long+`"}`))
	var m struct {
		Tool        string `json:"tool"`
		ArgsPreview string `json:"argsPreview"`
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if m.Tool != "t" || len([]rune(m.ArgsPreview)) > 260 {
		t.Errorf("iz kırpılmadı: tool=%q len=%d", m.Tool, len(m.ArgsPreview))
	}
	if !strings.HasSuffix(m.ArgsPreview, "…") {
		t.Error("kırpma işareti yok — iz tam görünümü taklit ediyor")
	}
}

// ── ULAŞILABİLİRLİK — köprü ve muhafız döngüye gerçekten bağlı ─────────
//
// repeatedCallJSON'un sözleşme alanları da burada pinli: ToolErrorJSON
// şekliyle (error/retryable/hint) aynı olmalı ki model iki hata biçimi
// görmesin.
func TestMcpBridgeIsReachable(t *testing.T) {
	src, err := os.ReadFile("copilot_chat.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for needle, why := range map[string]string{
		"externalChatTools(":               "dış tool köprüsü döngüden çağrılmıyor — ölü yol",
		"markRepeatedCall(seenToolCalls, tc.Name, tc.Input)": "tekrar muhafızı yürütme yolunda değil",
		"s.mcpClient.Registry().Call":      "çağrılar Registry üzerinden gitmiyor",
		`"mcp.call", "mcp_server"`:         "dış çağrı audit izi düşmüş",
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("%s (aranan: %q)", why, needle)
		}
	}
	for _, field := range []string{`"error"`, `"retryable"`, `"hint"`} {
		if !strings.Contains(repeatedCallJSON, field) {
			t.Errorf("repeatedCallJSON %s alanını taşımıyor — model iki hata biçimi görür", field)
		}
	}
}
