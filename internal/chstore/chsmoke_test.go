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
// Aranan şey "sorgu hatasız çalıştı mı": eksik kolonlar, yanlış bind
// sayısı, geçersiz fonksiyon çağrıları, agregat dönüş tipleri hepsi
// BURADA patlar.
//
// v0.9.595 — İLK YAZIMDAKİ VARSAYIM YANLIŞTI ve düzeltildi. Başlık
// "satır dönmesi gerekmiyor, beklenen zaten sıfır satır" diyordu. Bu,
// iki okuma şeklini aynı sanıyordu:
//
//	QueryRow + agregat  → boş tabloda bile BİR satır döner, Scan koşar
//	rows.Next() döngüsü → boş tabloda hiç dönmez, Scan HİÇ KOŞMAZ
//
// Yani TARAMA yollarında struct-alanı ↔ kolon-tipi uyuşmazlığı — yani
// v0.9.543, bu testin var olma sebeplerinden biri — sessizce
// kaçıyordu. Bizzat kanıtlandı: bir sayaç alanı int'e (kolon UInt64)
// çevrildiğinde tarama yolundaki vaka YEŞİL kaldı, QueryRow yolundaki
// kardeşi anında kırmızı verdi.
//
// Çare seedSmokeRows: tarama yollarının Scan'i gerçekten çalıştırması
// için bir tohum satır. Vaka ayrıca DÖNEN SATIR SAYISINI da kontrol
// eder — "hata vermedi" ile "gerçekten tarandı" farklı şeyler.
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
	"errors"
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

// seedSmokeRows — TARAMA yollarını gerçekten çalıştırmak için tohum.
//
// v0.9.595'te bulunan KÖR NOKTA. Boş tabloya karşı koşan bu test iki
// okuma şeklini AYNI sanıyordu, oysa değiller:
//
//	QueryRow + agregat  → boş tabloda bile BİR satır döner, Scan koşar
//	rows.Next() döngüsü → boş tabloda hiç dönmez, Scan HİÇ KOŞMAZ
//
// Yani tarama-tabanlı okumalarda tip uyuşmazlığı — bu testin var olma
// sebebi olan hata sınıfı — sessizce kaçıyordu. Bizzat kanıtlandı:
// ConfirmedRCASignatures'ın sayaç alanı int'e (kolon UInt64)
// çevrildiğinde test YEŞİL kaldı; aynı bozma QueryRow yolundaki
// kardeşinde anında kırmızı verdi.
//
// Bir satır yeter: Scan'in koşması için gereken tek şey o.
func seedSmokeRows(t *testing.T, st *Store, now time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := st.UpsertRCAVerdict(ctx, RCAVerdictRecord{
		ExchangeID: "smoke-ex-1", AnchorKind: "problem", AnchorID: "smoke-p-1",
		Service: "smoke-service", Verdict: "root_cause_identified",
		RCEntity: "smoke-db", RCFailMode: "bağlantı havuzu tükenmesi",
		Confidence: 0.5, ModelConf: 0.8, HypoConf: 0.4, HypoVersion: 1,
		Parsed: true, ShieldNotes: []string{"güven tavanlandı"},
		CreatedAt: now.UnixNano(),
	}); err != nil {
		t.Fatalf("tohum verdict yazılamadı: %v", err)
	}
	// 👍 — ConfirmedRCASignatures'ın INNER JOIN'i buna bağlı; onsuz
	// sorgu koşar ama sıfır satır döner ve Scan yine çalışmazdı.
	if err := st.UpsertAIFeedback(ctx, AIFeedback{
		ExchangeID: "smoke-ex-1", Surface: "rootcause-verdict", Verdict: 1,
		CreatedAt: now.UnixNano(),
	}); err != nil {
		t.Fatalf("tohum geri bildirim yazılamadı: %v", err)
	}
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
	seedSmokeRows(t, st, now)

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
		// v0.9.595 — iki yeni okuma da AYNI sınıftan risk taşıyor:
		// count()/countIf()/uniqExact() UInt64 döner ve struct alanı
		// int olsaydı DERLENİR, tüm testlerden GEÇER, yalnız burada
		// patlardı (v0.9.543'ün birebir tekrarı). Ayrıca ikisi de
		// JOIN'li — join tarafındaki tip/kolon hatası da burada çıkar.
		{"RCAVerdictQualityStats", func() error {
			_, err := st.RCAVerdictQualityStats(ctx, from, now)
			return err
		}},
		{"ConfirmedRCASignatures", func() error {
			sigs, err := st.ConfirmedRCASignatures(ctx, "smoke-service", now)
			if err != nil {
				return err
			}
			if len(sigs) == 0 {
				return errors.New("tohum satır tarandı sayılmadı — Scan hiç " +
					"koşmadı, yani tip uyuşmazlığı bu testten SESSİZCE geçerdi")
			}
			return nil
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
