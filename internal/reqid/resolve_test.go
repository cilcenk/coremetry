package reqid

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// resolve_test.go — v0.9.1142. Çözümleyicinin sözleşmesi: TEK arama, TEK
// pencere, bulunamamak HATA DEĞİL.
//
// Store arayüzü GÖMÜLÜ (api/logs_degrade_test.go emsali): yalnız Search
// uygulanıyor, başka bir metodu çağırmak nil arayüz üzerinden panikler —
// yani çözümleyici sessizce ikinci bir okuma eklerse test patlar.
type searchStub struct {
	logstore.Store
	page  *logstore.Page
	err   error
	seen  logstore.Filter
	calls int
}

func (s *searchStub) Search(_ context.Context, f logstore.Filter) (*logstore.Page, error) {
	s.calls++
	s.seen = f
	return s.page, s.err
}

func rec(trace, span, svc string, tsNs int64) *logstore.LogRecord {
	return &logstore.LogRecord{TraceID: trace, SpanID: span, ServiceName: svc, Timestamp: tsNs}
}

func TestResolveWindowAndFilter(t *testing.T) {
	id, ok := Parse(validID(), Location(""))
	if !ok {
		t.Fatal("sentetik kimlik ayrıştırılamadı")
	}
	st := &searchStub{page: &logstore.Page{Logs: []*logstore.LogRecord{
		rec("", "", "svc-a", 0),
		rec("9fc37145182089354c2c20a1c63e0817", "00f067aa0ba902b7", "svc-a", 42),
	}}}
	res, err := Resolve(context.Background(), st, id)
	if err != nil {
		t.Fatalf("hata: %v", err)
	}
	if st.calls != 1 {
		t.Fatalf("%d arama yapıldı — tek arama, tek pencere", st.calls)
	}
	if st.seen.Search != id.Raw {
		t.Fatalf("aranan metin %q — kimliğin ORİJİNAL token'ı olmalı", st.seen.Search)
	}
	wantFrom, wantTo := id.Window()
	if !st.seen.From.Equal(wantFrom) || !st.seen.To.Equal(wantTo) {
		t.Fatalf("pencere %s → %s, beklenen %s → %s",
			FmtLocal(st.seen.From), FmtLocal(st.seen.To), FmtLocal(wantFrom), FmtLocal(wantTo))
	}
	if st.seen.Limit != SearchLimit || st.seen.Limit > 20 {
		t.Fatalf("LIMIT %d — küçük ve sabit olmalı", st.seen.Limit)
	}
	// v0.9.1084 dersi: hasTrace filtresi ES mapping'inde yapısal trace
	// alanı yoksa HER ŞEYİ eler; burada kullanmak "kimlik yok" yalanı olurdu.
	if st.seen.HasTrace {
		t.Error("HasTrace filtresi kullanılmış — v0.9.1084 sınıfı sessiz boş sonuç")
	}
	// PIT disiplini (v0.9.286): imleç istemeyen okuma segment tutmaz.
	if st.seen.WantCursor {
		t.Error("WantCursor true — imleç kullanılmıyor, PIT tutturmak boşuna maliyet")
	}
	if res.TraceID != "9fc37145182089354c2c20a1c63e0817" || res.SpanID != "00f067aa0ba902b7" {
		t.Fatalf("trace bağlamı seçilmedi: %+v", res)
	}
	if res.Service != "svc-a" || res.LogTS != 42 {
		t.Fatalf("eşleşen satırın kanıtı taşınmadı: %+v", res)
	}
	if res.MatchedLogs != 2 || res.DistinctTraces != 1 {
		t.Fatalf("sayımlar: eşleşen=%d farklı-trace=%d", res.MatchedLogs, res.DistinctTraces)
	}
}

func TestResolveHonesty(t *testing.T) {
	id, _ := Parse(validID(), Location(""))

	t.Run("eşleşme yok → bulunamadı, hata DEĞİL", func(t *testing.T) {
		st := &searchStub{page: &logstore.Page{}}
		res, err := Resolve(context.Background(), st, id)
		if err != nil {
			t.Fatalf("bulunamamak hata döndürdü: %v", err)
		}
		if res.TraceID != "" || res.MatchedLogs != 0 {
			t.Fatalf("boş sonuç değil: %+v", res)
		}
	})

	t.Run("trace bağlamı olmayan satırlar → bulunamadı", func(t *testing.T) {
		st := &searchStub{page: &logstore.Page{Logs: []*logstore.LogRecord{rec("", "", "svc", 1)}}}
		res, _ := Resolve(context.Background(), st, id)
		if res.TraceID != "" {
			t.Fatalf("trace uydurdu: %+v", res)
		}
		if res.MatchedLogs != 1 {
			t.Fatalf("eşleşen satır sayısı kayboldu: %+v", res)
		}
	})

	t.Run("birden çok farklı trace sayılır", func(t *testing.T) {
		st := &searchStub{page: &logstore.Page{Logs: []*logstore.LogRecord{
			rec("aaaa7145182089354c2c20a1c63e0817", "1111111111111111", "svc", 2),
			rec("bbbb7145182089354c2c20a1c63e0817", "2222222222222222", "svc", 3),
		}}}
		res, _ := Resolve(context.Background(), st, id)
		if res.DistinctTraces != 2 {
			t.Fatalf("farklı trace sayısı %d — tek trace gibi sunulamaz", res.DistinctTraces)
		}
		if res.TraceID != "aaaa7145182089354c2c20a1c63e0817" {
			t.Fatalf("ilk trace seçilmedi: %q", res.TraceID)
		}
	})

	t.Run("kısmi cevap taşınır", func(t *testing.T) {
		st := &searchStub{page: &logstore.Page{Partial: true}}
		res, _ := Resolve(context.Background(), st, id)
		if !res.Partial {
			t.Error("logstore Partial bayrağı düştü — 'bulunamadı' kesin yokluk sanılır")
		}
	})

	t.Run("store hatası yükselir", func(t *testing.T) {
		st := &searchStub{err: errors.New("boom")}
		if _, err := Resolve(context.Background(), st, id); err == nil {
			t.Error("store hatası yutuldu")
		}
	})

	t.Run("nil store panik etmez", func(t *testing.T) {
		if _, err := Resolve(context.Background(), nil, id); err != nil {
			t.Errorf("nil store hata döndürdü: %v", err)
		}
	})
}

func TestFmtLocalIs24Hour(t *testing.T) {
	ts := time.Date(2026, 8, 17, 21, 5, 3, 40*int(time.Millisecond), Location(""))
	got := FmtLocal(ts)
	if got != "2026-08-17 21:05:03.040 +03:00" {
		t.Fatalf("damga %q — 24 saat + ofset konvansiyonu (v0.9.879)", got)
	}
}
