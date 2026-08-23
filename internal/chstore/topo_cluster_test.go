// v0.9.1025 — topoloji kuyruk düğümünün CLUSTER kolonu.
//
// ─── Neden bu üç test var ───────────────────────────────────────────
// Topolojideki bir kuyruk düğümünden /messaging ÇEKMECESİNE derin link
// atılabilmesi için düğümün (system, cluster, destination) üçlüsünün
// tamamı gerekiyor. `cluster` bu sürümde topology_edges_5m'e yazılmaya
// başlıyor — ve yazılan değer, çekmecenin okuduğu MV'nin (
// messaging_summary_5m / messaging_caller_summary_5m) cluster'ıyla
// KARAKTER KARAKTER aynı kuraldan çıkmak ZORUNDA.
//
// Ayrışmanın belirtisi bir HATA DEĞİL: GetMessagingDetail cluster'ı tam
// eşitlikle filtreliyor, yani bir rung kayarsa çekmece sessizce BOŞ
// açılır. Operatör "bu topic'te veri yok" diye okur. v0.9.973 tam olarak
// bu sınıfı ("cevapsızlık, yanlış cevap değil") teşhis etmişti; burada
// aynı sınıfın yazma tarafındaki ikizi çivileniyor.
package chstore

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// clusterChainKeys — bir coalesce zincirinden taranan attr anahtarlarını
// SIRAYLA çıkarır. Sıra sözleşmenin parçası: aynı anahtar kümesini farklı
// sırada tarayan iki ifade, iki anahtarı birden basan bir span'de FARKLI
// cluster üretir.
func clusterChainKeys(expr string) []string {
	re := regexp.MustCompile(`indexOf\(attr_keys, '([^']+)'\)`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(expr, -1) {
		out = append(out, m[1])
	}
	return out
}

// mvClusterChain — store.go içindeki bir messaging MV gövdesinden
// `… AS cluster` coalesce'ini kesip çıkarır. `AS cluster`tan GERİYE doğru
// en yakın `coalesce(`e gidiyoruz: DESTINATION zinciri metinde cluster'dan
// SONRA geldiği için ileriye bakan bir pencere onun anahtarlarını da
// yutardı ve test yanlış şeyi çivilerdi.
func mvClusterChain(t *testing.T, body, mvName string) string {
	t.Helper()
	i := strings.Index(body, mvName)
	if i < 0 {
		t.Fatalf("%s DDL'i store.go'da bulunamadı — MV yeniden adlandırıldıysa bu test GÜNCELLENMELİ, silinmemeli", mvName)
	}
	rest := body[i:]
	j := strings.Index(rest, "AS cluster")
	if j < 0 {
		t.Fatalf("%s gövdesinde `AS cluster` yok — MV artık cluster materialize etmiyorsa köprü kimliği ÇÖKER", mvName)
	}
	head := rest[:j]
	k := strings.LastIndex(head, "coalesce(")
	if k < 0 {
		t.Fatalf("%s içinde `AS cluster` öncesi coalesce( yok", mvName)
	}
	return head[k:]
}

// TestTopoQueueClusterMirrorsMessagingMV — AYNILIK PİNİ.
//
// Üç yazım karşılaştırılıyor: paylaşılan `msgClusterExpr` sabiti (topoloji
// pass'lerinin kullandığı) ve iki messaging MV'sinin DDL'ine GÖMÜLÜ
// zincir. Sabit paylaşımı MV tarafında mümkün değil (DDL metni ham
// backtick), o yüzden garanti derleyiciden değil buradan geliyor.
func TestTopoQueueClusterMirrorsMessagingMV(t *testing.T) {
	src, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	body := stripGoLineComments(string(src))

	want := clusterChainKeys(msgClusterExpr)
	if len(want) != 3 {
		t.Fatalf("msgClusterExpr'den 3 anahtar bekleniyordu, %d çıktı (%v) — zincir değiştiyse MV'ler de değişmeli", len(want), want)
	}

	for _, mv := range []string{"messaging_summary_5m", "messaging_caller_summary_5m"} {
		chain := mvClusterChain(t, body, mv)
		got := clusterChainKeys(chain)
		if len(got) != len(want) {
			t.Fatalf("%s cluster zinciri %d anahtar tarıyor, msgClusterExpr %d: %v vs %v",
				mv, len(got), len(want), got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s anahtar %d ayrıştı: MV %q, msgClusterExpr %q — SIRA da sözleşme; kayan bir rung çekmeceyi SESSİZCE boş açar",
					mv, i, got[i], want[i])
			}
		}
		// Terminal literal: boş cluster'ın '(default)'a düşmesi çekmece
		// kimliğinin ta kendisi. MV '' bıraksaydı, topolojiden gelen
		// '(default)' hiçbir satırla eşleşmezdi.
		if !strings.Contains(chain, "'(default)'") {
			t.Errorf("%s cluster zinciri '(default)' terminaline düşmüyor — tek-cluster kurulumda köprü kırılır", mv)
		}
	}
	if !strings.Contains(msgClusterExpr, "'(default)'") {
		t.Error("msgClusterExpr '(default)' terminaline düşmüyor")
	}
}

// TestTopoQueueClusterGuardedAndShared — yazma tarafının şekli.
//
// İki şey aynı anda doğru olmalı:
//   - ifade paylaşılan sabitten geliyor (yeniden yazım = ayrışma riski),
//   - msg_system guard'ı yerinde (infra pass'i HER span'i geçiyor; guard
//     olmadan dört indexOf taraması messaging olmayan %93 satırda da
//     koşar).
func TestTopoQueueClusterGuardedAndShared(t *testing.T) {
	got := topoQueueClusterSQL()
	if !strings.Contains(got, msgClusterExpr) {
		t.Error("topoQueueClusterSQL msgClusterExpr sabitini GÖMMÜYOR — messaging MV'siyle ayrışmaya açık")
	}
	if !strings.HasPrefix(got, "if(msg_system != '', ") {
		t.Errorf("msg_system guard'ı kayboldu: %q", got)
	}
	if !strings.HasSuffix(got, ", '')") {
		t.Errorf("guard'ın else dalı boş dize değil: %q — messaging olmayan kenarlar uydurma bir cluster taşır", got)
	}
}

// TestTopoClusterWrittenByBothPasses — "MUST mirror" sözleşmesi + kolon
// sayısı dengesi.
//
// Kuyruk düğümü İKİ pass'ten doğuyor: infra (producer → queue, queue
// CHILD tarafında) ve async consumer (queue → consumer, queue PARENT
// tarafında). Biri cluster yazıp diğeri yazmazsa, düğümün cluster'ı
// hangi kenarın en güncel olduğuna göre gelip gider — deep-link
// aralıklı çalışır, ki bu hiç çalışmamaktan daha kötü teşhis edilir.
//
// Üç parça (kolon listesi / iç projeksiyon / dış toplama) DAİMA birlikte
// açılır: ikisi açık biri kapalı hâl, INSERT kolon sayısı ile SELECT
// kolon sayısının uyuşmadığı bir CH hatasıdır ve her bucket'ı öldürür.
func TestTopoClusterWrittenByBothPasses(t *testing.T) {
	src, err := os.ReadFile("topology.go")
	if err != nil {
		t.Fatal(err)
	}
	body := stripGoLineComments(string(src))
	// Birleştirme operatörünün etrafındaki boşluk üslup meselesi
	// ("`+clusterCol+`" ile "` + clusterCol + `" aynı şey) — pin
	// ŞEKLİ değil VARLIĞI çiviliyor, o yüzden boşluklar düşürülüyor.
	packed := strings.ReplaceAll(body, " ", "")

	for _, c := range []struct {
		frag string
		what string
	}{
		{"+clusterCol+", "INSERT kolon listesi"},
		{"+clusterInner+", "iç projeksiyon (msg_cluster)"},
		{"+clusterOuter+", "dış toplama (any(msg_cluster) AS cluster)"},
	} {
		if n := strings.Count(packed, c.frag); n != 2 {
			t.Errorf("%s (%s) iki pass'te değil, %d yerde — infra ve consumer pass'leri AYRIŞTI",
				c.frag, c.what, n)
		}
	}

	// Satır SİLME tuzağı: cluster GROUP BY'a girerse tek bir aggregation
	// koşusu, dedup anahtarı dışında ayrışan iki satır üretir ve
	// ReplacingMergeTree FINAL okumasında birini SİLER (calls kaybı).
	// `any(` bunun tek koruması — kaybolursa cluster gruplama boyutuna
	// terfi etmiş demektir.
	if !strings.Contains(body, "any(msg_cluster) AS cluster") {
		t.Error("cluster `any()` ile toplanmıyor — GROUP BY boyutuna terfi ettiyse ReplacingMergeTree FINAL okumasında SATIR SİLİNİR")
	}
	for _, forbidden := range []string{
		"GROUP BY parent_service, child, proto, kind_out, cluster",
		"GROUP BY parent_service, child_node, cluster",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("cluster GROUP BY'a girmiş (%q) — ORDER BY dışında kalan bir gruplama anahtarı satır kaybettirir", forbidden)
		}
	}
}

// TestTopoClusterColumnIsAdditive — şema tarafı: kolon hem CREATE'te hem
// idempotent ALTER'da olmalı. Yalnız CREATE'te olsaydı MEVCUT kurulumlar
// (tablo zaten var → CREATE IF NOT EXISTS no-op) kolonu HİÇ almazdı;
// yalnız ALTER'da olsaydı taze kurulumda kolon bir boot geç gelirdi.
func TestTopoClusterColumnIsAdditive(t *testing.T) {
	src, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	body := stripGoLineComments(string(src))

	if !strings.Contains(body, "cluster         LowCardinality(String) DEFAULT ''") {
		t.Error("topology_edges_5m CREATE'inde cluster kolonu yok")
	}
	if !strings.Contains(body, "ALTER TABLE topology_edges_5m ADD COLUMN IF NOT EXISTS cluster") {
		t.Error("mevcut kurulumlar için idempotent ALTER yok — kolon yalnız taze kurulumlara iner")
	}
	// ORDER BY'a DOKUNULMAMIŞ olmalı (CH kuralı: değiştirilemez; ayrıca
	// dedup anahtarını genişletmek 14 günlük karışık kimlik demekti).
	if !strings.Contains(body, "ORDER BY (time_bucket, parent_service, child_node, node_kind, protocol)") {
		t.Error("topology_edges_5m ORDER BY'ı değişmiş — dedup anahtarı SÖZLEŞME, genişletmek eski satırları çift kimliğe düşürür")
	}
}
