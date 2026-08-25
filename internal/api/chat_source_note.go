package api

import (
	"fmt"
	"strings"
)

// chat_source_note.go — serbest tool döngüsünün KAYNAK KÜNYESİ
// (v0.10.29, Copilot denetimi bulgusu).
//
// ── KUSUR ───────────────────────────────────────────────────────────────
//
// Dört kademenin ÜÇÜ sunucu-üretimi bir künye taşıyordu:
//
//	guided → "Kaynak: servis RED özeti + baseline + …"
//	drawer → drawerSourceNote
//	RAG    → sources[] (doc/ref/chunk/score)
//	serbest döngü → HİÇBİR ŞEY (yalnız ⚙ çipleri)
//
// Yani deterministik atıf, modelin EN SERBEST olduğu ve en çok
// uydurabileceği yolda eksikti. Operatör iki kademeyi ayırt edemiyordu:
// birinde cevabın altında kaynak yazıyor, diğerinde hiçbir şey. Dipnotun
// YOKLUĞU "kaynak yok" değil "kaynak bilinmiyor" anlamına geliyordu ve
// bu ayrım hiçbir yerde anlatılmıyordu.
//
// ── ASIL DEĞER: SIFIR ARAÇ DURUMU ───────────────────────────────────────
//
// Denetimin daha sert bir bulgusu vardı: serbest döngüde model 0 tool
// çağırıp DOĞRUDAN cevap yazabiliyor ve o metin `answer` olarak
// yayınlanıyor. "En az bir tool çağrılmalı" kapısı yok; guided'ın
// ıskaladığı her soru bu yola düşüyor.
//
// Yani modele HİÇ veri gitmemişken sayı üretme fırsatı YAPISAL olarak
// açık — ve arayüzde bunu telemetriden gelen cevaptan ayıran hiçbir
// işaret yoktu. Bu künyenin en değerli hâli tool listelemek değil,
// LİSTENİN BOŞ olduğunu söylemek.
//
// ⚠ Bu bir kapı DEĞİL: model yine cevap veriyor. Kapı koymak ayrı bir
// karar (bazı sorular gerçekten telemetri istemiyor — "sen kimsin"
// v0.10.13'te tam da bu yüzden ayrı bir rotaya alınmıştı). Buradaki iş,
// operatörün AYIRT EDEBİLMESİ.

// chatSourceMaxNamed — künyede kaç araç adı yazılacak. Fazlası "+N".
// Bir cevabın altına on beş araç adı basmak, operatörün okumayacağı bir
// duvar olurdu (aynı gerekçe: engine_degraded.go joinUpTo).
const chatSourceMaxNamed = 6

// chatSourceNoteTR — serbest döngü cevabının kaynak künyesi.
//
// Saf: künye bir DOĞRULUK iddiası ve çalışan bir LLM'e bağlı olmadan
// test edilebilmeli.
func chatSourceNoteTR(tools []string) string {
	uniq := dedupePreserveOrder(tools)
	if len(uniq) == 0 {
		// ⚠ EN ÖNEMLİ DAL. Hiç araç çağrılmadı: cevap telemetriden
		// gelmiyor. Bunu söylemek, cevabı geçersiz kılmıyor — operatöre
		// neye baktığını söylüyor.
		return "\n\n⚠ Kaynak: hiçbir telemetri aracı çağrılmadı — bu cevap " +
			"yalnız modelin kendi bilgisine dayanıyor, canlı veriye değil."
	}
	named := uniq
	extra := 0
	if len(named) > chatSourceMaxNamed {
		extra = len(named) - chatSourceMaxNamed
		named = named[:chatSourceMaxNamed]
	}
	s := "\n\nKaynak: " + strings.Join(named, ", ")
	if extra > 0 {
		s += fmt.Sprintf(" +%d", extra)
	}
	return s + fmt.Sprintf(" (%d araç)", len(uniq))
}

// dedupePreserveOrder — aynı araç birden çok turda çağrılabiliyor;
// künyede iki kez yazmak sayıyı da yanlış gösterirdi.
//
// SIRA korunuyor: modelin izlediği yol, operatöre soruşturmanın şeklini
// anlatıyor (önce list_services, sonra get_trace…). Alfabetik sıralamak
// o bilgiyi silerdi.
func dedupePreserveOrder(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
