package chstore

import (
	"testing"
	"time"
)

// v0.10.126 — perf bütçesi P2: ham /api/traces yolu 24h pencereyi tam
// tarıyordu (1.54M satır → 50 satır, p50 4.19 s). Yenilik dilimi: zaman-
// DESC listede önce dar kuyruk penceresi; K = offset+limit+1 satırın hepsi
// floor'un üstünde başlamışsa sayfa tam taramayla aynı. Bu testler saf
// karar noktalarını çiviler: hangi basamaklar, kim uygun, sayfa ne zaman
// kabul edilir. Çalışma-zamanı kanıtı: query_log read_rows (release notu).

func TestTraceRawProbeWindows(t *testing.T) {
	t0 := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		win  time.Duration
		want []time.Duration
	}{
		{"1h — basamak yok, tam tarama zaten dar", time.Hour, nil},
		{"2h — (1h+1h)*2 > 2h, yok", 2 * time.Hour, nil},
		{"4h — yalnız 1h", 4 * time.Hour, []time.Duration{time.Hour}},
		{"24h — 1h, 6h (24h basamağı sığmaz)", 24 * time.Hour, []time.Duration{time.Hour, 6 * time.Hour}},
		{"7d — üçü de", 7 * 24 * time.Hour, []time.Duration{time.Hour, 6 * time.Hour, 24 * time.Hour}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := traceRawProbeWindows(t0.Add(-c.win), t0)
			if len(got) != len(c.want) {
				t.Fatalf("basamaklar %v, beklenen %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("basamak %d: %v, beklenen %v", i, got[i], c.want[i])
				}
			}
		})
	}
	if traceRawProbeWindows(time.Time{}, t0) != nil || traceRawProbeWindows(t0, t0) != nil {
		t.Fatal("sıfır/ters pencere basamak üretmemeli")
	}
}

func TestTraceRawProbeEligible(t *testing.T) {
	t0 := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	base := TraceFilter{From: t0.Add(-24 * time.Hour), To: t0, Limit: 50}
	cases := []struct {
		name string
		mut  func(f *TraceFilter)
		want bool
	}{
		{"varsayılan (zaman DESC, 24h, 50)", func(f *TraceFilter) {}, true},
		{"sort=time açık", func(f *TraceFilter) { f.Sort = "time" }, true},
		{"sort=duration — tüm pencere gerekir", func(f *TraceFilter) { f.Sort = "duration" }, false},
		{"order=asc — dilim kuyrukta değil başta", func(f *TraceFilter) { f.Order = "asc" }, false},
		{"tek trace id — zaten sınırlı", func(f *TraceFilter) { f.TraceID = "0123456789abcdef0123456789abcdef" }, false},
		{"id listesi — zaten sınırlı", func(f *TraceFilter) { f.TraceIDs = []string{"a"} }, false},
		{"CSV dışa aktarım (50k) — basamak boşuna", func(f *TraceFilter) { f.Limit = 50000 }, false},
		{"limit 500 — sınırın kendisi uygun", func(f *TraceFilter) { f.Limit = 500 }, true},
		{"2h pencere — basamak sığmaz", func(f *TraceFilter) { f.From = t0.Add(-2 * time.Hour) }, false},
		{"offset 200 — sayfa 5 uygun", func(f *TraceFilter) { f.Offset = 200 }, true},
		{"attribute filtresi (P2 senaryosu) uygun", func(f *TraceFilter) {
			f.Filters = []FilterExpr{{Key: "user_agent.original", Op: "LIKE", Values: []string{"%"}}}
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := base
			c.mut(&f)
			if got := traceRawProbeEligible(f); got != c.want {
				t.Fatalf("uygunluk %v, beklenen %v", got, c.want)
			}
		})
	}
}

func TestTraceRawProbePage(t *testing.T) {
	floor := time.Date(2026, 8, 28, 11, 0, 0, 0, time.UTC)
	fl := floor.UnixNano()
	// DESC sıralı: floor'un üstünde n kesin satır, ardından şüpheliler.
	mk := func(exact, suspect int) []TraceRow {
		var rows []TraceRow
		for i := 0; i < exact; i++ {
			rows = append(rows, TraceRow{TraceID: "e", StartTime: fl + int64(exact-i)*int64(time.Minute)})
		}
		for i := 0; i < suspect; i++ {
			rows = append(rows, TraceRow{TraceID: "s", StartTime: fl - int64(i+1)*int64(time.Minute)})
		}
		return rows
	}
	cases := []struct {
		name          string
		rows          []TraceRow
		offset, limit int
		wantOK        bool
		wantLen       int
		wantMore      bool
	}{
		{"K kesin satır → sayfa + hasMore", mk(51, 0), 0, 50, true, 50, true},
		{"K'dan az satır → genişle", mk(30, 0), 0, 50, false, 0, false},
		{"K içinde şüpheli → genişle (kısmi toplam sayfaya giremez)", mk(50, 1), 0, 50, false, 0, false},
		{"şüpheliler K'nın dışında → kabul", mk(51, 10), 0, 50, true, 50, true},
		{"offset 50, 101 kesin → 2. sayfa", mk(101, 0), 50, 50, true, 50, true},
		{"offset 50, 100 kesin → K=101 yok, genişle", mk(100, 0), 50, 50, false, 0, false},
		{"limit 0 → uygun değil", mk(5, 0), 0, 0, false, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			page, more, ok := traceRawProbePage(c.rows, fl, c.offset, c.limit)
			if ok != c.wantOK {
				t.Fatalf("ok %v, beklenen %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if len(page) != c.wantLen || more != c.wantMore {
				t.Fatalf("len %d more %v, beklenen %d/%v", len(page), more, c.wantLen, c.wantMore)
			}
			// Sayfa yalnız kesin satırlardan oluşur ve DESC sırayı korur.
			for i, r := range page {
				if r.StartTime < fl {
					t.Fatalf("satır %d floor'un altında başlıyor — kısmi toplam sızdı", i)
				}
				if i > 0 && r.StartTime > page[i-1].StartTime {
					t.Fatalf("satır %d sıra bozuk", i)
				}
			}
			// 2. sayfa gerçekten offset'ten başlar: ilk satır 51. kesin satır.
			if c.offset > 0 && page[0].StartTime != c.rows[c.offset].StartTime {
				t.Fatal("offset uygulanmadı")
			}
		})
	}
}
