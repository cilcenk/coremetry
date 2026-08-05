package anomaly

import (
	"os"
	"strings"
	"testing"
)

// v0.9.691 — AÇIK PROBLEMLER TİK BAŞINA TEK SORGUDA.
//
// checkOne her (servis, metrik) çifti için `FindOpenProblem` çağırıyordu:
// `problems FINAL` üzerinde nokta sorgusu, ve `problems` sıralama anahtarı
// `id` olduğu için rule_id/service ile budama YOK — her çağrı tam tarama.
//
// ÖLÇÜLDÜ (chc-0 query_log, 6 saat): 47.442 çağrı · 42 GiB · SELECT
// baytının %4.5. A/B: 297 nokta sorgusu 1.725 ms vs tek snapshot 5.2 ms
// → 331×. EXPLAIN: Parts 9/9, Granules 9/9 — budama sıfır, teyitli.
//
// Toplu okuma ZATEN YAZILMIŞ (OpenProblemsSnapshot); bu dedektör
// bağlanmamıştı — bu kod tabanının tekrar eden deseni: yetenek var,
// kullanan yok.

func detectorSrc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("anomaly.go")
	if err != nil {
		t.Fatal(err)
	}
	// Yorumlar sıyrılıyor: bu dosyanın aradığı adlar anomaly.go'nun kendi
	// gerekçesinde de geçiyor ve yorumlu tarama açıklamayı kod sanar.
	out := make([]string, 0, 1024)
	for _, l := range strings.Split(string(b), "\n") {
		if i := strings.Index(l, "//"); i >= 0 {
			l = l[:i]
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// ASIL KAPI: nokta sorgusu tarama döngüsünde OLMAMALI.
func TestDetectorDoesNotPointQueryPerService(t *testing.T) {
	if strings.Contains(detectorSrc(t), "FindOpenProblem(") {
		t.Error("dedektör hâlâ servis başına FindOpenProblem çağırıyor — " +
			"problems FINAL üzerinde budamasız tam tarama (ölçüm: 47.442 çağrı / 6 saat)")
	}
}

// Snapshot tik başına BİR KEZ alınmalı: checkOne'ın içine kayarsa N+1
// geri gelir, üstelik daha pahalı hâliyle.
func TestSnapshotTakenOncePerScan(t *testing.T) {
	src := detectorSrc(t)
	if n := strings.Count(src, "OpenProblemsSnapshot("); n != 1 {
		t.Errorf("OpenProblemsSnapshot %d kez çağrılıyor, tam 1 olmalı (scan başına)", n)
	}
	iScan := strings.Index(src, "func (d *Detector) scan(")
	iCheck := strings.Index(src, "func (d *Detector) checkOne(")
	iSnap := strings.Index(src, "OpenProblemsSnapshot(")
	if iScan < 0 || iCheck < 0 || iSnap < 0 {
		t.Fatal("beklenen fonksiyonlar bulunamadı")
	}
	if !(iSnap > iScan && iSnap < iCheck) {
		t.Error("snapshot scan() İÇİNDE alınmalı — checkOne'a kayarsa N+1 geri gelir")
	}
}

// Snapshot hatası tik'i ÖLDÜRMEMELİ. Eski kod `open, _ :=` ile hatayı
// yutup "açık problem yok" sayıyordu; ByKey nil-alıcı güvenli olduğu
// için aynı yola düşülüyor. Bu davranış korunmazsa CH'de tek bir
// hıçkırık tüm anomali tespitini durdurur.
func TestSnapshotErrorDoesNotAbortScan(t *testing.T) {
	src := detectorSrc(t)
	i := strings.Index(src, "OpenProblemsSnapshot(")
	win := src[i : i+400]
	if strings.Contains(win, "return") {
		t.Error("snapshot hatasında scan ERKEN DÖNÜYOR — eski davranış hatayı yutup devam etmekti")
	}
	if !strings.Contains(win, "log.Printf") {
		t.Error("snapshot hatası sessiz — en azından loglanmalı")
	}
}
