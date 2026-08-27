package chstore

import "context"

// Uygulama DB şema kataloğu anlık görüntüsü system_settings'te
// "schema_catalog" anahtarı altında durur (v0.10.115). devops.go ile
// aynı bölünme: appschema paketi marshal/unmarshal eder, chstore yalnız
// baytları taşır. Kolon tanımları kaynak kod DEĞİLDİR ama yine de
// yalnız buraya yazılır — ai_calls örneğine maske özeti gider.
const schemaCatalogKey = "schema_catalog"

// GetSchemaCatalogRaw — kayıtlı JSON blobu; hiç yoksa nil.
func (s *Store) GetSchemaCatalogRaw(ctx context.Context) ([]byte, error) {
	return s.GetSetting(ctx, schemaCatalogKey)
}

// PutSchemaCatalogRaw — blobu üzerine yazar (boş = temizle).
func (s *Store) PutSchemaCatalogRaw(ctx context.Context, raw []byte) error {
	return s.PutSetting(ctx, schemaCatalogKey, raw)
}
