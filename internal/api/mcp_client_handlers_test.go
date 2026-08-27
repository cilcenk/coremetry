package api

// mcp_client_handlers_test.go — v0.10.87 (MCP istemci dilim ②).
// devops_handlers_test.go kalıbı: sır birleştirme tablosu + sırsız
// audit izi. Adlar jenerik.

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/mcpclient"
)

func mcpCur(servers ...mcpclient.ServerConfig) mcpclient.Settings {
	return mcpclient.Settings{Servers: servers}
}

func TestMergeMCPServers_SecretMerge(t *testing.T) {
	stored := mcpCur(mcpclient.ServerConfig{
		Name: "Runbook KB", Transport: "http", URL: "https://mcp.internal/kb",
		Token: "saklı-token", Enabled: true,
	})
	base := mcpServerInput{Name: "Runbook KB", Transport: "http",
		URL: "https://mcp.internal/kb", Enabled: true}

	cases := []struct {
		name      string
		token     string
		wantToken string
	}{
		{"boş girdi saklıyı korur", "", "saklı-token"},
		{"sentinel saklıyı korur", secretKept, "saklı-token"},
		{"yeni değer değiştirir", "yeni-token", "yeni-token"},
		// Sentinel'i İÇEREN değer keep-sinyali DEĞİL (devops tablosunun
		// aynı satırı): operatör gerçekten yıldızlı bir token yapıştırmış
		// olabilir.
		{"sentinel-içeren değer aynen yazılır", "x" + secretKept, "x" + secretKept},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			in.Token = tc.token
			out, bad := mergeMCPServers([]mcpServerInput{in}, stored)
			if bad != "" {
				t.Fatalf("beklenmeyen 400: %s", bad)
			}
			if out.Servers[0].Token != tc.wantToken {
				t.Errorf("token=%q, istenen %q", out.Servers[0].Token, tc.wantToken)
			}
		})
	}

	t.Run("ad yazımı değişse de saklı token eşleşir", func(t *testing.T) {
		in := base
		in.Name = "runbook kb" // sanitize aynı kimliğe iner
		out, bad := mergeMCPServers([]mcpServerInput{in}, stored)
		if bad != "" || out.Servers[0].Token != "saklı-token" {
			t.Errorf("sanitize eşlemesi tutmadı: %+v / %s", out, bad)
		}
	})
	t.Run("stdio'ya geçiş http token'ını düşürür", func(t *testing.T) {
		in := mcpServerInput{Name: "Runbook KB", Transport: "stdio",
			Command: "/opt/mcp/server", Enabled: true}
		out, bad := mergeMCPServers([]mcpServerInput{in}, stored)
		if bad != "" || out.Servers[0].Token != "" {
			t.Errorf("öksüz token kaldı: %+v / %s", out, bad)
		}
	})
}

func TestMergeMCPServers_Validation(t *testing.T) {
	cases := []struct {
		name string
		in   []mcpServerInput
		want string // 400 gövdesinde geçmesi gereken parça
	}{
		{"boş ad", []mcpServerInput{{Transport: "http", URL: "https://x"}}, "sunucu adı boş"},
		{"kopya ad (sanitize sonrası)", []mcpServerInput{
			{Name: "kb one", Transport: "http", URL: "https://x", Enabled: true},
			{Name: "KB_One", Transport: "http", URL: "https://y", Enabled: true},
		}, "tekrar ediyor"},
		{"http url'siz", []mcpServerInput{{Name: "a", Transport: "http"}}, "URL ister"},
		{"http şemasız url", []mcpServerInput{{Name: "a", Transport: "http", URL: "mcp.internal"}}, "URL ister"},
		{"stdio komutsuz", []mcpServerInput{{Name: "a", Transport: "stdio"}}, "komut yolu ister"},
		{"bilinmeyen taşıma", []mcpServerInput{{Name: "a", Transport: "gopher", URL: "https://x"}}, "http ya da stdio"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, bad := mergeMCPServers(tc.in, mcpclient.Settings{})
			if bad == "" || !strings.Contains(bad, tc.want) {
				t.Errorf("400=%q, %q içermeli", bad, tc.want)
			}
		})
	}
	t.Run("boş taşıma http varsayılır", func(t *testing.T) {
		out, bad := mergeMCPServers([]mcpServerInput{
			{Name: "a", URL: "https://x", Enabled: true}}, mcpclient.Settings{})
		if bad != "" || out.Servers[0].Transport != "http" {
			t.Errorf("varsayılan taşıma: %+v / %s", out, bad)
		}
	})
}

// TestMCPServersAuditDetails_NoSecrets — token audit_log'a HİÇ girmez.
// Snapshot zaten sır taşımaz; bu test o sözleşmenin audit yoluna da
// taşındığını çiviler (devops TestDevOpsAuditDetails_NoSecrets ikizi).
func TestMCPServersAuditDetails_NoSecrets(t *testing.T) {
	svc := mcpclient.NewService()
	svc.Configure(mcpCur(mcpclient.ServerConfig{
		Name: "kb", Transport: "http", URL: "https://mcp.internal/kb",
		Token: "çok-gizli-token", Enabled: true,
	}))
	raw := string(mcpServersAuditDetails(svc.Snapshot()))
	if strings.Contains(raw, "çok-gizli-token") {
		t.Fatalf("audit izi token taşıyor: %s", raw)
	}
	for _, want := range []string{`"hasToken":true`, `"url":"https://mcp.internal/kb"`, `"count":1`} {
		if !strings.Contains(raw, want) {
			t.Errorf("audit izinde eksik: %s (iz: %s)", want, raw)
		}
	}
}

// TestMCPClientSnapshotNeverCarriesToken — GET şekli sır taşımaz;
// hasToken tek sinyaldir.
func TestMCPClientSnapshotNeverCarriesToken(t *testing.T) {
	svc := mcpclient.NewService()
	svc.Configure(mcpCur(mcpclient.ServerConfig{
		Name: "kb", Transport: "http", URL: "https://x", Token: "gizli", Enabled: true,
	}))
	snap := svc.Snapshot()
	if len(snap.Servers) != 1 || !snap.Servers[0].HasToken {
		t.Fatalf("snapshot şekli: %+v", snap)
	}
	// Yapısal iddia: ServerSnapshot tipinde token ALANI yok — alan
	// eklenirse bu dosya derlenmeye devam eder ama JSON'a bakan satır
	// yakalar.
	b := mcpServersAuditDetails(snap)
	if strings.Contains(string(b), "gizli") {
		t.Error("snapshot/audit zinciri sır sızdırdı")
	}
}
