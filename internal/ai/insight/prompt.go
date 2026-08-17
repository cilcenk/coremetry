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

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
