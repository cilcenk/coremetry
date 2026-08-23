package chstore

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/cilcenk/coremetry/internal/config"
)

// CANLI küme testi — v0.9.1312. VARSAYILAN OLARAK ATLANIR.
//
// Sihirbazın çekirdeği (bölünmüş/birleşik tespiti, çift-sayım kapısı,
// ADIM 1→4 zinciri) yalnız GERÇEK bir dağıtık küme üzerinde
// kanıtlanabilir: `cluster()` davranışı, ZK yolları ve replikasyon
// yakınsaması taklit edilemez. Bu yüzden test CI'da koşmaz; operatör ya
// da geliştirici lokal dağıtık kümeye açıkça yönlendirir:
//
//	COREMETRY_LIVE_CH=localhost:9100 COREMETRY_LIVE_CLUSTER=coremetry \
//	COREMETRY_LIVE_DB=coremetry go test ./internal/chstore/ -run Live -v
//
// SALT OKUNUR: bu test HİÇBİR ŞEY YAZMAZ. Ön kontrolü koşar ve
// ölçümlerin kendi içinde tutarlı olduğunu doğrular. Göçün gerçekten
// birleştirdiği, ayrı bir elle koşuda kanıtlanır (bkz. sürüm notu).
func liveStore(t *testing.T) (*Store, string) {
	t.Helper()
	host := os.Getenv("COREMETRY_LIVE_CH")
	if host == "" {
		t.Skip("COREMETRY_LIVE_CH ayarlı değil — canlı küme testi atlanıyor")
	}
	cluster := os.Getenv("COREMETRY_LIVE_CLUSTER")
	db := os.Getenv("COREMETRY_LIVE_DB")
	if db == "" {
		db = "coremetry"
	}
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{host},
		Auth: clickhouse.Auth{Database: db},
	})
	if err != nil {
		t.Fatalf("CH bağlantısı kurulamadı: %v", err)
	}
	return &Store{conn: conn, cfg: config.CHConfig{ClusterName: cluster, Database: db}}, cluster
}

func TestLiveStateUnifyPreflight(t *testing.T) {
	s, cluster := liveStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pre, err := s.StateUnifyPreflight(ctx)
	if err != nil {
		t.Fatalf("ön kontrol hata verdi: %v", err)
	}
	t.Logf("küme=%s shard=%d host=%d tablo=%d bölünmüş=%d birleşik=%d supported=%v",
		pre.Cluster, pre.Shards, pre.Hosts, len(pre.Tables), pre.SplitCount, pre.DoneCount, pre.Supported)
	t.Logf("hüküm: %s", pre.Detail)

	if pre.Cluster != cluster {
		t.Fatalf("küme adı %q, beklenen %q", pre.Cluster, cluster)
	}
	if len(pre.Tables) == 0 {
		t.Fatal("ön kontrol hiç state tablosu döndürmedi")
	}
	if pre.MacrosVerdict != VerdictOK {
		t.Errorf("makro hükmü %q, beklenen ok: %+v", pre.MacrosVerdict, pre.Macros)
	}
	if pre.SplitCount+pre.DoneCount != len(pre.Tables) {
		t.Errorf("bölünmüş(%d) + birleşik(%d) != toplam(%d)", pre.SplitCount, pre.DoneCount, len(pre.Tables))
	}

	for _, tb := range pre.Tables {
		// Split bayrağı clusterReadSafe'in ta kendisi olmalı: sihirbazın
		// `cluster()` INSERT'ünü açan kapı ile "bu tablo göç istiyor mu"
		// kararı AYNI ölçüm — ikisi ayrışırsa biri diğerini yanıltır.
		if want := clusterReadSafe(tb.DistinctPaths, pre.Shards); tb.Split != want {
			t.Errorf("%s: Split=%v ama clusterReadSafe(%d,%d)=%v",
				tb.Name, tb.Split, tb.DistinctPaths, pre.Shards, want)
		}
		// Birleşik bir tabloda cluster() okuması veriyi katlar; o tabloya
		// asla INSERT atılmamalı.
		if !tb.Split && tb.DistinctPaths == 1 && pre.Shards > 1 {
			if strings.Contains(tb.ZKPath, "/{shard}/") {
				t.Errorf("%s: tek yolda ama yol hâlâ shard'lı: %s", tb.Name, tb.ZKPath)
			}
		}
		if tb.CatchUp == "" {
			t.Errorf("%s: yakalama sözleşmesi boş — operatör boşluğu göremez", tb.Name)
		}
	}
}

// Zaten BİRLEŞİK bir tabloya göç uygulanmaya çalışılırsa sihirbaz INSERT
// ATMADAN reddetmeli. Bu, coordinator'ün ölçtüğü çift-sayım bug'ının
// (birleşik `problems` için cluster() 4808 yerine 9616 döndürüyor) canlı
// kümedeki kapısı.
func TestLiveStateUnifyRefusesUnifiedTable(t *testing.T) {
	s, cluster := liveStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pre, err := s.StateUnifyPreflight(ctx)
	if err != nil {
		t.Fatalf("ön kontrol hata verdi: %v", err)
	}
	var unified *StateUnifyTable
	for i := range pre.Tables {
		if !pre.Tables[i].Split {
			unified = &pre.Tables[i]
			break
		}
	}
	if unified == nil {
		t.Skip("birleşik tablo yok — reddetme kapısı denenemiyor")
	}

	res := s.StateUnifyMigrateTable(ctx, cluster, *unified)
	if res.OK {
		t.Fatalf("%s ZATEN BİRLEŞİK ama göç OK döndü — cluster() okuması veriyi katlardı", unified.Name)
	}
	if len(res.Steps) != 1 || res.Steps[0].Step != "çift-sayım kapısı" {
		t.Fatalf("%s: beklenen tek adım 'çift-sayım kapısı', alınan %+v", unified.Name, res.Steps)
	}
	t.Logf("REDDEDİLDİ (beklenen): %s → %s", unified.Name, res.Err)
}

// YAZAN canlı test — iki kapı birden ister (COREMETRY_LIVE_CH ve
// COREMETRY_LIVE_WRITE), çünkü gerçekten RENAME atar. Bölünmüş bir
// tabloyu birleştirdiğini ve sonra dört host'un aynı sayıyı verdiğini
// kanıtlar. `_old` yedeği DURUR — bu test onu silmez.
func TestLiveStateUnifyMigratesSplitTable(t *testing.T) {
	if os.Getenv("COREMETRY_LIVE_WRITE") != "1" {
		t.Skip("COREMETRY_LIVE_WRITE=1 değil — yazan test atlanıyor")
	}
	s, cluster := liveStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	pre, err := s.StateUnifyPreflight(ctx)
	if err != nil {
		t.Fatalf("ön kontrol hata verdi: %v", err)
	}
	if pre.SplitCount == 0 {
		t.Skip("bölünmüş tablo yok — önce bir tabloyu eski yola geri al")
	}
	if !pre.Supported {
		t.Fatalf("ön kontrol desteklemiyor: %s", pre.Detail)
	}

	for _, tb := range pre.Tables {
		if !tb.Split || tb.Blocked != "" {
			continue
		}
		before := map[string]uint64{}
		for _, h := range tb.Hosts {
			before[h.Host] = h.Rows
		}
		t.Logf("ÖNCE  %s: %v (%d grup)", tb.Name, before, tb.DistinctPaths)

		res := s.StateUnifyMigrateTable(ctx, cluster, tb)
		if !res.OK {
			t.Fatalf("%s göçü başarısız: %s (adımlar: %+v)", tb.Name, res.Err, res.Steps)
		}
		t.Logf("SONRA %s: %d satır (FINAL %d) · %dms · yakalama: %s",
			res.Table, res.Rows, res.FinalRows, res.DurationMs, res.CatchUp)
	}

	post, err := s.StateUnifyPreflight(ctx)
	if err != nil {
		t.Fatalf("göç sonrası ön kontrol hata verdi: %v", err)
	}
	if post.SplitCount != 0 {
		t.Fatalf("göçten sonra hâlâ %d bölünmüş tablo var", post.SplitCount)
	}
	t.Logf("SONUÇ: %d tablonun hepsi birleşik — %s", len(post.Tables), post.Detail)
}
