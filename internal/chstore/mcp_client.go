package chstore

import (
	"context"
)

// Dış MCP sunucu listesi system_settings'te "mcp_client_servers"
// anahtarında yaşar (v0.10.87, MCP istemci dilim ②). devops.go ile aynı
// bölünme: marshal/unmarshal mcpclient.Service'in işi, chstore yalnız
// baytları taşır — config struct büyüdükçe saklanan kolon şekli
// değişmez. Yeni tablo YOK (invariant 5).
const mcpClientKey = "mcp_client_servers"

// GetMCPClientSettingsRaw — kayıtlı JSON blob; hiç kaydedilmemişse nil.
func (s *Store) GetMCPClientSettingsRaw(ctx context.Context) ([]byte, error) {
	return s.GetSetting(ctx, mcpClientKey)
}

// PutMCPClientSettingsRaw — blob'u üstüne yazar.
func (s *Store) PutMCPClientSettingsRaw(ctx context.Context, raw []byte) error {
	return s.PutSetting(ctx, mcpClientKey, raw)
}
