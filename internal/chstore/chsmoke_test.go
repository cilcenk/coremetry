//go:build chsmoke

// v0.9.584 — GERÇEK ClickHouse'a karşı tip duman testi.
//
// Bu oturumda ÜÇ kez, derlenen ve tüm testlerden geçen kod prod'da
// patladı — hepsi ClickHouse tip uyuşmazlığı:
//
//	v0.9.543  struct alanı uint64, kolon UInt32   → satırlar sessizce düştü
//	v0.9.572  toStartOfInterval DÖNÜŞ tipi        → code 43
//	v0.9.578  time_bucket'a BAĞLAMA tipi          → code 53
//
// Üçünü de saf-fonksiyon testleri kaçırdı, çünkü SQL'e ve tiplere hiç
// dokunmuyorlardı. Kaynak taramaları (make audit CHECK 9, chtime_bind)
// bilinen iki deseni yakalıyor ama YENİ bir tip hatasını yakalayamaz.
//
// Bu testin fikri basit ve sınıfın tamamını kapatıyor: her okuma
// yolunu BOŞ bir ClickHouse'a karşı BİR KEZ çalıştır.
//
// SATIR DÖNMESİ GEREKMİYOR — beklenen zaten sıfır satır. Aranan tek
// şey "sorgu hatasız çalıştı mı". Tip uyuşmazlıkları, eksik kolonlar,
// yanlış bind sayısı, geçersiz fonksiyon çağrıları hepsi BURADA
// patlar; boş tablo bunların hiçbirini gizlemez.
//
// Lokal minikube bunu sağlayamıyor: şema orada da doğru ama sorgular
// elle çalıştırılmıyor. Bu yüzden CI'da.
//
// Çalıştırma:
//
//	docker run -d -p 9000:9000 clickhouse/clickhouse-server
//	CH_SMOKE_ADDR=localhost:9000 go test -tags=chsmoke ./internal/chstore/
package chstore

import (
	"bytes"
	"context"
	"log"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/config"
)

// chErrRe — ClickHouse hata imzası. Yumuşak-başarısız yollar hatayı
// DÖNDÜRMEZ, LOGLAR; imza log'da yakalanır.
var chErrRe = regexp.MustCompile(`code: \d+`)

func smokeStore(t *testing.T) *Store {
	t.Helper()
	addr := os.Getenv("CH_SMOKE_ADDR")
	if addr == "" {
		t.Skip("CH_SMOKE_ADDR verilmedi — duman testi atlanıyor")
	}
	st, err := New(config.CHConfig{
		Addr:        addr,
		Database:    "default",
		DialTimeout: "10s",
	}, config.RetentionConfig{})
	if err != nil {
		t.Fatalf("şema kurulamadı: %v", err)
	}
	return st
}

// TestCHSmokeReads — her okuma yolunu bir kez çalıştırır.
//
// Her vaka AYRI t.Run: biri patlarsa hangisi olduğu adıyla belli olur
// ve kalanlar yine koşar (ilk hatada durmak, tek turda tek hata
// bulmak demektir).
func TestCHSmokeReads(t *testing.T) {
	st := smokeStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	now := time.Now()
	from := now.Add(-time.Hour)

	cases := []struct {
		name string
		run  func() error
	}{
		// v0.9.572/578'in patladığı yol: toStartOfInterval + bind tipi.
		{"FindSharedExceptionBursts", func() error {
			_, err := st.FindSharedExceptionBursts(ctx, 24*time.Hour, 3)
			return err
		}},
		{"GetTrace", func() error {
			// Var olmayan bir trace: satır dönmez ama PENCERE ARAMASI
			// koşar — v0.9.578'in code 53 verdiği tam bu sorgu.
			_, err := st.GetTrace(ctx, "0000000000000000000000000000dead")
			return err
		}},
		{"FindTraceIDBySpan", func() error {
			_, err := st.FindTraceIDBySpan(ctx, "000000000000dead", from, now)
			return err
		}},
		// v0.9.580'in yeni yolu: attr dizisi taraması.
		{"SampleCorrelationIDs", func() error {
			_, err := st.SampleCorrelationIDs(ctx, "smoke-service", from, now)
			return err
		}},
		// v0.9.555'in dokunduğu MV okumaları.
		{"GetServiceSummary5m", func() error {
			_, err := st.GetServiceSummary5m(ctx, "smoke-service", from, now)
			return err
		}},
		{"CountServicesAgg", func() error {
			_, err := st.CountServicesAgg(ctx, from, now, "", nil)
			return err
		}},
		// v0.9.543'ün patladığı sınıf: struct alanı ↔ kolon tipi.
		{"OpenProblemsSnapshot", func() error {
			_, err := st.OpenProblemsSnapshot(ctx)
			return err
		}},
		{"ListProblems", func() error {
			_, err := st.ListProblems(ctx, ProblemFilter{Status: "open", Limit: 10})
			return err
		}},
		{"BusinessBreakdown", func() error {
			_, err := st.BusinessBreakdown(ctx, "smoke-service", "CHANNEL_CODE", from, now)
			return err
		}},
		{"ListExceptionGroups", func() error {
			_, err := st.ListExceptionGroups(ctx, ExceptionGroupFilter{Limit: 10})
			return err
		}},
		{"RouterGaps", func() error {
			_, err := st.RouterGaps(ctx, 7*24*time.Hour, 10)
			return err
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// LOG DA DENETLENİR ve bu şart. İlk taslak yalnız dönen
			// hataya bakıyordu ve v0.9.578'in bug'ını geri koyunca
			// YAKALAYAMADI: GetTrace'in pencere araması YUMUŞAK
			// başarısız oluyor — hatayı loglayıp akışa devam ediyor,
			// fonksiyon nil dönüyor.
			//
			// Yumuşak-başarısızlık bu kod tabanında bilinçli ve yaygın
			// (dayanıklılık için). Yalnız dönüş değerine bakan bir
			// duman testi, tam da en sinsi tip hatalarını kaçırır:
			// sorgu patlar, kod sessizce daha pahalı bir yola düşer.
			var logBuf bytes.Buffer
			prev := log.Writer()
			log.SetOutput(&logBuf)
			err := c.run()
			log.SetOutput(prev)

			if err != nil {
				t.Errorf("sorgu GERÇEK ClickHouse'ta hata verdi: %v\n\n"+
					"Boş tabloda satır dönmemesi NORMAL — bu hata bir TİP ya da "+
					"SÖZDİZİMİ sorunudur ve prod'da da aynen patlar. Bu oturumda "+
					"üç kez böyle bir hata canlıya çıktı (v0.9.543/572/578).", err)
			}
			if out := logBuf.String(); chErrRe.MatchString(out) {
				t.Errorf("sorgu YUMUŞAK başarısız oldu — hata döndürmedi ama "+
					"logladı:\n%s\n"+
					"Bu, en sinsi hâli: kod sessizce daha pahalı/yanlış bir yola "+
					"düşer ve dışarıdan çalışıyor görünür (v0.9.578 tam olarak "+
					"böyleydi).", strings.TrimSpace(out))
			}
		})
	}
}
