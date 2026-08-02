// v0.9.550 regresyon testi — evaluator kalp atışı okuması.
//
// Operatör raporu: "worker modda çalışan evaluator gerçekten problem
// bulsun ve Problems sekmesinde göreyim, bazen sanki takıldığını
// hissediyorum."
//
// Korunan sözleşme, tek cümlede: BİLMEMEK "ok" DEĞİLDİR. Bu dosyanın
// varlık sebebi, gelecekte birinin unknown/stale hallerini sadeleştirip
// "veri yoksa sağlıklı say" demesini engellemek — o sadeleştirme
// derlenir, testsiz geçer ve operatöre ölü bir evaluator'ı sağlıklı
// gösterir. Yani düzeltilen hatanın TA KENDİSİNİ geri getirir.
package api

import (
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/evaluator"
)

func TestEvaluatorHealthFrom(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	// 60 sn aralık → bayatlık eşiği 180 sn.
	const iv = int64(60_000)

	at := func(d time.Duration) int64 { return now.Add(-d).UnixNano() }

	cases := []struct {
		name    string
		hb      *evaluator.Heartbeat
		found   bool
		cacheOn bool
		want    string
	}{
		{
			name: "redis yok → unknown (asla ok)", hb: &evaluator.Heartbeat{},
			found: false, cacheOn: false, want: "unknown",
		},
		{
			name: "kalp atışı hiç yok → unknown (asla ok)", hb: &evaluator.Heartbeat{},
			found: false, cacheOn: true, want: "unknown",
		},
		{
			name: "taze + temiz → ok",
			hb: &evaluator.Heartbeat{
				StartedAt: at(35 * time.Second), FinishedAt: at(30 * time.Second),
				IntervalMS: iv, Rules: 128,
			},
			found: true, cacheOn: true, want: "ok",
		},
		{
			// Sağlıklı sistemin normali: hiç problem açılmıyor. Bu
			// TAKILMA DEĞİLDİR ve öyle raporlanmamalı — aksi halde
			// sessiz bir sistem sürekli alarm verirdi.
			name: "taze ama 0 problem → yine ok",
			hb: &evaluator.Heartbeat{
				FinishedAt: at(10 * time.Second), IntervalMS: iv,
				Rules: 40, Opened: 0, Resolved: 0,
			},
			found: true, cacheOn: true, want: "ok",
		},
		{
			name: "eşik sınırında (180sn) → henüz ok",
			hb: &evaluator.Heartbeat{
				FinishedAt: at(180 * time.Second), IntervalMS: iv, Rules: 5,
			},
			found: true, cacheOn: true, want: "ok",
		},
		{
			name: "eşiği aştı (181sn) → stale",
			hb: &evaluator.Heartbeat{
				FinishedAt: at(181 * time.Second), IntervalMS: iv, Rules: 5,
			},
			found: true, cacheOn: true, want: "stale",
		},
		{
			name: "son tik hata ile bitti → failing",
			hb: &evaluator.Heartbeat{
				FinishedAt: at(20 * time.Second), IntervalMS: iv, Err: "tik süre sınırını aştı",
			},
			found: true, cacheOn: true, want: "failing",
		},
		{
			// Hem bayat hem hatalı: bayatlık ÖNCE gelir, yoksa
			// operatör yarım saat önceki hatayı şu anki durum sanar.
			name: "bayat + hatalı → stale (hata değil)",
			hb: &evaluator.Heartbeat{
				FinishedAt: at(30 * time.Minute), IntervalMS: iv, Err: "boom",
			},
			found: true, cacheOn: true, want: "stale",
		},
		{
			// Takılmanın canlı hali: tik başladı, hiç bitmedi.
			// FinishedAt=0 iken yaş BAŞLANGIÇTAN sayılmazsa
			// time.Unix(0,0) ile 1970'e düşer — yaş 56 yıl çıkar,
			// stale görünür ama sebebi yanlış olurdu.
			name: "tik başladı bitmedi (10dk) → stale",
			hb: &evaluator.Heartbeat{
				StartedAt: at(10 * time.Minute), FinishedAt: 0, IntervalMS: iv,
			},
			found: true, cacheOn: true, want: "stale",
		},
		{
			// Bitmemiş ama HENÜZ taze bir tik: uzun sürüyor olabilir,
			// takılmış demek için erken.
			name: "tik başladı bitmedi (5sn) → ok",
			hb: &evaluator.Heartbeat{
				StartedAt: at(5 * time.Second), FinishedAt: 0, IntervalMS: iv,
			},
			found: true, cacheOn: true, want: "ok",
		},
		{
			// intervalMs=0 (eski/bozuk kayıt) → 1dk varsayılan, eşik 180sn.
			name: "intervalMs yok → 1dk varsayılan, 200sn stale",
			hb: &evaluator.Heartbeat{
				FinishedAt: at(200 * time.Second), IntervalMS: 0,
			},
			found: true, cacheOn: true, want: "stale",
		},
		{
			// Saat kayması: kalp atışı GELECEKTE damgalı. Negatif yaş
			// "-30 sn önce" gibi saçma bir metin üretmemeli.
			name: "gelecek damgası → ok, negatif yaş yok",
			hb: &evaluator.Heartbeat{
				FinishedAt: now.Add(1 * time.Minute).UnixNano(), IntervalMS: iv,
			},
			found: true, cacheOn: true, want: "ok",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := evaluatorHealthFrom(c.hb, c.found, c.cacheOn, now)
			if got.Status != c.want {
				t.Errorf("status = %q, beklenen %q (reason: %s)", got.Status, c.want, got.Reason)
			}
			if got.Reason == "" {
				t.Error("reason boş — operatör ekranda sebepsiz bir rozet görür")
			}
			if got.AgeSec < 0 && got.Status != "unknown" {
				t.Errorf("negatif yaş yalnız unknown'da olmalı, status=%s ageSec=%d",
					got.Status, got.AgeSec)
			}
		})
	}
}

// TestEvaluatorHealthUnknownIsNeverOK — sözleşmenin tek cümlesi,
// ayrı bir test olarak sabitlendi. Yukarıdaki tablo genişletilirken
// birinin unknown dalını "ok"a çevirmesi ihtimaline karşı.
func TestEvaluatorHealthUnknownIsNeverOK(t *testing.T) {
	now := time.Now()
	for _, cacheOn := range []bool{false, true} {
		got := evaluatorHealthFrom(&evaluator.Heartbeat{}, false, cacheOn, now)
		if got.Status == "ok" {
			t.Fatalf("kalp atışı yokken status=ok (cacheOn=%v) — ölçemediğimizi "+
				"sağlıklı diye raporlamak bu sürümün düzelttiği hatanın ta kendisi", cacheOn)
		}
	}
}

// TestHumanAgeTR — HER birim dalı. Değer+birim şablonlarında bir dalın
// sessizce bozulması bu repoda tekrar eden bir hata sınıfı
// (retention_test.go, fmtAgeTR); şablon tek satır bile olsa her birim
// ship anında test edilir.
func TestHumanAgeTR(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0 sn"},
		{1 * time.Second, "1 sn"},
		{59 * time.Second, "59 sn"},
		{60 * time.Second, "1 dk"},
		{90 * time.Second, "1 dk"},
		{59 * time.Minute, "59 dk"},
		{60 * time.Minute, "1 sa"},
		{23 * time.Hour, "23 sa"},
		{24 * time.Hour, "1 gün"},
		{72 * time.Hour, "3 gün"},
		{-5 * time.Second, "0 sn"}, // saat kayması
	}
	for _, c := range cases {
		if got := humanAgeTR(c.in); got != c.want {
			t.Errorf("humanAgeTR(%v) = %q, beklenen %q", c.in, got, c.want)
		}
	}
}
