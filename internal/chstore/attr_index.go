package chstore

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
)

// attr_index.go — v0.10.299 (docs/audit/trace-attribute-search.md Dilim 1a,
// operatör onayı 2026-09-03): HER attribute için tek, attribute sayısından
// bağımsız eşitlik indeksi.
//
// Ölçülen kusur (audit G1): terfi etmemiş bir attribute filtresi
// `attr_values[indexOf(attr_keys,'k')] = v` hiçbir skip index kullanmıyor —
// pencerenin TAMAMI okunuyor (868k/868k satır, değer nadir de olsa yok da
// olsa). Çare: her span için `cityHash64(k <US> v)` dizisi (MATERIALIZED —
// INSERT anında sunucu hesaplar, Go yoluna dokunmaz, Distributed forward
// düşürür: dağıtık-güvenli) + `bloom_filter(0.01)` skip index. Ölçüldü
// (scratch, 24 s): nadir değerde 868k → 65k satır, 155 MiB → 4.9 MiB;
// kolon 10 B/satır, indeks 0.3 B/satır. Yaygın değerde budamaz (Datadog
// facet davranışı) — o yol Dilim 2 facet kaydı.
//
// Ayırıcı 0x1F (ASCII unit separator): anahtar/değer içinde `=` olabilir,
// 0x1F pratikte olmaz; SQL'de `'\x1F'` kaçışı (CH string literal), Go
// tarafında AttrKVSep. Derleyici (Dilim 1b) hash'i SUNUCUDA üretir
// (`cityHash64(concat(?, '\x1F', ?))`, iki bağlama) — Go'da CityHash
// uygulaması gerekmez, Go↔CH hash uyumu sorusu doğmaz.
//
// Eski part'lar: MATERIALIZED kolon okuma anında hesaplanır, indeks ise
// yalnız YENİ part'larda — kazanç retention boyunca dolar (operatör kararı:
// kademeli; `MATERIALIZE INDEX` opsiyonel, migrations/0014 ADIM 6).
// Prod dış Distributed: boot koşmaz (0013 sözleşmesi) — migrations/
// 0014_attr_kvh.sql elle; ifade bu dosyayla BİREBİR (attr_index_migration_test).

// AttrKVSep — anahtar ile değer arasındaki ayırıcı (0x1F).
const AttrKVSep = "\x1F"

// attrKVSepSQL — aynı ayırıcının CH string literal'i (raw string: `\x1F`
// metni CH'ye gider, CH kaçışı çözer).
const attrKVSepSQL = `'\x1F'`

// AttrKVHashSQL — derleyicinin bir (k, v) çifti için hash ifadesi; iki
// bağlama argümanı (anahtar, değer) — kontrol karakteri asla bağlanmaz.
const AttrKVHashSQL = "cityHash64(concat(?, " + attrKVSepSQL + ", ?))"

type attrIndexCol struct {
	col, keysCol, valsCol, idx, keysIdx string
}

// attrIndexCols — span ve resource kapsamı; ikisi de aynı şekil.
var attrIndexCols = []attrIndexCol{
	{col: "attr_kvh", keysCol: "attr_keys", valsCol: "attr_values", idx: "idx_attr_kvh", keysIdx: "idx_attr_keys"},
	{col: "res_kvh", keysCol: "res_keys", valsCol: "res_values", idx: "idx_res_kvh", keysIdx: "idx_res_keys"},
}

// attrIndexExpr — MATERIALIZED ifadesi (0014 ile birebir).
func attrIndexExpr(c attrIndexCol) string {
	return fmt.Sprintf("arrayMap((k, v) -> cityHash64(concat(k, %s, v)), %s, %s)", attrKVSepSQL, c.keysCol, c.valsCol)
}

// attrIndexDDL — eksik olanı ekler (idempotent); sıra kolon → kv indeksi →
// anahtar indeksi. Onarım (DROP+ADD) YOK: ifade tek yazımlı, yazım tuzağı
// (v0.9.621) burada yok.
func attrIndexDDL(c attrIndexCol, colExists, idxExists, keysIdxExists bool) []string {
	var out []string
	if !colExists {
		out = append(out, fmt.Sprintf(
			"ALTER TABLE spans ADD COLUMN IF NOT EXISTS %s Array(UInt64) MATERIALIZED %s CODEC(ZSTD(3))",
			c.col, attrIndexExpr(c)))
	}
	if !idxExists {
		out = append(out, fmt.Sprintf(
			"ALTER TABLE spans ADD INDEX IF NOT EXISTS %s %s TYPE bloom_filter(0.01) GRANULARITY 4",
			c.idx, c.col))
	}
	if !keysIdxExists {
		out = append(out, fmt.Sprintf(
			"ALTER TABLE spans ADD INDEX IF NOT EXISTS %s %s TYPE bloom_filter(0.01) GRANULARITY 4",
			c.keysIdx, c.keysCol))
	}
	return out
}

// repairAttrIndexCols — boot: uygulama yönetimli kurulumda eksik kolon /
// indeksleri ekler (küme kipinde execDDL erteler → iki-boot sözleşmesi).
// Dış Distributed prod'da atlanır (0014 elle).
func (s *Store) repairAttrIndexCols(ctx context.Context) map[string]bool {
	ensured := map[string]bool{}
	if s.spansIsExternalDistributed(ctx) {
		log.Printf("[chstore] dış Distributed `spans` — attribute hash indeksi (attr_kvh) ATLANDI; migrations/0014_attr_kvh.sql elle")
		return ensured
	}
	for _, c := range attrIndexCols {
		_, colExists := s.spansColumnExpr(ctx, c.col)
		stmts := attrIndexDDL(c, colExists, s.spansIndexExists(ctx, c.idx), s.spansIndexExists(ctx, c.keysIdx))
		ensured[c.col] = true
		for _, q := range stmts {
			if err := s.execDDL(ctx, q); err != nil {
				ensured[c.col] = false
				log.Printf("[chstore] attribute hash indeksi DDL'i başarısız (yumuşak): %v", err)
				break
			}
		}
	}
	return ensured
}

// probeAttrIndex — iki hash kolonu da var mı? MATERIALIZED kolon eski
// part'larda okuma anında hesaplandığından "var" = "kullanılabilir"
// (terfi kolonlarındaki "var ≠ dolu" tuzağı burada yok: tek yazım, tek
// ifade). İndeksin varlığı sorgu doğruluğunu etkilemez, yalnız hızı.
func (s *Store) probeAttrIndex(ctx context.Context) bool {
	for _, c := range attrIndexCols {
		if _, ok := s.spansColumnExpr(ctx, c.col); !ok {
			return false
		}
	}
	return true
}

var (
	attrIndexReady atomic.Bool
	// attrIndexUsed — derleyicinin bloom yolunu seçtiği yüklem sayısı
	// (/api/health `attr_index_used`; self-observability kuralı).
	attrIndexUsed atomic.Uint64
)

// AttrIndexStats — (bloom yüklemi sayısı, indeks hazır mı).
func AttrIndexStats() (uint64, bool) { return attrIndexUsed.Load(), attrIndexReady.Load() }

// AttrIndexUsed — /api/health sayacı.
func AttrIndexUsed() uint64 { return attrIndexUsed.Load() }

// registerAttrIndex — boot / ertelenmiş DDL inince yayınlanır.
func registerAttrIndex(ok bool) {
	attrIndexReady.Store(ok)
	if ok {
		log.Printf("[chstore] attribute hash indeksi hazır: attr_kvh/res_kvh — eşitlik/IN filtreleri bloom yolunda")
	}
}

// AttrIndexAvailable — derleyici kapısı (Dilim 1b): false iken dizi yolu aynen.
func AttrIndexAvailable() bool { return attrIndexReady.Load() }
