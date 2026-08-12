// v0.9.985 — Distributed spool sağlık sinyalinin regresyon testleri.
//
// ORİJİNAL SEMPTOM (lokal küme, 2026-08-12): `spans_local`'a son yazım
// 08:22/08:49 iken CH saati 12:02 — üç buçuk saat boyunca İKİ shard'a da
// tek satır inmedi. Aynı anda /api/health: clickhouse "ok",
// spans_write_failed 0, spans_queued 0, spans_accepted 109814 ve
// tırmanıyor. Distributed motoru INSERT'i diske spool'layıp hemen OK
// döndüğü için uygulama katmanının HİÇBİR sayacı arızayı göremiyordu.
//
// Buradaki testler üç şeyi çiviliyor:
//  1. Saf verdict'in tam kararı (özellikle "artıyor → degraded" dalı,
//     arızayı yakalayan tek dal).
//  2. Kümülatif error_count'ın tek başına degraded ÜRETMEMESİ — üç gün
//     önceki bir blip sinyali kalıcı olarak yakardı.
//  3. Tek-düğümde HİÇ SORGU ÇALIŞMAMASI (SQL "" döner) — dağıtık olmayan
//     kurulumda davranış bayt-bayt değişmemeli.
package chstore

import (
	"strings"
	"testing"
)

// measured — Measured=true bir örnek kurar (probe koştu, sonuç okundu).
func measured(files, errs uint64, tables ...DistributionQueueEntry) *DistributionQueue {
	q := &DistributionQueue{Measured: true, Files: files, ErrorCount: errs, Tables: tables}
	return q
}

func TestDistributionVerdict(t *testing.T) {
	spans := DistributionQueueEntry{Table: "spans", Files: 12702, ErrorCount: 1145,
		LastError: "Code: 241. DB::Exception: Memory limit (total) exceeded"}
	metrics := DistributionQueueEntry{Table: "metric_points", Files: 26132, ErrorCount: 298}

	cases := []struct {
		name         string
		cur, prev    *DistributionQueue
		wantDegraded bool
		// wantDetail: "" = neden metni de BOŞ olmalı (sinyal yok).
		// Aksi hâlde detail'in içermesi gereken ayırt edici parça.
		wantDetail string
	}{
		// ── Sinyal ÜRETİLMEYEN hâller ────────────────────────────────
		{
			// Tek düğüm: CollectDistributionQueue nil döner (sorgu YOK).
			name: "tek düğüm — nil girdi, sinyal yok",
			cur:  nil, prev: nil,
			wantDegraded: false, wantDetail: "",
		},
		{
			// Fail-open dersi (v0.9.984): "ölçemedim" ≠ "temiz". Probe
			// düşmüşken degraded İDDİA ETME ama "ok" diye de sayma —
			// verdict sessiz kalır, gövdedeki measured=false konuşur.
			name: "probe düştü — teşhis uydurma",
			cur:  &DistributionQueue{Measured: false, ProbeError: "timeout"}, prev: nil,
			wantDegraded: false, wantDetail: "",
		},
		{
			name: "kuyruk boş",
			cur:  measured(0, 0), prev: measured(0, 0),
			wantDegraded: false, wantDetail: "",
		},
		{
			// Sağlıklı kümede anlık derinlik normaldir — canlı ölçümde
			// sağlam tablolar 0-1 dosyadaydı. Eşiğin altı sessiz.
			name: "eşik altı çalkantı — büyüse bile sessiz",
			cur:  measured(40, 0), prev: measured(10, 0),
			wantDegraded: false, wantDetail: "",
		},
		{
			// KRİTİK: error_count sunucu açılışından beri kümülatif ve
			// AZALMAZ. Kuyruk boşken geçmişteki hata alarm üretmemeli.
			name: "boş kuyruk + eski hatalar — kümülatif sayaç alarm ÜRETMEZ",
			cur:  measured(0, 5000), prev: measured(0, 5000),
			wantDegraded: false, wantDetail: "",
		},

		// ── Bilgi ver, ama arıza İDDİA ETME ──────────────────────────
		{
			name: "ilk ölçüm — derinlik var, yön bilinmiyor",
			cur:  measured(12702, 1145, spans), prev: nil,
			wantDegraded: false, wantDetail: "trend henüz ölçülmedi",
		},
		{
			// Operatör 241'i çözdü, kuyruk eriyor: bu İYİ HABER, alarm değil.
			name: "drene oluyor — azalan kuyruk ok",
			cur:  measured(9000, 1145, spans), prev: measured(12702, 1145),
			wantDegraded: false, wantDetail: "drene oluyor",
		},
		{
			name: "sabit ve hatasız",
			cur:  measured(500, 0), prev: measured(500, 0),
			wantDegraded: false, wantDetail: "sabit, gönderim hatası yok",
		},

		// ── ARIZA: 2026-08-12'nin ta kendisi ─────────────────────────
		{
			name: "BÜYÜYOR — canlı arıza (3s39d ölü ingest, health yeşildi)",
			cur:  measured(38834, 1443, spans, metrics), prev: measured(38713, 1440),
			wantDegraded: true, wantDetail: "BÜYÜYOR",
		},
		{
			name: "sabit + gönderim hatası — gönderici takılı",
			cur:  measured(12702, 1145, spans), prev: measured(12702, 1100),
			wantDegraded: true, wantDetail: "SABİT",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, detail := DistributionVerdict(c.cur, c.prev)
			if got != c.wantDegraded {
				t.Fatalf("degraded = %v, want %v (detail=%q)", got, c.wantDegraded, detail)
			}
			if c.wantDetail == "" {
				if detail != "" {
					t.Fatalf("sinyal üretilmemeliydi, detail = %q", detail)
				}
				return
			}
			if !strings.Contains(detail, c.wantDetail) {
				t.Fatalf("detail = %q, %q içermeliydi", detail, c.wantDetail)
			}
		})
	}
}

// Arızalı dalların neden metni operatörü DOĞRU KATMANA göndermeli:
// "spans_write_failed 0 ama veri yok" sorusunun cevabı spool
// sözleşmesidir ve CH'nin kendi istisnası zaten elimizdedir.
func TestDistributionVerdictDetailCarriesDiagnosis(t *testing.T) {
	spans := DistributionQueueEntry{Table: "spans", Files: 12702, ErrorCount: 1145,
		LastError: "Code: 241. DB::Exception: Received from chc-0.chc-headless:9000.\n" +
			"   Memory limit (total) exceeded: would use 2.82 GiB, maximum: 2.80 GiB."}
	cur := measured(12702, 1145, spans)
	prev := measured(12000, 1100)

	degraded, detail := DistributionVerdict(cur, prev)
	if !degraded {
		t.Fatalf("büyüyen kuyruk degraded olmalıydı")
	}
	for _, want := range []string{"spans", "Son hata", "Code: 241"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail %q içermeliydi: %q", want, detail)
		}
	}
	// /api/health JSON'una giriyor — tek satır olmalı.
	if strings.ContainsAny(detail, "\n\r") {
		t.Fatalf("detail satır sonu taşıyor: %q", detail)
	}
}

// worstDistributionTable sıraya GÜVENMEMELİ: sorgu files DESC döndürüyor
// ama saf fonksiyon elle kurulan girdide de en derini bulmalı.
func TestWorstDistributionTableIgnoresInputOrder(t *testing.T) {
	got := worstDistributionTable([]DistributionQueueEntry{
		{Table: "logs", Files: 3},
		{Table: "metric_points", Files: 26132},
		{Table: "spans", Files: 12702},
	})
	if !strings.HasPrefix(got, "metric_points") {
		t.Fatalf("en derin tablo metric_points olmalıydı, got %q", got)
	}
	if worstDistributionTable(nil) != "" {
		t.Fatalf("boş girdi boş dönmeli")
	}
}

// SQL ŞEKLİ PİNİ — iki dal.
//
// Tek-düğüm dalı bu sürümün en önemli sözleşmesi: cluster_name boşken
// metin BOŞ döner, yani çağıran hiçbir sorgu çalıştırmaz. Dağıtık
// olmayan kurulumlar için ek maliyet SIFIR ve davranış değişmemiş olur.
func TestDistributionQueueSQLShape(t *testing.T) {
	if got := distributionQueueSQL(""); got != "" {
		t.Fatalf("tek düğümde sorgu ÜRETİLMEMELİ, got %q", got)
	}
	if got := distributionQueueSQL("   "); got != "" {
		t.Fatalf("boşluktan ibaret küme adı da tek-düğüm sayılmalı, got %q", got)
	}

	q := distributionQueueSQL("coremetry")
	// clusterAllReplicas ŞART: spool her düğümün KENDİ diskinde durur;
	// cluster() shard başına tek replika okur ve 2×2'lik bir kümede
	// spool'un yarısını görünmez yapardı (v0.9.454 ile aynı bulgu).
	if !strings.Contains(q, "clusterAllReplicas('coremetry', system.distribution_queue)") {
		t.Fatalf("clusterAllReplicas dalı eksik:\n%s", q)
	}
	if strings.Contains(q, "cluster('") {
		t.Fatalf("cluster() kullanılmamalı (shard başına tek replika):\n%s", q)
	}
	// Sistem tablosu küçük ama sınırsız bırakılmaz: tıkanmış bir kümede
	// bu okuma sağlık yolunun önünde durur.
	if !strings.Contains(q, "max_execution_time") {
		t.Fatalf("max_execution_time eksik:\n%s", q)
	}
	if !strings.Contains(q, "skip_unavailable_shards = 1") {
		t.Fatalf("düşmüş bir düğüm tüm ölçümü düşürmemeli:\n%s", q)
	}
	// Tek round-trip: teşhis alanlarının hepsi aynı sorguda.
	for _, col := range []string{"data_files", "data_compressed_bytes",
		"broken_data_files", "error_count", "last_exception"} {
		if !strings.Contains(q, col) {
			t.Fatalf("%s kolonu eksik:\n%s", col, q)
		}
	}
	// last_exception kırpılmalı — CH istisnaları kilobaytlarca olabilir
	// ve bu metin /api/health gövdesine giriyor.
	if !strings.Contains(q, "substring(argMax(last_exception") {
		t.Fatalf("last_exception kırpılmamış:\n%s", q)
	}
}

// Küme adındaki tek tırnak SQL'i kıramamalı (metin enterpolasyonu).
func TestDistributionQueueSQLStripsQuote(t *testing.T) {
	q := distributionQueueSQL("a'b")
	if strings.Contains(q, "'a'b'") {
		t.Fatalf("tek tırnak süzülmedi:\n%s", q)
	}
	if !strings.Contains(q, "clusterAllReplicas('ab', system.distribution_queue)") {
		t.Fatalf("beklenen temizlenmiş ad yok:\n%s", q)
	}
}

func TestFmtCount(t *testing.T) {
	for _, c := range []struct {
		in   uint64
		want string
	}{
		{0, "0"}, {7, "7"}, {999, "999"}, {1000, "1.000"},
		{12702, "12.702"}, {26132, "26.132"}, {1234567, "1.234.567"},
	} {
		if got := fmtCount(c.in); got != c.want {
			t.Fatalf("fmtCount(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
