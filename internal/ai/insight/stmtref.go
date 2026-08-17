package insight

import (
	"net/url"
	"strconv"
	"strings"
)

// stmtref.go — `slow-query` kartının kimlik kodeği (v0.9.1137, Faz 2.4).
// Saf, tablo-testli.
//
// Kimlik ZATEN var ve ÜÇ yerde aynı: /slow-queries çekmecesinin
// `?stmt=` paramı (frontend/src/pages/slowqueries/stmtParam.ts),
// /api/databases/statements/detail'in (hash, system) argüman çifti ve
// v0.8.375'in kalıcı stmt_hash'i. Kart DÖRDÜNCÜ bir yazılış üretmiyor,
// aynı dizeyi okuyor:
//
//	"<stmtHash>[|<dbSystem>]"
//
// Neden hash bir DİZE olarak taşınıyor: uint64 JSON'da 2^53 sonrası
// sessizce hassasiyet kaybeder (SlowQueryRow.stmtHash sözleşmesi). Bu
// yüzden tel üstünde ondalık dize, sunucuda ParseUint.
//
// KABUL TESTİ decodeStmtParam ile BİREBİR aynı olmalı: FE'nin
// reddettiği bir dizeyi sunucunun kabul etmesi (ya da tersi) "link
// çalışıyor ama çekmece açılmıyor" sınıfı üretir. Reddedilenler:
//   - rakam-dışı ya da 20 haneden uzun hash,
//   - "0" (MV'nin "ifade yok" nöbetçisi — hiçbir sınıfı adreslemez),
//   - ikiden fazla parça,
//   - ikinci parça VAR ama boş,
//   - bozuk %-kaçışı.
type StmtRef struct {
	// Hash — ayrıştırılmış kimlik.
	Hash uint64
	// System — db_system kapsamı; "" = motorlar arası katlanmış
	// (katalogun varsayılanı).
	System string
	// Param — kanonik `?stmt=` değeri (linkler bunu taşır). ESCAPE
	// EDİLMEZ: href() url.Values ile kodlar, iki kez kodlamak '|'yi
	// %257C yapar ve FE kodeği onu tanımaz.
	Param string
}

// ParseStmtRef — id → StmtRef. ok=false ⇒ 400 (LLM'e hiç gitmez).
func ParseStmtRef(raw string) (StmtRef, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return StmtRef{}, false
	}
	parts := strings.Split(raw, "|")
	if len(parts) > 2 || parts[0] == "" {
		return StmtRef{}, false
	}
	digits := parts[0]
	if len(digits) > 20 {
		return StmtRef{}, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return StmtRef{}, false
		}
	}
	hash, err := strconv.ParseUint(digits, 10, 64)
	if err != nil || hash == 0 {
		return StmtRef{}, false
	}
	system := ""
	if len(parts) == 2 {
		if parts[1] == "" {
			return StmtRef{}, false
		}
		dec, err := url.QueryUnescape(parts[1])
		if err != nil || strings.TrimSpace(dec) == "" {
			return StmtRef{}, false
		}
		system = dec
	}
	return StmtRef{Hash: hash, System: system, Param: StmtParam(hash, system)}, true
}

// StmtParam — kanonik `?stmt=` üreticisi.
//
// Sistem adı YALNIZ güvenli karakterlerden oluşuyorsa taşınır; aksi
// halde DÜŞÜRÜLÜR ve link motorlar arası katlanmış hâle açılır.
// Gerekçe: FE kodeği ikinci parçayı decodeURIComponent'ten geçiriyor ve
// bozuk bir %-kaçışı orada ATAR (çekmece hiç açılmaz). Bugün gerçek
// değerler (postgresql/oracle/mysql/redis/mssql) bu kümenin içinde;
// dışına çıkan bir değerde DAHA GENİŞ ama açılan bir kapsam,
// açılmayan bir çekmeceden iyidir (v0.9.655: kırık link, link
// yokluğundan kötü).
func StmtParam(hash uint64, system string) string {
	p := strconv.FormatUint(hash, 10)
	if s := strings.TrimSpace(system); s != "" && stmtSystemSafe(s) {
		p += "|" + s
	}
	return p
}

func stmtSystemSafe(s string) bool {
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}
