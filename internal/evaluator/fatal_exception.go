// v0.9.609 — altyapı-ölümcül exception dedektörü.
//
// Operatör isteği (prod trace'i, 2026-08-03): bir servis bağımlılığına
// ulaşamıyor, `java.net.UnknownHostException` alıyor, hedef bir
// Kubernetes servis adı. "Varsa çok kritik, hemen P1 üret."
//
// Bu dedektör paylaşılan-bağımlılık dedektöründen ÜÇ noktada ayrılıyor
// ve üçü de aynı sebepten: bu bir OLAY değil DURUM.
//
//	patlama (shared_exception)     ölümcül exception (bu dosya)
//	───────────────────────────    ────────────────────────────
//	çok servis ŞART (≥3)           TEK servis yeter
//	eşik: aynı anda N servis        eşik: 1 oluşum
//	kimlik zaman kovası taşır       kimlik (tip, servis) — kova YOK
//	kendiliğinden durabilir         biri düzeltene kadar sürer
package evaluator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

const (
	fatalExcRuleID = "exception:fatal-infrastructure"

	// fatalExcLookback — her tikte ne kadar geriye bakılır.
	// 24 saat: gece oluşan bir arıza sabah hâlâ görünsün.
	fatalExcLookback = 24 * time.Hour

	// fatalExcActiveFor — koşul "hâlâ sürüyor" sayılma süresi.
	//
	// 15 dakika, paylaşılan-patlama ile aynı. Bir DNS arızası
	// düzeldiğinde exception AKMAYI DURDURUR; son görülme bu süreden
	// eskiyse koşul bitmiştir ve problem çözülür.
	fatalExcActiveFor = 15 * time.Minute
)

// fatalExcProblemID — kararlı kimlik.
//
// Zaman kovası YOK ve bu bilinçli: aynı servis aynı ismi çözemediği
// sürece bu AYNI problemdir. Kovaya bağlasaydık, saatlerdir süren tek
// bir arıza her kovada yeni bir P1 doğururdu — operatörün gece 3'te
// göreceği son şey aynı arızanın on iki kopyası.
func fatalExcProblemID(exType, service string) string {
	return fmt.Sprintf("fatal-exc:%s:%s", exType, service)
}

// fatalExcDescription — operatörün okuyacağı cümle. SAF (tablo testli).
//
// Hedef adı mesajın İÇİNDE ve asıl eyleme dönüştüren bilgi o —
// "UnknownHostException aldı" bir gözlem, "şu adı çözemiyor" bir
// başlangıç noktası.
func fatalExcDescription(f chstore.FatalException) string {
	target := strings.TrimSpace(f.Message)
	if len(target) > 160 {
		target = string([]rune(target)[:160]) + "…"
	}
	out := fmt.Sprintf("%s servisi bir bağımlılığın adını ÇÖZEMİYOR (%s).",
		f.Service, f.Type)
	if target != "" {
		out += " Hedef: " + target + "."
	}
	out += fmt.Sprintf(" %d oluşum. Yeniden deneme bunu düzeltmez —"+
		" isim yok; yapılandırma, servis kaydı ya da DNS kontrol edilmeli.", f.Count)
	return out
}

// evaluateFatalExceptions — tik geçişi.
func (e *Evaluator) evaluateFatalExceptions(ctx context.Context, snap *chstore.OpenProblems) {
	found, err := e.store.FindFatalExceptions(ctx, fatalExcLookback)
	if err != nil {
		log.Printf("[evaluator] ölümcül exception okunamadı: %v", err)
		return
	}
	now := time.Now()
	for _, f := range found {
		e.reconcileFatalException(ctx, f, now, snap)
	}
}

func (e *Evaluator) reconcileFatalException(ctx context.Context, f chstore.FatalException, now time.Time, snap *chstore.OpenProblems) {
	id := fatalExcProblemID(f.Type, f.Service)
	existing := snap.ByID(id)
	hasOpen := existing != nil && existing.ID != ""
	active := now.Sub(time.Unix(0, f.LastSeen)) <= fatalExcActiveFor

	switch {
	case active && !hasOpen:
		p := chstore.Problem{
			ID:       id,
			RuleID:   fatalExcRuleID,
			RuleName: "Bağımlılık adı çözülemiyor",
			// DOĞDUĞU AN critical ve bu bir yan etki değil, tasarım:
			// operatörün açık isteği ("hemen P1"). Ayrıca yaş-bazlı
			// eskalasyonun bu problemi hiç ellememesini sağlıyor —
			// zaten tavanda, v0.9.585'in eskalasyon seli buradan
			// tekrar edemez.
			Severity:    "critical",
			Service:     f.Service,
			Metric:      "exception.fatal_infrastructure",
			Value:       float64(f.Count),
			Threshold:   1,
			Status:      "open",
			Description: fatalExcDescription(f),
			StartedAt:   f.FirstSeen,
		}
		if err := e.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[evaluator] ölümcül exception açılamadı %s: %v", id, err)
			return
		}
		e.countOpened()
		log.Printf("[evaluator] PROBLEM OPENED (exception.fatal_infrastructure): %s", p.Description)
		if e.notifier != nil {
			go e.notifier.SendProblemAlert(context.Background(), p)
		}

	case active && hasOpen:
		// Sürüyor: canlı sayıyı tazele, StartedAt korunur.
		existing.Value = float64(f.Count)
		existing.Description = fatalExcDescription(f)
		if err := e.store.UpsertProblem(ctx, *existing); err != nil {
			log.Printf("[evaluator] ölümcül exception tazelenemedi %s: %v", id, err)
		}

	case !active && hasOpen:
		chstore.MarkResolved(existing, time.Unix(0, f.LastSeen).UnixNano())
		if err := e.store.UpsertProblem(ctx, *existing); err != nil {
			log.Printf("[evaluator] ölümcül exception kapatılamadı %s: %v", id, err)
			return
		}
		e.countResolved()
		log.Printf("[evaluator] PROBLEM RESOLVED (exception.fatal_infrastructure): %s", id)
	}
}
