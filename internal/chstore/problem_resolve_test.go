package chstore

import (
	"testing"
	"time"
)

// v0.9.977 — KAPANIŞTA İHLAL DEĞERİ EZİLİYORDU.
//
// Altı kapanış yolu (kural, sayım-alarmı, anomali, db kapasitesi, runtime,
// SLO burn) `open.Value = <toparlanmış değer>` yazıyordu. İki sonuç:
//
//  1. Problemin NEDEN açıldığını anlatan tek sayı siliniyordu. Post-mortem'de
//     "error_rate 22, eşik 15" yerine "error_rate 3.9, eşik 15" görünüyor —
//     hiç ihlal etmemiş gibi.
//  2. Öncelik okuma anında hesaplandığı için aynı satır açılışta ve kapanışta
//     FARKLI basamak veriyordu. v0.9.976 öncesinde bu, ters-çevirme hatasıyla
//     birleşip kapanmış problemleri SAHTE P1'e çeviriyordu.
func TestMarkResolvedKeepsBreachValue(t *testing.T) {
	at := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC).UnixNano()

	cases := []struct {
		name string
		in   Problem
	}{
		{"kural yolu (error_rate 22/15)", Problem{
			Severity: "critical", Status: "open", Metric: "error_rate",
			Value: 22, Threshold: 15, Comparator: ">"}},
		{"'<' ailesi (uptime 40/99)", Problem{
			Severity: "critical", Status: "open", Metric: "uptime",
			Value: 40, Threshold: 99, Comparator: "<"}},
		{"tam kayıp (monitor DOWN 0/1)", Problem{
			Severity: "critical", Status: "open", Metric: "uptime",
			Value: 0, Threshold: 1}},
		{"anomali (current 8.4 / baseline 1.2)", Problem{
			Severity: "warning", Status: "open", Metric: "request_rate",
			Value: 8.4, Threshold: 1.2}},
		{"zaten ack'lenmiş satır", Problem{
			Severity: "critical", Status: "acknowledged", Metric: "db_p99_ms",
			Value: 9000, Threshold: 5000, Comparator: ">"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := c.in
			wantValue, wantThreshold := p.Value, p.Threshold
			MarkResolved(&p, at)

			if p.Value != wantValue {
				t.Errorf("Value = %v, %v olarak KALMALIYDI — kapanış anındaki "+
					"toparlanmış değer ihlali siler ve satır 'hiç ihlal etmemiş' "+
					"gibi görünür", p.Value, wantValue)
			}
			if p.Threshold != wantThreshold {
				t.Errorf("Threshold = %v, %v bekleniyordu", p.Threshold, wantThreshold)
			}
			if p.Status != "resolved" {
				t.Errorf("Status = %q, \"resolved\" bekleniyordu", p.Status)
			}
			if p.ResolvedAt == nil || *p.ResolvedAt != at {
				t.Errorf("ResolvedAt = %v, %v bekleniyordu", p.ResolvedAt, at)
			}
		})
	}

	// nil alıcı: çağıran tarafta FindOpenProblem nil dönebiliyor.
	MarkResolved(nil, at)
}

// TestPriorityIsSymmetricAcrossResolve — ASİMETRİ PİNİ.
//
// Aynı problem, açılışta ve kapanışta AYNI basamağı vermeli. Değer
// eziliyorken bir P1 kapanışta P2'ye (ya da v0.9.976 öncesi ters-çevirme
// ile P2 bir P1'e) dönüyordu; operatör aynı olayı iki farklı aciliyetle
// görüyordu.
//
// Bayat-critical terfisi bilinçli olarak dışarıda: o kol zaten "resolved"
// satırları hariç tutuyor (v0.9.838 tablosunda pinli), yani genç
// problemlerle ölçmek doğru karşılaştırma.
func TestPriorityIsSymmetricAcrossResolve(t *testing.T) {
	now := time.Now().UnixNano()
	fresh := now - int64(10*time.Minute)

	cases := []Problem{
		{Severity: "critical", Status: "open", Value: 45, Threshold: 15, Comparator: ">", StartedAt: fresh},
		{Severity: "critical", Status: "open", Value: 3.922, Threshold: 15, Comparator: ">", StartedAt: fresh},
		{Severity: "critical", Status: "open", Value: 40, Threshold: 99, Comparator: "<", StartedAt: fresh},
		{Severity: "critical", Status: "open", Value: 0, Threshold: 1, StartedAt: fresh},
		{Severity: "warning", Status: "open", Value: 30, Threshold: 10, Comparator: ">", StartedAt: fresh},
	}

	for _, open := range cases {
		openPri, openReason := computePriority(open, now, DefaultProblemPriority())

		closed := open
		MarkResolved(&closed, now)
		closedPri, closedReason := computePriority(closed, now, DefaultProblemPriority())

		if openPri != closedPri {
			t.Errorf("%s %v/%v (%q): açılış %s (%q), kapanış %s (%q) — aynı olay "+
				"iki farklı aciliyet gösteremez",
				open.Severity, open.Value, open.Threshold, open.Comparator,
				openPri, openReason, closedPri, closedReason)
		}
	}
}
