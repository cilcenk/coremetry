// v0.9.572 — paylaşılan bağımlılık patlaması dedektörü.
//
// Operatör raporu (prod, gece 03:04): on beşten fazla servis AYNI
// saniyede aynı `java.sql.SQLRecoverableException` ORA-18730 hatasını
// aldı. Coremetry bunları on beş ayrı exception grubu olarak gösterdi —
// her biri "şu servisin sorunu" gibi. Oysa bu paylaşılan bir
// bağımlılıkta (Oracle) TEK bir olaydı; ortak paydayı operatörün
// kafasında kurması gerekiyordu.
//
// Bu dedektör onu Coremetry'nin kurmasını sağlıyor: aynı exception
// tipi, dar bir pencerede, çok sayıda ayrı serviste başlıyorsa tek bir
// Problem açılır. Tespit DETERMİNİSTİK — LLM yok. RCA paketinin
// [2] CORRELATE katmanı: model tespit etmez, tespit edileni anlatır.
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
	// sharedBurstRuleID — problems.rule_id. Sabit: tahliye/filtreleme
	// bu kimlikten geçiyor.
	sharedBurstRuleID = "exception:shared-dependency"

	// sharedBurstLookback — her tikte ne kadar geriye bakılır.
	//
	// 24 saat: operatörün vakası GECE oldu ve sabah fark edildi. Kısa
	// bir pencere, tam da kaçırılan olayı yeniden kaçırırdı.
	sharedBurstLookback = 24 * time.Hour

	// sharedBurstActiveFor — patlama "hâlâ sürüyor" sayılma süresi.
	//
	// Bu bir EŞİK İHLALİ değil, bir OLAY: sona erdiğinde kendiliğinden
	// düzelmez, sadece durur. Son görülme bu süreden eskiyse olay
	// bitmiştir ve problem çözülür — aksi halde açık kalır, yaş-bazlı
	// eskalasyonla P1'e çıkar ve gece olmuş bir şey sabah "kritik"
	// diye bağırır.
	sharedBurstActiveFor = 15 * time.Minute

	// sharedBurstMaxServicesInText — açıklamada kaç servis adı geçer.
	// Kalanı "+N daha" olur; on beş servis adı tek satıra sığmaz ve
	// pager'da okunmaz.
	sharedBurstMaxServicesInText = 6
)

// sharedBurstProblemID — patlamanın kararlı kimliği.
//
// Tip + zaman kovası: aynı tip, farklı gecelerde AYRI olaydır ve ayrı
// problem açmalı. Servis listesi kimliğe GİRMEZ — patlama büyüdükçe
// yeni servisler eklenir ve liste kimliğin parçası olsaydı her yeni
// servis yeni bir problem doğururdu (tam da düzeltmeye çalıştığımız
// dağınıklık).
func sharedBurstProblemID(exType string, bucketNs int64) string {
	return fmt.Sprintf("shared-exc:%s:%d", exType, bucketNs)
}

// sharedBurstDescription — operatörün okuyacağı tek cümle.
//
// SAF (tablo testli). Servis sayısı ÖNCE gelir çünkü asıl bulgu o:
// "bu bir servisin sorunu değil, N servisi birden vurdu".
func sharedBurstDescription(b chstore.SharedExceptionBurst) string {
	shown := b.Services
	extra := 0
	if len(shown) > sharedBurstMaxServicesInText {
		extra = len(shown) - sharedBurstMaxServicesInText
		shown = shown[:sharedBurstMaxServicesInText]
	}
	list := strings.Join(shown, ", ")
	if extra > 0 {
		list += fmt.Sprintf(" +%d daha", extra)
	}
	msg := strings.TrimSpace(b.Message)
	if len(msg) > 120 {
		msg = string([]rune(msg)[:120]) + "…"
	}
	out := fmt.Sprintf("%d servis aynı anda %s aldı (%s) — paylaşılan bir bağımlılık olayı gibi görünüyor. Toplam %d oluşum.",
		len(b.Services), b.Type, list, b.Occurrences)
	if msg != "" {
		out += " Örnek: " + msg
	}
	return out
}

// sharedBurstSeverity — patlamanın ciddiyeti.
//
// Servis sayısına bağlı, oluşum sayısına DEĞİL: paylaşılan bir
// bağımlılık arızasının ağırlığı KAÇ SERVİSİ vurduğudur. Tek serviste
// 10.000 oluşum o servisin sorunu; on serviste 10'ar oluşum altyapı
// sorunu. SAF (tablo testli).
func sharedBurstSeverity(serviceCount int) string {
	if serviceCount >= 8 {
		return "critical"
	}
	return "warning"
}

// evaluateSharedExceptionBursts — tik geçişi.
func (e *Evaluator) evaluateSharedExceptionBursts(ctx context.Context, snap map[string]*chstore.Problem) {
	bursts, err := e.store.FindSharedExceptionBursts(ctx, sharedBurstLookback, 0)
	if err != nil {
		log.Printf("[evaluator] paylaşılan exception patlaması okunamadı: %v", err)
		return
	}
	now := time.Now()
	for _, b := range bursts {
		e.reconcileSharedBurst(ctx, b, now, snap)
	}
}

func (e *Evaluator) reconcileSharedBurst(ctx context.Context, b chstore.SharedExceptionBurst, now time.Time, snap map[string]*chstore.Problem) {
	id := sharedBurstProblemID(b.Type, b.BucketStart)
	existing := snap[id]
	hasOpen := existing != nil && existing.ID != ""
	active := now.Sub(time.Unix(0, b.LastSeen)) <= sharedBurstActiveFor

	switch {
	case active && !hasOpen:
		p := chstore.Problem{
			ID:       id,
			RuleID:   sharedBurstRuleID,
			RuleName: "Paylaşılan bağımlılık · eşzamanlı exception",
			Severity: sharedBurstSeverity(len(b.Services)),
			// Service BOŞ bırakılır ve bu bilinçli: olay TEK bir
			// servise ait değil. Birini seçmek, tam da düzeltmeye
			// çalıştığımız yanlış atfı yapmak olurdu.
			Service:     "",
			Metric:      "exception.shared_dependency",
			Value:       float64(len(b.Services)),
			Threshold:   float64(sharedBurstMinServicesEffective()),
			Status:      "open",
			Description: sharedBurstDescription(b),
			StartedAt:   b.FirstSeen,
		}
		if err := e.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[evaluator] paylaşılan patlama açılamadı %s: %v", id, err)
			return
		}
		e.countOpened()
		log.Printf("[evaluator] PROBLEM OPENED (exception.shared_dependency): %s", p.Description)
		if e.notifier != nil {
			go e.notifier.SendProblemAlert(context.Background(), p)
		}

	case active && hasOpen:
		// Patlama büyüyor olabilir (yeni servisler katılıyor). Canlı
		// değerleri tazele; StartedAt korunur.
		existing.Severity = sharedBurstSeverity(len(b.Services))
		existing.Value = float64(len(b.Services))
		existing.Description = sharedBurstDescription(b)
		if err := e.store.UpsertProblem(ctx, *existing); err != nil {
			log.Printf("[evaluator] paylaşılan patlama tazelenemedi %s: %v", id, err)
		}

	case !active && hasOpen:
		resolvedAt := time.Unix(0, b.LastSeen).UnixNano()
		existing.Status = "resolved"
		existing.ResolvedAt = &resolvedAt
		if err := e.store.UpsertProblem(ctx, *existing); err != nil {
			log.Printf("[evaluator] paylaşılan patlama kapatılamadı %s: %v", id, err)
			return
		}
		e.countResolved()
		log.Printf("[evaluator] PROBLEM RESOLVED (exception.shared_dependency): %s", id)
	}
}

// sharedBurstMinServicesEffective — eşiği tek yerden okunur kılar
// (Problem.Threshold operatöre "kaçtan sonra" olduğunu söylüyor).
func sharedBurstMinServicesEffective() int { return 3 }
