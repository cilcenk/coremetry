package api

import "strings"

// chat_chart_origin.go — CANLI GRAFİĞİ YALNIZ SUNUCU KURAR (v0.10.47).
//
// Copilot denetiminin kapatılmamış son maddesi: sohbetteki bir ```chart```
// çitinin KÖKENİ arayüzde ayırt edilmiyor.
//
// ── AÇIK NEREDE ─────────────────────────────────────────────────────────
//
// Sunucu tarafı ZATEN sağlam: `render_chart` aracının çıktısı
// `chatChartBlock` ile ayrıştırılıyor, `ok:false` dönen bir kapsam
// (olmayan servis) hiç çit olmuyor ve çit modelin ham argümanlarından
// DEĞİL, doğrulanmış spec'ten kuruluyor (copilot_chat.go:456).
//
// Açık ÖBÜR tarafta: sunucu çitleri cevabın SONUNA ekliyor (appendCharts),
// modelin kendi metnine hiç dokunmuyordu. Model düzyazısının içine kendi
// ```chart``` çitini yazarsa arayüz onu ayırt edemiyor — chatMarkdown.ts
// yalnız `lang === 'chart'`e bakıyor — ve UYDURMA bir kapsamla CANLI bir
// grafik çiziyordu.
//
// Doğru veri + yanlış kapsam, düzyazıdan daha ikna edici bir hatadır:
// grafik daha yüksek güven taşır. (Aynı gerekçe v0.9.1186'da birimi
// modelin elinden almıştı.)
//
// ── NEDEN İŞARETLEME DEĞİL, SÖKME ───────────────────────────────────────
//
// İlk akla gelen çözüm çiti "sunucu kurdu" diye işaretlemekti. İşE
// YARAMAZ: model, sunucunun araç sonucunu KENDİ bağlamında görüyor, yani
// işareti birebir kopyalayabilir. Kopyalanabilir bir işaret köken
// kanıtlamaz.
//
// Sökme ise kanıta ihtiyaç duymuyor, çünkü ayrım YAPISAL: sunucu kendi
// çitlerini modelin metnine değil, metnin SONUNA ekliyor. Dolayısıyla
// modelin metninde bulunan her chart çiti tanım gereği model-yazımıdır.
// Hiçbir prompt modele çit yazmasını söylemiyor (prompts.go:1258 tersini
// söylüyor: "render_chart çağır"), yani meşru bir kayıp yok.
//
// ── SESSİZ SİLME DE YANLIŞ OLURDU ───────────────────────────────────────
//
// Çiti sessizce yok etmek, modelin düzyazısında "aşağıdaki grafikte
// görüldüğü gibi" diyen bir cümleyi sahipsiz bırakırdı. Yerine görünür
// bir not konuyor: operatör NE OLDUĞUNU okuyor.

// modelChartFenceNoteTR — sökülen çitin yerine geçen görünür not.
//
// Tek satır: sohbet balonunda bir paragraflık uyarı, cevabın kendisini
// bastırır. Ne olduğunu ve NİÇİN olduğunu söylüyor.
const modelChartFenceNoteTR = "⚠ *Model burada bir grafik çizmeye çalıştı; " +
	"kapsamı doğrulanmadığı için çizilmedi — canlı grafikleri yalnız sunucu kurar.*"

// isFenceLine — chatMarkdown.ts'in FENCE kuralının AYNISI: `/^\s{0,3}```/`.
//
// ⚠ Bu iki yazımın AYNI kalması sözleşme. Arayüz daha gevşek eşleşirse
// (örn. 4 boşluk girinti) sökülmemiş bir çit yine canlı çizilir; daha katı
// eşleşirse sunucu meşru bir kod bloğunu boşuna söker. Kapı:
// chat_chart_origin_test.go, chatMarkdown.ts'i okuyup kuralı kıyaslıyor.
func isFenceLine(line string) bool {
	n := 0
	for n < len(line) && n < 4 && (line[n] == ' ' || line[n] == '\t') {
		n++
	}
	return n <= 3 && strings.HasPrefix(line[n:], "```")
}

// fenceLang — çit dilini chatMarkdown.ts ile aynı şekilde çıkarır:
// `line.trim().slice(3).trim().split(/\s+/)[0].toLowerCase()`.
func fenceLang(line string) string {
	t := strings.TrimSpace(line)
	if len(t) < 3 {
		return ""
	}
	f := strings.Fields(t[3:])
	if len(f) == 0 {
		// Dilsiz çit (``` tek başına). Fields boş dilim döner — indekslemek
		// PANİK ederdi ve bu dal sohbette EN SIK görülen çit biçimi.
		return ""
	}
	return strings.ToLower(f[0])
}

// stripModelChartFences — modelin kendi yazdığı ```chart``` çitlerini
// görünür bir notla değiştirir. Sökülen çit sayısını da döndürür.
//
// chart OLMAYAN çitler AYNEN korunuyor ve kapanışlarına kadar ATLANIYOR —
// atlamazsak bir ```json bloğunun İÇİNDEKİ "```chart" metni çit sanılır ve
// modelin gösterdiği örnek sessizce bozulurdu. (Arayüz de tam böyle
// davranıyor: fence bir sonraki FENCE satırında kapanır.)
func stripModelChartFences(text string) (string, int) {
	if !strings.Contains(text, "```") {
		return text, 0
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	stripped := 0

	for i := 0; i < len(lines); i++ {
		if !isFenceLine(lines[i]) {
			out = append(out, lines[i])
			continue
		}
		isChart := fenceLang(lines[i]) == "chart"
		// Gövdeyi bir sonraki çit satırına kadar tüket (kapanış DAHİL).
		j := i + 1
		body := []string{}
		for j < len(lines) && !isFenceLine(lines[j]) {
			body = append(body, lines[j])
			j++
		}
		closed := j < len(lines)

		if isChart {
			out = append(out, modelChartFenceNoteTR)
			stripped++
		} else {
			out = append(out, lines[i])
			out = append(out, body...)
			if closed {
				out = append(out, lines[j])
			}
		}
		if closed {
			i = j
		} else {
			i = len(lines)
		}
	}
	return strings.Join(out, "\n"), stripped
}
