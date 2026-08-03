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
	"strconv"
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

// TestDDLTaskTimeoutIsUnderClientReadTimeout — v0.9.606'nın ASIL kuralı.
//
// Operator-reported: v0.9.605'ten sonra CREATE DATABASE geçti ama ilk
// CREATE TABLE istemci tarafında "i/o timeout" ile öldü. Sebep iki
// sayının çelişmesiydi:
//
//	sunucu:  distributed_ddl_task_timeout = 180 sn (CH varsayılanı)
//	istemci: ReadTimeout                  =  30 sn (sürücü ayarı)
//
// Sunucu kendi bütçesi içindeyken istemci bağlantıyı koparıyordu ve
// hata "i/o timeout" diye görünüyordu — gerçek sebebi (DDL kuyruğu)
// hiç söylemeden. En kötü hata türü budur: doğru yere bakmanı
// engelleyen hata.
//
// İki sayı iki ayrı yerde yaşıyor; bu test aralarındaki ilişkiyi
// kuruyor. Biri değişirse öteki de değişmeli.
func TestDDLTaskTimeoutIsUnderClientReadTimeout(t *testing.T) {
	src := storeSourceNoComments(t)

	// İstemci ReadTimeout'unu KAYNAKTAN oku — elle yazmak, iki sayının
	// yeniden ayrışmasına kapı açardı.
	m := regexp.MustCompile(`ReadTimeout:\s*(\d+)\s*\*\s*time\.Second`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("ReadTimeout kaynakta bulunamadı — test bayatladı")
	}
	readTimeout, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("ReadTimeout ayrıştırılamadı: %v", err)
	}

	if ddlTaskTimeoutSeconds >= readTimeout {
		t.Errorf("DDL bütçesi (%d sn) istemci ReadTimeout'undan (%d sn) KISA DEĞİL.\n\n"+
			"Sunucu kendi süresi içindeyken istemci bağlantıyı koparır ve hata "+
			"'i/o timeout' diye görünür — gerçek sebebi (DDL kuyruğu) hiç "+
			"söylemeden. Prod'da tam olarak bu oldu.",
			ddlTaskTimeoutSeconds, readTimeout)
	}
	// Anlamlı bir alt sınır: 0/negatif ayar CH tarafında "sonsuz bekle"
	// ya da tanımsız davranışa açılır.
	if ddlTaskTimeoutSeconds <= 0 {
		t.Errorf("DDL bütçesi %d — pozitif olmalı", ddlTaskTimeoutSeconds)
	}
}
