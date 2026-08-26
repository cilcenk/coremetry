package api

import (
	"encoding/json"
	"strings"
)

// guided_chart_allowlist.go — GUIDED'DA ÇİZİLEN GRAFİK, SUNUCUNUN
// YAZDIĞI GRAFİKTİR (v0.10.70).
//
// ── AÇIK NEREDE ─────────────────────────────────────────────────────────
//
// v0.10.47 serbest döngüde modelin kendi yazdığı ```chart``` çitlerini
// söktü. v0.10.68 aynı kuralın guided'a TAŞINAMAYACAĞINI yazdı ve
// sebebini kaydetti: guided'da sunucu çiti KANIT bloğuna yazıyor, emit
// edilen cevap ise yalnız modelin anlatımı. Yani grafik ekrana ANCAK
// model çiti AKTARIRSA ulaşıyor; sökmek guided grafiklerini tamamen
// kaldırırdı.
//
// Ama 10.68 açık bir riski de yazdı: model aktardığı spec'i
// DEĞİŞTİREBİLİR (başka servis, başka agg, başka pencere) ya da kendi
// uydurduğu bir çiti araya sokabilir. Sonuç yine "doğru veriyle çizilmiş
// yanlış kapsam" — düzyazıdaki yanlıştan daha ikna edici, çünkü grafik.
//
// ── NEDEN İMZA DEĞİL, İZİN LİSTESİ ──────────────────────────────────────
//
// İlk tasarım HMAC'ti: sunucu çiti imzalar, arayüz doğrular. İşE YARAMAZ
// — arayüzde gizli anahtar yok, doğrulama yine sunucuda olmak zorunda.
// Ve sunucuda doğrulanacaksa imzaya HİÇ gerek yok:
//
//	SUNUCU NE YAZDIĞINI ZATEN BİLİYOR.
//
// Kanıt bloğu sunucu-yazımı bir metin. Ondan çıkarılan çitler, o turda
// meşru olan spec'lerin TAM listesidir. Modelin cevabındaki her çit bu
// listeye karşı sınanıyor: eşleşen geçer, eşleşmeyen sökülür.
//
// Model listeye yeni bir üye EKLEYEMEZ (liste sunucunun kendi metninden
// türüyor), yani uydurma bir çit yapısal olarak geçemez. Kopyalanabilir
// bir işaretin aksine bu köken KANITI.
//
// ── NEDEN BAŞLIK KARŞILAŞTIRILMIYOR ─────────────────────────────────────
//
// Karşılaştırma KAPSAM alanları üzerinden: hangi veri çizilecek.
// `title` bilerek dışarıda, çünkü arayüz onu zaten YOK SAYIYOR
// (v0.10.43 — başlık spec'ten değil agg'den türetiliyor). Başlığı
// karşılaştırmak, çizimi hiç etkilemeyen bir farktan meşru bir grafiği
// düşürmek olurdu.

// modelChartFenceRejectedTR — izin listesinde olmayan çitin yerine.
const modelChartFenceRejectedTR = "⚠ *Model burada sunucunun vermediği bir grafik " +
	"kapsamı yazdı; çizilmedi — kapsamı doğrulanamıyor.*"

// chartScope — bir spec'in ÇİZİMİ belirleyen alanları. `title` yok
// (arayüz onu yok sayıyor, bkz. dosya başlığı).
type chartScope struct {
	Service   string
	Operation string
	Agg       string
	RangeS    int64
	GroupBy   string
	FromNs    int64
	ToNs      int64
}

func scopeOf(s guidedChartSpec) chartScope {
	return chartScope{
		Service: s.Service, Operation: s.Operation, Agg: s.Agg,
		RangeS: s.RangeS, GroupBy: s.GroupBy, FromNs: s.FromNs, ToNs: s.ToNs,
	}
}

// serverChartScopes — SUNUCU-YAZIMI metinden meşru kapsamları çıkarır.
//
// Girdi kanıt bloğudur ve tamamen sunucu tarafından üretilmiştir; yani
// buradan çıkan liste, o turda çizilmesine izin verilen kapsamların
// TAMAMIDIR.
func serverChartScopes(evidence string) map[chartScope]bool {
	out := map[chartScope]bool{}
	forEachChartFence(evidence, func(body string) {
		var spec guidedChartSpec
		if json.Unmarshal([]byte(body), &spec) == nil && spec.Service != "" {
			out[scopeOf(spec)] = true
		}
	})
	return out
}

// filterModelChartFences — modelin cevabındaki çitleri izin listesine
// karşı süzer. Sökülen çit sayısını da döndürür.
//
// İzin listesi BOŞsa hiçbir çit geçmez: sunucu o turda grafik vermediyse
// cevapta grafik olamaz.
func filterModelChartFences(answer string, allowed map[chartScope]bool) (string, int) {
	if !strings.Contains(answer, "```") {
		return answer, 0
	}
	lines := strings.Split(answer, "\n")
	out := make([]string, 0, len(lines))
	rejected := 0

	for i := 0; i < len(lines); i++ {
		if !isFenceLine(lines[i]) {
			out = append(out, lines[i])
			continue
		}
		isChart := fenceLang(lines[i]) == "chart"
		j := i + 1
		body := []string{}
		for j < len(lines) && !isFenceLine(lines[j]) {
			body = append(body, lines[j])
			j++
		}
		closed := j < len(lines)

		if isChart {
			var spec guidedChartSpec
			ok := json.Unmarshal([]byte(strings.Join(body, "\n")), &spec) == nil &&
				allowed[scopeOf(spec)]
			if ok {
				out = append(out, lines[i])
				out = append(out, body...)
				if closed {
					out = append(out, lines[j])
				}
			} else {
				out = append(out, modelChartFenceRejectedTR)
				rejected++
			}
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
	return strings.Join(out, "\n"), rejected
}

// forEachChartFence — metindeki her ```chart``` çitinin GÖVDESİNİ gezer.
// Çit tarama kuralı chat_chart_origin.go ile AYNI yardımcılardan geliyor;
// ikinci bir tanım, iki tarayıcının sessizce ayrışması demekti.
func forEachChartFence(text string, fn func(body string)) {
	if !strings.Contains(text, "```") {
		return
	}
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		if !isFenceLine(lines[i]) {
			continue
		}
		isChart := fenceLang(lines[i]) == "chart"
		j := i + 1
		body := []string{}
		for j < len(lines) && !isFenceLine(lines[j]) {
			body = append(body, lines[j])
			j++
		}
		if isChart {
			fn(strings.Join(body, "\n"))
		}
		if j < len(lines) {
			i = j
		} else {
			i = len(lines)
		}
	}
}
