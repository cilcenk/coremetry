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
