// v0.9.607 — var olan nesne için DDL GÖNDERME.
//
// Operator-reported (prod, v0.9.606 sonrası): pod artık çökmüyor ama
// READY de olmuyor — migrate'te asılı kalıyor.
//
// Zincir şöyleydi ve her adımı bir öncekinin düzeltmesiydi:
//
//	v0.9.604  kod 159 ölümcül sayılmıyor        → crashloop durdu
//	v0.9.605  sunucu istisna atmıyor            → CREATE DATABASE geçti
//	v0.9.606  sunucu bütçesi < istemci timeout  → i/o timeout bitti
//	          ama BOOT HÂLÂ BİTMİYOR
//
// Kalan sebep aritmetik: boot 158 bildirimsel DDL çalıştırıyor ve
// tıkalı bir DDL kuyruğunda her biri bütçesini (20 sn) doluyor.
// 158 × 20 sn ≈ 53 dakika. Pod ölmüyor, sadece hiç hazır olmuyor.
//
// ASIL SORU: bu DDL'lerin kaçı gerçekten gerekli? Prod'da şema aylardır
// yerinde — hepsi `IF NOT EXISTS`, yani hepsi ZATEN NO-OP. Ödediğimiz
// tek şey, sonucu baştan belli olan 158 dağıtık kuyruk turu.
//
// Çare bu yüzden bir "atlama hilesi" değil, fazlalığın kaldırılması:
// nesne zaten varsa `CREATE ... IF NOT EXISTS` göndermenin HİÇBİR
// etkisi yok. Tek sorgu ile var olanları öğrenip o ifadeleri hiç
// göndermiyoruz.
//
// NE ATLANMIYOR: ALTER'lar (kolon ekleme/tip değiştirme — yükseltme
// yolunun kendisi), DROP'lar, ve probe'a bağlı bölümler. Yalnız
// sonucu tanım gereği no-op olan CREATE'ler eleniyor.
//
// TAZE KURULUMDA DAVRANIŞ AYNI: hiçbir nesne yok → hiçbiri elenmiyor →
// tüm DDL koşuyor.
package chstore

import (
	"context"
	"log"
	"regexp"
	"strings"
)

// ddlObjectRe — `CREATE TABLE|VIEW|MATERIALIZED VIEW IF NOT EXISTS <ad>`
// ifadesinden nesne adını çıkarır.
//
// YALNIZ `IF NOT EXISTS` taşıyan CREATE'ler eşleşir ve bu bilinçli:
// onsuz bir CREATE, var olan nesnede HATA verir — yani davranışı
// "no-op" değildir ve elenemez.
var ddlObjectRe = regexp.MustCompile(
	`(?is)^\s*CREATE\s+(?:MATERIALIZED\s+VIEW|VIEW|TABLE)\s+IF\s+NOT\s+EXISTS\s+` + "`?" + `([a-zA-Z0-9_]+)` + "`?")

// ddlCreatesObject — bu ifade hangi nesneyi yaratıyor? ok=false ise
// ifade elenmeye UYGUN DEĞİL (ALTER, DROP, IF NOT EXISTS'siz CREATE…).
// SAF — tablo testli.
func ddlCreatesObject(sql string) (string, bool) {
	m := ddlObjectRe.FindStringSubmatch(sql)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// existingObjects — mevcut veritabanındaki tablo/görünüm adları.
//
// Küme modunda hem `x` hem `x_local` döner; ikisini de kümeye koyuyoruz
// çünkü adaptDDL bildirimsel adı ikisine birden çevirebiliyor ve
// eleme kararı BİLDİRİMSEL ad üzerinden veriliyor.
//
// Hata hâlinde BOŞ küme döner — yani hiçbir şey elenmez ve davranış
// bugünküyle birebir aynı olur. Emin olamadığımızda DDL'i GÖNDERMEK
// doğru taraf: fazladan bir no-op zararsız, eksik bir tablo değil.
func (s *Store) existingObjects(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	rows, err := s.conn.Query(ctx,
		`SELECT name FROM system.tables WHERE database = currentDatabase()`)
	if err != nil {
		log.Printf("[chstore] mevcut nesne listesi okunamadı (%v) — tüm DDL gönderilecek", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			log.Printf("[chstore] nesne adı taranamadı (%v) — tüm DDL gönderilecek", err)
			return map[string]bool{}
		}
		out[n] = true
		out[strings.TrimSuffix(n, "_local")] = true
	}
	if err := rows.Err(); err != nil {
		log.Printf("[chstore] nesne listesi hatası (%v) — tüm DDL gönderilecek", err)
		return map[string]bool{}
	}
	return out
}

// planDeclarativeDDL — hangi ifadeler gönderilecek, hangileri elendi.
// SAF (tablo testli): karar mantığı SQL'den ve ağdan ayrı.
func planDeclarativeDDL(stmts []string, existing map[string]bool) (send []string, skipped int) {
	for _, q := range stmts {
		if name, ok := ddlCreatesObject(q); ok && existing[name] {
			skipped++
			continue
		}
		send = append(send, q)
	}
	return send, skipped
}

// ── ADD COLUMN elemesi (v0.9.608) ────────────────────────────────────
//
// v0.9.607 CREATE'leri eledi ve prod'da 44/49'u kaldırdı — ama boot
// yine bitmedi. Kalan sebep ikinci liste: 69 ALTER, tıkalı kuyrukta
// her biri bütçesini doluyor → ~23 dakika.
//
// Bunların ~50'si `ADD COLUMN IF NOT EXISTS`. Kolon zaten varsa bu
// ifadenin etkisi — CREATE'lerde olduğu gibi — TANIM GEREĞİ yok.
// Aynı kanıt, aynı eleme.
//
// MODIFY COLUMN / MODIFY TTL / ALTER DELETE ELENMİYOR: onların no-op
// olduğunu kanıtlamak tipi ve codec'i CH'nin normalize ettiği biçimle
// karşılaştırmayı gerektirir ve o karşılaştırma kırılgandır. Yanlış
// bir eleme, uygulanmamış bir tip değişikliği demektir — sessiz ve
// kalıcı. Kanıtlayamadığımızı elemiyoruz.

// addColumnRe — `ALTER TABLE <tablo> ADD COLUMN IF NOT EXISTS <kolon>`.
// YALNIZ `IF NOT EXISTS` taşıyanlar; onsuzu var olan kolonda HATA verir.
var addColumnRe = regexp.MustCompile(
	`(?is)^\s*ALTER\s+TABLE\s+` + "`?" + `([a-zA-Z0-9_]+)` + "`?" +
		`\s+ADD\s+COLUMN\s+IF\s+NOT\s+EXISTS\s+` + "`?" + `([a-zA-Z0-9_]+)` + "`?")

// ddlAddsColumn — bu ifade hangi (tablo, kolon) çiftini ekliyor?
// ok=false ise eleme adayı DEĞİL. SAF.
func ddlAddsColumn(sql string) (table, column string, ok bool) {
	m := addColumnRe.FindStringSubmatch(sql)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// existingColumns — mevcut (tablo, kolon) çiftleri.
//
// Küme modunda `x` ve `x_local` ayrı satırlar; ikisini de koyuyoruz
// çünkü eleme kararı BİLDİRİMSEL ad üzerinden veriliyor.
//
// Hata hâlinde BOŞ küme → hiçbir şey elenmez → davranış bugünküyle
// aynı. Emin olamadığımızda ALTER'ı GÖNDERMEK doğru taraf.
func (s *Store) existingColumns(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	rows, err := s.conn.Query(ctx,
		`SELECT table, name FROM system.columns WHERE database = currentDatabase()`)
	if err != nil {
		log.Printf("[chstore] kolon listesi okunamadı (%v) — tüm ALTER gönderilecek", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var tbl, col string
		if err := rows.Scan(&tbl, &col); err != nil {
			log.Printf("[chstore] kolon taranamadı (%v) — tüm ALTER gönderilecek", err)
			return map[string]bool{}
		}
		out[tbl+"."+col] = true
		out[strings.TrimSuffix(tbl, "_local")+"."+col] = true
	}
	if err := rows.Err(); err != nil {
		log.Printf("[chstore] kolon listesi hatası (%v) — tüm ALTER gönderilecek", err)
		return map[string]bool{}
	}
	return out
}

// planAlterDDL — var olan kolonu ekleyen ALTER'ları eler. SAF.
func planAlterDDL(stmts []string, cols map[string]bool) (send []string, skipped int) {
	for _, q := range stmts {
		if tbl, col, ok := ddlAddsColumn(q); ok && cols[tbl+"."+col] {
			skipped++
			continue
		}
		send = append(send, q)
	}
	return send, skipped
}
