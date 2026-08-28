package chstore

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// v0.10.127 — K8s entity katmanı AŞAMA 3 adım 1: şema sözleşmesi.
// Tasarım: docs/plans/entity-layer-design-2026-08-28.md §2.
//
// Çivilenen sözleşmeler:
//   - entities / entity_relations: ReplacingMergeTree(version), ORDER BY
//     dedup anahtarı = (…, entity_id|child_id, valid_from) — ömür ayrımı
//     valid_from'da; PARTITION BY YOK (Kural P1: yeniden yazılan last_seen
//     partition'da olsaydı FINAL kopyaları temizleyemezdi); TTL last_seen.
//   - entity_sync_runs: partition kolonu (started_at) ORDER BY'da,
//     yeniden yazılmıyor.
//   - entity_seen MV'leri TERFİ KOLONLARINI okur (indexOf yok — pod_seen'in
//     ertelenme gerekçesi buydu), k8s bağlamsız span'leri kapıda eler,
//     filtre öznesi service_name önde, tDigest taşımaz.
//   - İki MV gün-bir üç kayıtta (highVolumeTables / defaultShardPolicy /
//     tablesWithoutTraceID) — v0.5.426 / v0.8.375 dersi.

func TestEntityStateTablesDDL(t *testing.T) {
	tables := migrateDDLSlice(t, "tables")
	orderBy := clauseRe("ORDER BY")
	cases := []struct {
		name      string
		wantOrder []string // ORDER BY içinde geçmesi gerekenler
		wantTTL   string
		partition string // "" = PARTITION BY olmamalı
	}{
		{"entities", []string{"entity_type", "cluster_id", "entity_id", "valid_from"}, "last_seen", ""},
		{"entity_relations", []string{"rel_type", "cluster_id", "parent_id", "child_id", "valid_from"}, "last_seen", ""},
		{"entity_sync_runs", []string{"cluster_id", "started_at"}, "started_at", "toYYYYMM(started_at)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ddl := tableDDLByName(tables, c.name)
			if ddl == "" {
				t.Fatalf("%s `tables` diliminde yok", c.name)
			}
			if !strings.Contains(ddl, "ReplacingMergeTree(version)") {
				t.Fatalf("%s state tablosu ReplacingMergeTree(version) olmalı", c.name)
			}
			m := orderBy.FindStringSubmatch(ddl)
			if m == nil {
				t.Fatalf("%s ORDER BY bulunamadı", c.name)
			}
			for _, col := range c.wantOrder {
				if !regexp.MustCompile(`\b` + col + `\b`).MatchString(m[1]) {
					t.Fatalf("%s ORDER BY %q kolonunu içermeli: %s", c.name, col, m[1])
				}
			}
			if !strings.Contains(ddl, "TTL "+c.wantTTL) {
				t.Fatalf("%s TTL %s üzerinden olmalı", c.name, c.wantTTL)
			}
			hasPart := strings.Contains(ddl, "PARTITION BY")
			if c.partition == "" && hasPart {
				t.Fatalf("%s PARTITION BY taşımamalı (Kural P1)", c.name)
			}
			if c.partition != "" && !strings.Contains(ddl, "PARTITION BY "+c.partition) {
				t.Fatalf("%s PARTITION BY %s olmalı", c.name, c.partition)
			}
		})
	}
}

func TestEntitySeenMVDDL(t *testing.T) {
	cases := []struct {
		name, interval string
		ttlDays        int
	}{
		{"entity_seen_1m", "1 MINUTE", 3},
		{"entity_seen_5m", "5 MINUTE", 30},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ddl := entitySeenMVDDL(c.name, c.interval, c.ttlDays)
			for _, want := range []string{
				"CREATE MATERIALIZED VIEW IF NOT EXISTS " + c.name,
				"ENGINE = AggregatingMergeTree",
				"PARTITION BY toDate(time_bucket)",
				"ORDER BY (service_name, cluster, k8s_namespace, k8s_pod, time_bucket)",
				"TTL toDate(time_bucket) + INTERVAL " + strconv.Itoa(c.ttlDays) + " DAY",
				"toStartOfInterval(time, INTERVAL " + c.interval + ")",
				"FROM spans",
				"WHERE k8s_pod != ''",
				"span_count_state", "error_count_state", "duration_sum_state",
				"first_seen_state", "last_seen_state", "k8s_node",
			} {
				if !strings.Contains(ddl, want) {
					t.Fatalf("%s DDL %q içermeli:\n%s", c.name, want, ddl)
				}
			}
			// Terfi kolonu okur, dizi açmaz — pod_seen'in ertelenme sebebi.
			if strings.Contains(ddl, "indexOf(") {
				t.Fatalf("%s dizi araması yapmamalı (terfi kolonu okur):\n%s", c.name, ddl)
			}
			// Yalın: yüzdelik state'i yok (satır başına ~1.5 KB).
			if strings.Contains(ddl, "quantiles") {
				t.Fatalf("%s tDigest taşımamalı", c.name)
			}
		})
	}
}

func TestEntitySeenRegisteredDayOne(t *testing.T) {
	for _, mv := range []string{"entity_seen_1m", "entity_seen_5m"} {
		if !highVolumeTables[mv] {
			t.Errorf("%s highVolumeTables'da yok — küme kipinde _local/Distributed çıkmaz", mv)
		}
		if got := defaultShardPolicy[mv]; got != "cityHash64(service_name)" {
			t.Errorf("%s shard anahtarı cityHash64(service_name) olmalı, alınan %q", mv, got)
		}
		if !tablesWithoutTraceID[mv] {
			t.Errorf("%s tablesWithoutTraceID'de yok", mv)
		}
	}
}
