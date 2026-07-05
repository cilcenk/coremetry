package chstore

import "testing"

// v0.8.297 (operator-reported: "postgres/mssql/oracle/redis gittiği yerlerde
// instance name veya db.name yazsa iyi olur — sadece oracle yazıyor") — the
// sampled /api/service-map db nodes now carry the dominant db.name for their
// db.system (db_summary_5m via DbNamesBySystem), same enrichment the MV path
// has had since v0.8.37. Contract of annotateDbNames:
//   - only Kind=="db" nodes are touched;
//   - an already-set DbName is never overwritten;
//   - unknown system / nil map / empty name are no-ops.
func TestAnnotateDbNames(t *testing.T) {
	names := map[string]string{"oracle": "COREBANK", "postgresql": "core_txn"}

	t.Run("db node gets the dominant db.name for its system", func(t *testing.T) {
		nodes := []ServiceMapNode{
			{Service: "db:oracle", Kind: "db", Subkind: "oracle"},
			{Service: "payments"}, // real service — untouched
		}
		annotateDbNames(nodes, names)
		if nodes[0].DbName != "COREBANK" {
			t.Fatalf("oracle node DbName = %q, want COREBANK", nodes[0].DbName)
		}
		if nodes[1].DbName != "" {
			t.Fatalf("service node must not get a db.name, got %q", nodes[1].DbName)
		}
	})

	t.Run("queue and external nodes are untouched", func(t *testing.T) {
		nodes := []ServiceMapNode{
			{Service: "queue:kafka", Kind: "queue", Subkind: "kafka"},
			{Service: "ext:stripe.com", Kind: "external", Subkind: "stripe.com"},
		}
		annotateDbNames(nodes, map[string]string{"kafka": "nope", "stripe.com": "nope"})
		if nodes[0].DbName != "" || nodes[1].DbName != "" {
			t.Fatalf("non-db nodes must stay empty, got %q / %q", nodes[0].DbName, nodes[1].DbName)
		}
	})

	t.Run("existing DbName is never overwritten", func(t *testing.T) {
		nodes := []ServiceMapNode{{Service: "db:oracle", Kind: "db", Subkind: "oracle", DbName: "LEGACY"}}
		annotateDbNames(nodes, names)
		if nodes[0].DbName != "LEGACY" {
			t.Fatalf("DbName overwritten: %q", nodes[0].DbName)
		}
	})

	t.Run("unknown system and nil map are no-ops", func(t *testing.T) {
		nodes := []ServiceMapNode{{Service: "db:clickhouse", Kind: "db", Subkind: "clickhouse"}}
		annotateDbNames(nodes, names)
		if nodes[0].DbName != "" {
			t.Fatalf("unknown system must stay empty, got %q", nodes[0].DbName)
		}
		annotateDbNames(nodes, nil) // must not panic
	})
}
