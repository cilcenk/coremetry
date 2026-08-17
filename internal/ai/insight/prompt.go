package insight

import (
	"fmt"
	"strings"
)

// prompt.go — problem kartının PROSE yarısının kullanıcı-prompt'u. Saf.
//
// SİSTEM prompt'u burada YOK ve olmayacak: o internal/copilot/prompts.go'ya
// aittir (Faz 1.6 sahiplik kararı) ve kart mevcut SystemPromptProblem'ı
// çağırır — bu dilimde yeni prompt yok.
//
// Kullanıcı yarısının burada olması, anomaly.assembleExceptionPrompt
// emsalinin aynısı: kanıtı KURAN yer onu prompt'a da yazar. Kartın
// gösterdiği sinyaller ile modelin gördüğü kanıt AYNI yapıdan türer;
// ayrı kaynaklardan türeseler, operatör kartta "deploy v5" okurken
// modelin başka bir deploy'dan bahsetmesi mümkün olurdu (v0.9.831'in
// "iki yerde stack seçimi" tuzağının prompt hâli).
//
// exception türünde bu dosya KULLANILMAZ: o girdiyi
// anomaly.BuildExceptionExplainInput kuruyor (trace + loglar + deploy
// penceresiyle) ve ikinci bir montaj yazmak o zenginliği kaybetmek olurdu.

// ProblemPromptUser — problem kanıtının prompt gövdesi. hypBlock,
// anomaly.HypothesisPromptBlockTR çıktısı (boş olabilir): deterministik
// korelasyon motorunun BİRİNCİL kanıtı, aynen taşınır.
func ProblemPromptUser(ev ProblemEvidence, hypBlock string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "PROBLEM:\n- servis: %s\n- metrik: %s\n- değer: %s (eşik %s",
		dash(ev.Service), dash(ev.Metric), fmtF(ev.Value), fmtF(ev.Threshold))
	if cmp := strings.TrimSpace(ev.Comparator); cmp != "" {
		fmt.Fprintf(&b, ", ihlal yönü %q", cmp)
	}
	b.WriteString(")\n")
	fmt.Fprintf(&b, "- şiddet: %s\n", dash(ev.Severity))
	if ev.Priority != "" {
		fmt.Fprintf(&b, "- triyaj: %s", ev.Priority)
		if ev.PriorityReason != "" {
			fmt.Fprintf(&b, " (%s)", ev.PriorityReason)
		}
		b.WriteString("\n")
	}
	if ev.StartedNs > 0 && ev.NowNs >= ev.StartedNs {
		state := "açık"
		if ev.ResolvedNs > 0 {
			state = "çözüldü"
		}
		fmt.Fprintf(&b, "- durum: %s, %s önce açıldı\n", state,
			FmtDurTR((ev.NowNs-ev.StartedNs)/1e9))
	}

	if d := ev.Deploy; d != nil && strings.TrimSpace(d.Version) != "" {
		fmt.Fprintf(&b, "\nYAKIN DEPLOY: %s, problemden %s önce",
			d.Version, FmtDurTR(d.AgeSec))
		// finite kalkanı: "+Inf%" bir kanıt değil, modele verilecek bir
		// halüsinasyon davetidir (bkz. signals.go finite gerekçesi).
		if d.HasImpact && finite(d.P99DeltaPct) && finite(d.ErrDeltaPP) {
			fmt.Fprintf(&b, " — ölçülmüş etki: p99 %+.0f%%, hata oranı %+.1f puan",
				d.P99DeltaPct, d.ErrDeltaPP)
		}
		b.WriteString("\nBu deploy zamansal olarak yakınsa kök neden adayı olarak ÖNE al; " +
			"ölçülmüş etki yoksa bunu bir varsayım olarak söyle, kanıt gibi sunma.\n")
	}

	if h := ev.Hyp; h != nil && strings.TrimSpace(h.TopSuspect) != "" && strings.TrimSpace(hypBlock) == "" {
		// hypBlock varsa aynı bilgiyi İKİ kez yazmayız (token + çelişki
		// riski); yoksa en azından çıplak adayı taşı.
		fmt.Fprintf(&b, "\nKÖK-NEDEN ADAYI (korelasyon motoru): %s", h.TopSuspect)
		if finite(h.Confidence) {
			fmt.Fprintf(&b, ", güven %%%.0f", h.Confidence*100)
		}
		b.WriteString("\n")
	}
	if s := strings.TrimSpace(hypBlock); s != "" {
		b.WriteString("\n" + s + "\n")
	}

	if bl := ev.Blast; bl != nil && bl.TotalCallers > 0 {
		fmt.Fprintf(&b, "\nETKİ ALANI: %d çağıran servis", bl.TotalCallers)
		if bl.CascadingCallers > 0 {
			fmt.Fprintf(&b, ", bunlardan %d tanesinin KENDİ açık problemi var (kaskad)",
				bl.CascadingCallers)
		}
		if len(bl.TopCallers) > 0 {
			fmt.Fprintf(&b, "; en hacimliler: %s", strings.Join(bl.TopCallers, ", "))
		}
		b.WriteString("\n")
	}

	if op := ev.SlowOp; op != nil && strings.TrimSpace(op.Name) != "" {
		fmt.Fprintf(&b, "\nEN YAVAŞ OPERASYON: %s · p95 %.0fms", op.Name, op.P95Ms)
		if op.ErrorRate > 0 {
			fmt.Fprintf(&b, " · hata oranı %%%.1f", op.ErrorRate)
		}
		b.WriteString("\n")
	}

	b.WriteString("\nYalnız YUKARIDAKİ kanıta dayan. Kanıt zayıfsa \"kanıt yetersiz\" de; " +
		"olmayan bir servis, sürüm ya da sayı UYDURMA. Cevabın sonunda operatörün " +
		"atacağı İLK adımı bir cümleyle yaz.\n")
	return b.String()
}

// ── log-pattern (v0.9.1137, Faz 2.4) ────────────────────────────────

// LogPatternPromptUser — TEK desenin kanıt paketi.
//
// Sistem prompt'u SystemPromptLogPatterns (panel anlatıcısının prompt'u)
// ve bu BİLİNÇLİ: yeni bir sistem prompt'u eklemek prompts.go'nun sicil
// kapısını (prompt_language_test.go) büyütür ve aynı işi ikinci kez
// tarif eder. Ama o prompt PANEL paketini bekliyor — "kaç yeni/patlayan
// desen", "Sürekli Gürültü" bölümü vb. Kart paketinde o bölümlerin
// kanıtı YOK; prompt'un kendi kuralı ("kanıtı olmayan bölümü tamamen
// atla") bunu çözüyor, ama tahmine bırakmıyoruz: kapsam kanıt
// gövdesinde AÇIKÇA söyleniyor.
func LogPatternPromptUser(ev LogPatternEvidence) string {
	var b strings.Builder
	b.WriteString("KAPSAM: bu paket TEK bir log desenini kapsıyor. " +
		"Şablon kataloğu (sürekli gürültü) ve diğer desenler bu pakette YOK — " +
		"o bölümleri ATLA, olmayan desen/şablon adı ANMA.\n\n")

	fmt.Fprintf(&b, "DESEN: %s\n", dash(ev.Pattern))
	if ev.WindowSec > 0 {
		fmt.Fprintf(&b, "- pencere: son %s\n", FmtDurTR(ev.WindowSec))
	}
	switch strings.ToLower(strings.TrimSpace(ev.Kind)) {
	case "new":
		fmt.Fprintf(&b, "- durum: YENİ — bu pencerede ilk kez göründü, karşılaştırılacak taban YOK "+
			"(kanıt yalnız ham sayı; oran iddiası UYDURMA)\n")
	case "spike":
		fmt.Fprintf(&b, "- durum: PATLAMA")
		if finite(ev.Ratio) && ev.Ratio > 0 {
			fmt.Fprintf(&b, " — tabanın %s katı", fmtF(ev.Ratio))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "- sayı: şimdi %s", fmtNum(ev.CurrentCount))
	if ev.BaselineCount > 0 {
		fmt.Fprintf(&b, ", pencere-eşdeğeri taban %s", fmtNum(ev.BaselineCount))
	}
	b.WriteString("\n")
	if s := strings.TrimSpace(ev.Service); s != "" {
		fmt.Fprintf(&b, "- en çok basan servis: %s\n", s)
	}
	if len(ev.TopServices) > 0 {
		b.WriteString("- servis kırılımı: ")
		parts := make([]string, 0, len(ev.TopServices))
		for _, ts := range ev.TopServices {
			if strings.TrimSpace(ts.Service) == "" {
				continue
			}
			parts = append(parts, fmt.Sprintf("%s (%s)", ts.Service, fmtNum(ts.Count)))
		}
		b.WriteString(strings.Join(parts, ", ") + "\n")
	}
	if ev.LastSeenNs > 0 && ev.NowNs >= ev.LastSeenNs {
		fmt.Fprintf(&b, "- son görülme: %s önce\n", FmtDurTR((ev.NowNs-ev.LastSeenNs)/1e9))
	}
	if s := oneLine(ev.Sample, 240); s != "" {
		fmt.Fprintf(&b, "- örnek satır: %s\n", s)
	}
	// Şiddet karışımı yok ve bu SÖYLENİYOR: söylenmezse model "çoğu ERROR
	// seviyesinde" gibi bir cümle uydurmaya davetli olur.
	b.WriteString("\nSeverity (log seviyesi) kırılımı bu pakette YOK — seviye hakkında " +
		"çıkarım YAPMA.\n")
	b.WriteString("Yalnız YUKARIDAKİ kanıta dayan. Operatörün atacağı İLK adımı " +
		"bir cümleyle yaz (hangi filtre, hangi servis).\n")
	return b.String()
}

// ── slow-query (v0.9.1137, Faz 2.4) ─────────────────────────────────

// SlowQueryPromptUser — yavaş sorgu sınıfının kanıt paketi.
//
// Etiketler İNGİLİZCE ve bu bilinçli: SystemPromptSlowQuery gövdesi
// "You receive: the normalized statement, a real sample with literals,
// the DB engine name, and the aggregate stats" diye SAYIYOR ve bugünkü
// /api/copilot/explain-slow-query gövdesi de bu etiketlerle yazılıyor.
// Cevap dili AnswerInTurkish ile sistem tarafında sabit.
//
// Eski gövdeden İKİ FARK, ikisi de kartın lehine:
//   - "Service:" tek servis yazıyordu (satırın servisi); ifade sınıfı
//     servisler ARASI, o yüzden burada çağıran KIRILIMI var.
//   - literalli örnek eskiden frontend'den geliyordu; burada MV'nin
//     kendi bucket örneği (anyMerge(sample_stmt_state)), yani kartın
//     gösterdiği sınıfla hash-tutarlı.
func SlowQueryPromptUser(ev SlowQueryEvidence) string {
	var b strings.Builder
	fmt.Fprintf(&b, "DB engine: %s\n", dash(ev.DBSystem))
	if n := strings.TrimSpace(ev.DBName); n != "" && n != "default" {
		fmt.Fprintf(&b, "Database: %s (statement grouping folds db.name — the class may span more)\n", n)
	}
	if ev.ToNs > ev.FromNs && ev.FromNs > 0 {
		fmt.Fprintf(&b, "Window: %s\n", FmtDurTR((ev.ToNs-ev.FromNs)/1e9))
	}
	fmt.Fprintf(&b, "Calls in window: %s\n", fmtNum(ev.Calls))
	if ev.Errors > 0 && ev.Calls > 0 {
		fmt.Fprintf(&b, "Errors: %s (%.1f%%)\n", fmtNum(ev.Errors),
			float64(ev.Errors)*100/float64(ev.Calls))
	}
	fmt.Fprintf(&b, "Latency: avg=%s · p95=%s · p99=%s · max=%s\n",
		fmtMs(ev.AvgMs), fmtMs(ev.P95Ms), fmtMs(ev.P99Ms), fmtMs(ev.MaxMs))
	fmt.Fprintf(&b, "Total wall-clock time spent in this query class: %s\n", fmtMs(ev.TotalMs))

	if len(ev.Callers) > 0 {
		b.WriteString("Calling services (by total time):\n")
		for i, c := range ev.Callers {
			if i >= 5 {
				fmt.Fprintf(&b, "  … (%d more not listed)\n", len(ev.Callers)-i)
				break
			}
			fmt.Fprintf(&b, "  • %s — %s calls, p95=%s, total=%s\n",
				c.Service, fmtNum(c.Calls), fmtMs(c.P95Ms), fmtMs(c.TotalMs))
		}
	}

	b.WriteString("\nNormalized statement (literals replaced with ?):\n")
	b.WriteString(capRunes(ev.Statement, 4000))
	if s := strings.TrimSpace(ev.Sample); s != "" && s != strings.TrimSpace(ev.Statement) {
		b.WriteString("\n\nReal sample with literals:\n")
		b.WriteString(capRunes(s, 4000))
	}
	b.WriteString("\n\nAnchor on the data above. Don't invent schema columns, services or " +
		"numbers you weren't shown.\n")
	return b.String()
}

// capRunes — prompt gövdesindeki SQL tavanı. RUNE sınırında keser:
// mevcut handler bayt kesiyor (api.go:9797) ve çok baytlı bir karakterin
// ortasından kesmek geçersiz UTF-8 üretip modele bozuk bayt gönderir.
func capRunes(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "… [truncated]"
	}
	return s
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
