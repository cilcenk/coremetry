package api

// abs_window.go — v0.10.437 (CoSRE router boşlukları D6): MUTLAK tarih/saat
// penceresi ve iki pencere kıyası. Prod /ai: "08/08/2026 saat 04-08 ile
// 08-09 arası servis süreleri" — router yalnız göreli pencereyi ("son 2
// saat") anlıyordu; mutlak tarih sessizce düşüp cevap "son 30 dk"dan
// geliyordu (yanlış zaman diliminden doğru görünen sayı — chat_anchor.go
// kusurunun aynısı). Şimdi:
//   - tek pencere → hangi rota çıkarsa çıksın çıpa (anchorTo) ve uzunluk
//     o pencere olur, bağlam adımı ilan eder;
//   - iki pencere → window_compare: aynı servisin RED'i yan yana + fark.
// Saatler TARAYICI saat dilimine göre (Context.tzOffsetMin; spec kararı:
// Europe/Istanbul sabiti DEĞİL) — sunucu UTC'de koşar, operatör yerel
// saat yazar. Pencere ≤ 24 sa, en çok 2.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type absWindow struct {
	From, To time.Time
}

var (
	absDateSlashRe = regexp.MustCompile(`\b(\d{1,2})[./](\d{1,2})[./](\d{4})\b`)
	absDateISORe   = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`)
	absDateTRRe    = regexp.MustCompile(`(?i)\b(\d{1,2})\s+(ocak|şubat|subat|mart|nisan|mayıs|mayis|haziran|temmuz|ağustos|agustos|eylül|eylul|ekim|kasım|kasim|aralık|aralik)(?:\s+(\d{4}))?\b`)
	// absHourRangeRe — "04-08", "04:00-08:30", "4 ile 8", "04.00–08.00".
	absHourRangeRe = regexp.MustCompile(`\b(\d{1,2})(?:[:.](\d{2}))?\s*(?:-|–|—|ile)\s*(\d{1,2})(?:[:.](\d{2}))?\b`)
)

var absMonthsTR = map[string]time.Month{
	"ocak": 1, "şubat": 2, "subat": 2, "mart": 3, "nisan": 4, "mayıs": 5, "mayis": 5, "haziran": 6, "temmuz": 7,
	"ağustos": 8, "agustos": 8, "eylül": 9, "eylul": 9, "ekim": 10, "kasım": 11, "kasim": 11, "aralık": 12, "aralik": 12,
}

// chatLocation — tarayıcı ofseti (UTC'den dakika, doğu pozitif) → konum.
func chatLocation(tzOffsetMin int) *time.Location {
	if tzOffsetMin == 0 {
		return time.UTC
	}
	if tzOffsetMin > 14*60 || tzOffsetMin < -12*60 {
		return time.UTC
	}
	return time.FixedZone(fmt.Sprintf("UTC%+d", tzOffsetMin/60), tzOffsetMin*60)
}

// looksLikeAbsoluteWindow — ucuz kapı (kılavuz sinyal kapısına ek): tarih
// ya da saat aralığı şekli var mı.
func looksLikeAbsoluteWindow(raw string) bool {
	return absDateSlashRe.MatchString(raw) || absDateISORe.MatchString(raw) || absDateTRRe.MatchString(raw) || absHourRangeRe.MatchString(raw)
}

type absDate struct {
	y int
	m time.Month
	d int
}

func absDates(raw string, now time.Time, loc *time.Location) (dates []absDate, stripped string) {
	stripped = raw
	for _, m := range absDateSlashRe.FindAllStringSubmatch(raw, -1) {
		d, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		y, _ := strconv.Atoi(m[3])
		if mo >= 1 && mo <= 12 && d >= 1 && d <= 31 {
			dates = append(dates, absDate{y, time.Month(mo), d})
		}
		stripped = strings.Replace(stripped, m[0], " ", 1)
	}
	for _, m := range absDateISORe.FindAllStringSubmatch(raw, -1) {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		if mo >= 1 && mo <= 12 && d >= 1 && d <= 31 {
			dates = append(dates, absDate{y, time.Month(mo), d})
		}
		stripped = strings.Replace(stripped, m[0], " ", 1)
	}
	for _, m := range absDateTRRe.FindAllStringSubmatch(raw, -1) {
		d, _ := strconv.Atoi(m[1])
		mo := absMonthsTR[strings.ToLower(m[2])]
		y := now.In(loc).Year()
		if m[3] != "" {
			y, _ = strconv.Atoi(m[3])
		}
		if mo != 0 && d >= 1 && d <= 31 {
			dates = append(dates, absDate{y, mo, d})
		}
		stripped = strings.Replace(stripped, m[0], " ", 1)
	}
	return dates, stripped
}

// extractAbsoluteWindows — SAF: mesaj → en çok 2 mutlak pencere (loc'ta).
// Tarih yoksa bugün (pencere gelecekteyse dün). Saat aralığı yoksa ve
// tarih varsa tüm gün. Bitiş ≤ başlangıç → ertesi güne sarar (22-02).
func extractAbsoluteWindows(raw string, now time.Time, loc *time.Location) ([]absWindow, bool) {
	dates, rest := absDates(raw, now, loc)
	var ranges [][4]int // h1,m1,h2,m2
	for _, m := range absHourRangeRe.FindAllStringSubmatch(rest, -1) {
		h1, _ := strconv.Atoi(m[1])
		h2, _ := strconv.Atoi(m[3])
		if h1 > 24 || h2 > 24 {
			continue
		}
		m1, m2 := 0, 0
		if m[2] != "" {
			m1, _ = strconv.Atoi(m[2])
		}
		if m[4] != "" {
			m2, _ = strconv.Atoi(m[4])
		}
		if m1 > 59 || m2 > 59 {
			continue
		}
		ranges = append(ranges, [4]int{h1, m1, h2, m2})
		if len(ranges) == 2 {
			break
		}
	}
	if len(dates) == 0 && len(ranges) == 0 {
		return nil, false
	}
	if len(dates) > 2 {
		dates = dates[:2]
	}
	today := now.In(loc)
	dateFor := func(i int) absDate {
		switch {
		case len(dates) == 0:
			return absDate{today.Year(), today.Month(), today.Day()}
		case i < len(dates):
			return dates[i]
		default:
			return dates[0]
		}
	}
	var out []absWindow
	if len(ranges) == 0 {
		for i := range dates {
			d := dates[i]
			from := time.Date(d.y, d.m, d.d, 0, 0, 0, 0, loc)
			out = append(out, absWindow{From: from, To: from.Add(24 * time.Hour)})
		}
	} else {
		for i, r := range ranges {
			d := dateFor(i)
			from := time.Date(d.y, d.m, d.d, r[0], r[1], 0, 0, loc)
			to := time.Date(d.y, d.m, d.d, r[2], r[3], 0, 0, loc)
			if !to.After(from) {
				to = to.Add(24 * time.Hour)
			}
			if len(dates) == 0 && from.After(now) {
				from, to = from.Add(-24*time.Hour), to.Add(-24*time.Hour)
			}
			out = append(out, absWindow{From: from, To: to})
		}
	}
	var kept []absWindow
	for _, w := range out {
		if d := w.To.Sub(w.From); d > 0 && d <= 24*time.Hour {
			kept = append(kept, w)
		}
	}
	if len(kept) == 0 {
		return nil, false
	}
	return kept, true
}

func absWindowLabel(w absWindow, loc *time.Location) string {
	f, t := w.From.In(loc), w.To.In(loc)
	if t.Sub(f) == 24*time.Hour && f.Hour() == 0 && f.Minute() == 0 {
		return f.Format("02/01/2006") + " (tüm gün)"
	}
	if f.Year() == t.Year() && f.YearDay() == t.YearDay() {
		return f.Format("02/01 15:04") + "–" + t.Format("15:04")
	}
	return f.Format("02/01 15:04") + "–" + t.Format("02/01 15:04")
}

// applyAbsoluteWindows — SAF: pencere(ler) rotaya/çıpaya işlenir. Tek
// pencere: rota aynı, çıpa+uzunluk pencere. İki pencere: window_compare
// (servis rotadan ya da bağlamdan; yoksa ask_service, çip mesajın pencere
// metnini taşır). Dönen etiket bağlam adımı içindir.
func applyAbsoluteWindows(route guidedRoute, wins []absWindow, ctxService string, anchorTo time.Time, rangeS int64, loc *time.Location, windowText string) (guidedRoute, time.Time, int64, string) {
	tz := loc.String()
	switch len(wins) {
	case 1:
		w := wins[0]
		return route, w.To, int64(w.To.Sub(w.From).Seconds()), "pencere: " + absWindowLabel(w, loc) + " (" + tz + ")"
	case 2:
		svc := route.Service
		if svc == "" {
			svc = ctxService
		}
		r := guidedRoute{Intent: guidedWindowCompare, Service: svc, Env: route.Env, Windows: wins, WindowText: windowText}
		if svc == "" {
			r = guidedRoute{Intent: guidedAskService, AskIntent: guidedWindowCompare, Env: route.Env, Windows: wins, WindowText: windowText}
		}
		return r, wins[1].To, int64(wins[1].To.Sub(wins[1].From).Seconds()), "kıyas: " + absWindowLabel(wins[0], loc) + " ↔ " + absWindowLabel(wins[1], loc) + " (" + tz + ")"
	}
	return route, anchorTo, rangeS, ""
}

// absWindowText — mesajdaki tarih/saat parçalarının ham metni (çipler için).
func absWindowText(raw string) string {
	var parts []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			parts = append(parts, s)
		}
	}
	for _, re := range []*regexp.Regexp{absDateSlashRe, absDateISORe, absDateTRRe} {
		for _, m := range re.FindAllString(raw, -1) {
			add(m)
		}
	}
	_, rest := absDates(raw, time.Now(), time.UTC)
	for _, m := range absHourRangeRe.FindAllString(rest, -1) {
		add(m)
	}
	return strings.Join(parts, " ile ")
}

func fmtMs(v float64) string {
	if v >= 1000 {
		return fmt.Sprintf("%.2f s", v/1000)
	}
	return fmt.Sprintf("%.0f ms", v)
}

func pctDelta(a, b float64) string {
	if a == 0 {
		if b == 0 {
			return "±0%"
		}
		return "yeni"
	}
	return fmt.Sprintf("%+.0f%%", (b-a)/a*100)
}

// renderWindowCompareTR — SAF: iki pencerenin RED'i yan yana + fark.
func renderWindowCompareTR(service string, wins []absWindow, reds []aiRED, loc *time.Location) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s — iki pencere kıyası (saatler %s):\n", service, loc.String())
	for i := range wins {
		r := reds[i]
		fmt.Fprintf(&b, "- Pencere %d %s: %d span, %.2f req/s, hata %%%.2f (%d), p50 %s, p95 %s, p99 %s\n",
			i+1, absWindowLabel(wins[i], loc), r.Spans, r.Rate, r.ErrorRate, r.ErrorCount, fmtMs(r.P50Ms), fmtMs(r.P95Ms), fmtMs(r.P99Ms))
	}
	if len(reds) == 2 {
		a, c := reds[0], reds[1]
		if a.Spans == 0 && c.Spans == 0 {
			b.WriteString("İki pencerede de span verisi yok — bunu dürüstçe söyle (saklama ufku ya da yanlış tarih olabilir).\n")
			return b.String()
		}
		fmt.Fprintf(&b, "Fark (2 − 1): trafik %s, p95 %s, p99 %s, hata oranı %.2f → %.2f puan.\n",
			pctDelta(a.Rate, c.Rate), pctDelta(a.P95Ms, c.P95Ms), pctDelta(a.P99Ms, c.P99Ms), a.ErrorRate, c.ErrorRate)
	}
	b.WriteString("Yorum: hangi pencerenin daha yavaş/hatalı olduğunu ve farkın büyüklüğünü söyle; kanıtta olmayan sayı uydurma; sayılar 5 dk ön-toplamdan (tam sayım).\n")
	return b.String()
}
