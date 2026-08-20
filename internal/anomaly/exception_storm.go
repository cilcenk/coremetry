package anomaly

// exception_storm.go — "aynı anda birden çok servis patladı" dedektörü
// (v0.9.1194, operatör-bildirimli + spec onayı).
//
// Bildirilen olay (2026-08-20): 25 saniyede DOKUZ farklı servisten yeni
// exception grubu — her biri 1-7 olaylık, hiçbiri tek başına hiçbir
// önceliği hak etmiyor. Öncelik hep tek grubun KENDİ şiddetinden
// türüyordu; oysa dokuz servisin eş-zamanlı patlaması, tek servisin 888
// hatasından daha güçlü bir olay sinyali (tipik kök: paylaşılan bağımlılık
// — DB/ağ/DNS). Bu dosya o sinyali tek bir P1 Problem satırına bağlar;
// satır normal bildirim hattına düşer (spec sorusu 2: evet).
//
// Yaşam döngüsü anomali dedektörünün applyOutcome şekli: sabit ruleID,
// açıkken tazele (tam-satır replace), koşul geçince MarkResolved. Bildirim
// yalnız AÇILIŞTA — tazelemede spam olmaz.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// exceptionStormRuleID — fırtına probleminin sabit kimliği. Servis alanı
// boş (filo-düzeyi olay); aynı anda EN FAZLA BİR fırtına satırı yaşar —
// süren bir fırtına büyürken satır güncellenir, ikinci satır açılmaz.
const exceptionStormRuleID = "exception-storm"

// stormDescription — problemin operatöre görünen gövdesi. SAF, tablo
// testli. En çok grup açan 8 servis adlandırılır; kalanı sayıyla itiraf
// edilir ("+N servis daha") — sessiz kırpma "hepsi bu" diye okunur.
func stormDescription(cands []chstore.ExceptionStormCandidate, window time.Duration, minServices int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Exception fırtınası: son %s içinde %d farklı servis yeni exception grubu açtı (eşik %d) — muhtemel ortak bağımlılık (DB/ağ/DNS).",
		shortWindow(window), len(cands), minServices)
	n := len(cands)
	show := n
	if show > 8 {
		show = 8
	}
	parts := make([]string, 0, show)
	for _, c := range cands[:show] {
		parts = append(parts, fmt.Sprintf("%s (%d grup, %d olay)", c.Service, c.Groups, c.Occurrences))
	}
	b.WriteString(" ")
	b.WriteString(strings.Join(parts, " · "))
	if n > show {
		fmt.Fprintf(&b, " · +%d servis daha", n-show)
	}
	return b.String()
}

// shortWindow — "10dk" / "1sa 30dk". Gerekçe metni pencereyi SÖYLER,
// sabitlemez: vida değişince cümle yalan olamaz (v0.9.775 kuralı).
func shortWindow(d time.Duration) string {
	m := int(d.Minutes())
	if m < 60 {
		return fmt.Sprintf("%ddk", m)
	}
	if m%60 == 0 {
		return fmt.Sprintf("%dsa", m/60)
	}
	return fmt.Sprintf("%dsa %ddk", m/60, m%60)
}

// checkExceptionStorm — tik başına bir kez, scan()'in sonunda (davranış
// motoruyla aynı gerekçe: ani-sapma hattının gecikmesi buna bağlanmaz;
// her dal soft-fail).
func (d *Detector) checkExceptionStorm(ctx context.Context) {
	cfg := d.store.GetExceptionTriage(ctx) // soft-fail: CH tökezlerse varsayılanlar
	window := cfg.StormWindow()
	minSvc := chstore.NormalizeExceptionTriage(cfg).StormMinServices

	cands, err := d.store.RecentNewExceptionServices(ctx, time.Now().Add(-window))
	if err != nil {
		log.Printf("[exception-storm] okuma düştü (tik atlandı): %v", err)
		return
	}
	open, err := d.store.FindOpenProblem(ctx, exceptionStormRuleID, "")
	if err != nil {
		log.Printf("[exception-storm] açık problem okunamadı (tik atlandı): %v", err)
		return
	}
	hasOpen := open != nil && open.ID != ""

	if len(cands) >= minSvc {
		desc := stormDescription(cands, window, minSvc)
		if hasOpen {
			// Tam-satır replace: TÜM alanlar taşınır (ev kuralı). Value
			// büyüyen fırtınayı izler; StartedAt İLK tespitte kalır —
			// stale-critical saati fırtınanın başından işler.
			open.Value = float64(len(cands))
			open.Threshold = float64(minSvc)
			open.Description = desc
			if err := d.store.UpsertProblem(ctx, *open); err != nil {
				log.Printf("[exception-storm] refresh: %v", err)
			}
			return
		}
		p := chstore.Problem{
			ID:          newID(),
			RuleID:      exceptionStormRuleID,
			RuleName:    "Exception fırtınası",
			Severity:    "critical",
			Service:     "", // filo-düzeyi: tek bir servise iliştirmek kökü gizlerdi
			Metric:      "exception_storm",
			Value:       float64(len(cands)),
			Threshold:   float64(minSvc),
			Comparator:  ">",
			Status:      "open",
			Description: desc,
			StartedAt:   time.Now().UnixNano(),
		}
		if err := d.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[exception-storm] open: %v", err)
			return
		}
		log.Printf("[exception-storm] AÇILDI: %d servis / %s pencere (eşik %d)",
			len(cands), shortWindow(window), minSvc)
		// Spec sorusu 2 (operatör: "düşsün"): fırtına critical bildirim
		// kanallarına normal Problem gibi gider. Yalnız açılışta — tazeleme
		// bildirimi spam olurdu.
		if d.notifier != nil {
			go d.notifier.SendProblemAlert(context.Background(), p)
		}
		return
	}

	// Koşul geçti: pencere kaydıkça yeni grup akışı eşiğin altına indi.
	if hasOpen {
		open.Description = strings.TrimRight(open.Description, " ") +
			fmt.Sprintf(" Çözüldü: son %s içinde yeni grup açan servis sayısı eşiğin altına indi (%d/%d).",
				shortWindow(window), len(cands), minSvc)
		chstore.MarkResolved(open, time.Now().UnixNano())
		if err := d.store.UpsertProblem(ctx, *open); err != nil {
			log.Printf("[exception-storm] resolve: %v", err)
			return
		}
		log.Printf("[exception-storm] ÇÖZÜLDÜ (%d/%d)", len(cands), minSvc)
	}
}
