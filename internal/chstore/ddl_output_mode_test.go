// v0.9.605 — dağıtık DDL beklemesi boot'u öldürmesin.
//
// Operator-reported (prod, v0.9.603 rollout'u): api pod'u
// `CREATE DATABASE … ON CLUSTER` sırasında 180 sn bekleyip kod 159 ile
// öldü ve crashloop'a girdi.
//
// v0.9.604 SEMPTOMU yumuşattı (159'u tolere etti). Bu dilim SEBEBİ
// kaldırıyor: beklemenin kendisini. Varsayılan output_mode 'throw'
// istemciyi TÜM host'ların bitmesini beklemeye zorluyor; boot 158
// bildirimsel DDL çalıştırıyor ve rollout'ta birkaç pod aynı anda boot
// ediyor.
//
// Ayar CANLI ClickHouse'a karşı doğrulandı (24.8):
//
//	varsayılan: throw
//	geçerli değerler: none, throw, null_status_on_timeout, never_throw,
//	                  none_only_active, throw_only_active,
//	                  null_status_on_timeout_only_active
package chstore

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var goComment = regexp.MustCompile(`(?m)^\s*//.*$`)

func storeSourceNoComments(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("store.go okunamadı: %v", err)
	}
	return goComment.ReplaceAllString(string(b), "")
}

// TestDDLOutputModeOnBothConnections — ayar İKİ bağlantıda da olmalı.
//
// Prod'da patlayan SETUP bağlantısıydı (CREATE DATABASE oradan koşuyor),
// ana bağlantıya sıra bile gelmemişti. Yalnız birine koymak arızayı
// olduğu gibi bırakırdı.
func TestDDLOutputModeOnBothConnections(t *testing.T) {
	src := storeSourceNoComments(t)
	n := strings.Count(src, `"distributed_ddl_output_mode": "null_status_on_timeout"`)
	if n < 2 {
		t.Errorf("ayar %d bağlantıda, 2 bekleniyordu (setup + ana).\n\n"+
			"Prod'da patlayan SETUP bağlantısıydı — CREATE DATABASE oradan "+
			"koşuyor ve ana bağlantıya sıra gelmeden ölüyordu. Yalnız ana "+
			"bağlantıya koymak arızayı olduğu gibi bırakır.", n)
	}
}

// TestDDLOutputModeIsNotNeverThrow — GEVŞETME DAR olmalı.
//
// `never_throw` gerçek DDL hatalarını da (sözdizimi, izin, tip
// uyuşmazlığı) yutardı ve boot bozuk bir şemayla SESSİZCE devam
// ederdi. Bu, crashloop'tan kötüdür: crashloop en azından görünür.
func TestDDLOutputModeIsNotNeverThrow(t *testing.T) {
	src := storeSourceNoComments(t)
	for _, bad := range []string{`"never_throw"`, `"none"`} {
		if strings.Contains(src, `"distributed_ddl_output_mode": `+bad) {
			t.Errorf("output_mode %s yapılmış — gerçek DDL hataları da yutulur ve "+
				"boot bozuk şemayla sessizce devam eder. Yalnız zamanaşımı "+
				"gevşetilmeli (null_status_on_timeout).", bad)
		}
	}
}
