package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.9.1249 — GetLogs'un pod conjunct'ı (Kibana-parite artığı: bağlam
// modalının "yalnız bu pod" kapsamı).
//
// Neden yalnız const şeklini pinlemek YETMEZ: bu dilimin gerçek riski
// yanlış satır kümesi değil, çağıranın SET ETTİĞİ bir filtrenin sorguya
// hiç girmemesi. Filter alanı eklenip conjunct unutulursa UI kapsamı
// açar, sunucu tüm podları döndürür ve kimse hata görmez. logsWhere
// saf seam'i tam bunu ölçülebilir kılıyor: aşağıdaki assert'ler
// conjunct düşürüldüğünde KIRMIZI yanar.

func TestLogsPodChainSQL(t *testing.T) {
	for _, want := range []string{
		"res_values[indexOf(res_keys, 'k8s.pod.name')]",
		"res_values[indexOf(res_keys, 'kubernetes.pod_name')]",
		"res_values[indexOf(res_keys, 'kubernetes.pod.name')]",
		"res_values[indexOf(res_keys, 'pod_name')]",
		"coalesce(",
	} {
		if !strings.Contains(logsPodChainSQL, want) {
			t.Errorf("logsPodChainSQL missing %q:\n%s", want, logsPodChainSQL)
		}
	}
	if strings.Contains(logsPodChainSQL, "resource_attributes[") {
		t.Error("logsPodChainSQL must not use Map access — the logs table has no resource_attributes column")
	}
	// İki backend aynı satırları süzmeli: logstore.chLogsPodExpr ile
	// anahtar kümesi/sırası AYNI. (Paket sınırı yüzünden değer değil,
	// sözleşme kopyalanıyor — ayrışma buradan görünür.)
	canonical := strings.Index(logsPodChainSQL, "'k8s.pod.name'")
	legacy := strings.Index(logsPodChainSQL, "'kubernetes.pod_name'")
	if canonical < 0 || legacy < 0 || canonical > legacy {
		t.Fatalf("kanonik k8s.pod.name önce gelmeli:\n%s", logsPodChainSQL)
	}
}

func TestLogsWhere_PodConjunct(t *testing.T) {
	pod := "payment-api-7d6f9b54c5-xkv2m"
	wc := logsWhere(LogFilter{
		Service: "payment-api",
		Pod:     pod,
		From:    time.Unix(0, 1),
		To:      time.Unix(0, 2),
	})
	sql := wc.sql()
	if !strings.Contains(sql, logsPodChainSQL+" = ?") {
		t.Fatalf("pod conjunct sorguya girmemiş (sessiz no-op sınıfı):\n%s", sql)
	}
	found := false
	for _, a := range wc.args {
		if s, ok := a.(string); ok && s == pod {
			found = true
		}
	}
	if !found {
		t.Fatalf("pod değeri bind args'a girmemiş: %v", wc.args)
	}
	// Kapsam DARALTMALI: conjunct sayısı pod'suz hâlden bir fazla.
	base := logsWhere(LogFilter{
		Service: "payment-api",
		From:    time.Unix(0, 1),
		To:      time.Unix(0, 2),
	})
	if len(wc.conds) != len(base.conds)+1 {
		t.Fatalf("pod tam olarak bir conjunct eklemeli: %d vs %d", len(wc.conds), len(base.conds))
	}
	if strings.Contains(base.sql(), "pod") {
		t.Fatalf("boş Pod hiçbir pod koşulu üretmemeli:\n%s", base.sql())
	}
}

// Pod, DİĞER conjunct'larla birlikte yaşamalı — bağlam yarıları
// service + env + search + pod'u aynı anda taşıyabiliyor.
func TestLogsWhere_PodComposesWithOtherFilters(t *testing.T) {
	wc := logsWhere(LogFilter{
		Service:     "payment-api",
		Env:         "prod",
		Pod:         "payment-api-abc",
		Search:      "timeout",
		SeverityMin: 17,
		From:        time.Unix(0, 1),
		To:          time.Unix(0, 2),
	})
	sql := wc.sql()
	for _, want := range []string{
		"service_name = ?",
		logsEnvChainSQL + " = ?",
		logsPodChainSQL + " = ?",
		"body LIKE ?",
		"severity_num >= ?",
		"time >= ?",
		"time <= ?",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("birleşik filtrede eksik conjunct %q:\n%s", want, sql)
		}
	}
	if got := strings.Count(sql, "?"); got != len(wc.args) {
		t.Errorf("placeholder/arg sayısı ayrışmış: %d ? vs %d arg", got, len(wc.args))
	}
}
