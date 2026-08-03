// v0.9.567 regresyon testi — birleşik (coalesce) group-by anahtarları.
//
// Bazı kavramların TEK bir attribute'u yok. Kod bunları dört yerde
// zaten coalesce ediyor (topology.go, messaging_e2e.go, dependencies.go)
// ama group-by tek anahtar aldığı için Explore ve preset dashboard'lar
// o birleşimi ifade EDEMİYORDU — doğru görünen ama boş dönen paneller.
//
// Kanıtlı olay (pivotHref.ts:80-86, canlı ClickHouse ile doğrulanmış):
// `messaging.destination.name` tek başına SIFIR satır döndürürken eski
// `messaging.destination` aynı saatte 1280 satır taşıyordu — 17 topic'in
// hepsinde has_name_attr = 0.
//
// Bu testin ASIL işi zincirlerin AYRIŞMAMASINI sağlamak: aynı kavramı
// iki yerde ayrı yazmak, bu oturumda beş kez hata üretti.
package chstore

import (
	"os"
	"strings"
	"testing"
)

// norm — SQL'i boşluktan arındırıp karşılaştırılabilir hale getirir.
func norm(s string) string { return strings.Join(strings.Fields(s), " ") }

func TestGroupKeyPeerCoalescesThreeSources(t *testing.T) {
	expr, args := groupKeyExpr("peer", true)
	if len(args) != 0 {
		t.Errorf("peer bağlı argüman istememeli: %v", args)
	}
	for _, want := range []string{"peer_service", "server.address", "net.peer.name"} {
		if !strings.Contains(expr, want) {
			t.Errorf("peer zinciri %q kaynağını taşımıyor — o kaynaktan gelen "+
				"bağımlılıklar görünmez olur:\n%s", want, expr)
		}
	}
	// Sıra ÖNEMLİ: peer_service en spesifik olan, önce gelmeli.
	iPeer := strings.Index(expr, "peer_service")
	iAddr := strings.Index(expr, "server.address")
	if iPeer > iAddr {
		t.Error("peer_service, server.address'ten SONRA geliyor — daha az " +
			"spesifik kaynak öne geçmiş")
	}
}

func TestGroupKeyMessagingDestinationCoalescesBothSpellings(t *testing.T) {
	expr, _ := groupKeyExpr("messaging.destination", true)
	for _, want := range []string{"messaging.destination.name", "messaging.destination"} {
		if !strings.Contains(expr, want) {
			t.Errorf("messaging zinciri %q yazımını taşımıyor:\n%s", want, expr)
		}
	}
	// Yeni semconv adı ÖNCE denenmeli.
	iName := strings.Index(expr, "messaging.destination.name")
	iLegacy := strings.LastIndex(expr, "'messaging.destination'")
	if iName < 0 || iLegacy < 0 || iName > iLegacy {
		t.Errorf("semconv sırası yanlış — yeni ad (.name) önce gelmeli:\n%s", expr)
	}
}

// ASIL KORUMA: zincirler koddaki asıllarıyla AYNI kalmalı. Biri
// güncellenip diğeri unutulursa aynı kavram iki farklı sonuç verir ve
// operatör hangisinin doğru olduğunu bilemez.
func TestCoalesceChainsMatchTopology(t *testing.T) {
	b, err := os.ReadFile("topology.go")
	if err != nil {
		t.Fatalf("topology.go okunamadı: %v", err)
	}
	topo := norm(string(b))

	peerExpr, _ := groupKeyExpr("peer", true)
	// group-by ifadesindeki coalesce gövdesi topology'nin infra_host
	// zinciriyle aynı kaynakları aynı sırada kullanmalı.
	for _, src := range []string{
		"nullIf(peer_service, '')",
		"nullIf(attr_values[indexOf(attr_keys, 'server.address')], '')",
		"nullIf(attr_values[indexOf(attr_keys, 'net.peer.name')], '')",
	} {
		if !strings.Contains(topo, src) {
			t.Errorf("topology.go artık %q kullanmıyor — group-by zinciri "+
				"ondan AYRIŞMIŞ olabilir, ikisi birlikte güncellenmeli", src)
		}
		if !strings.Contains(norm(peerExpr), src) {
			t.Errorf("group-by peer zinciri %q taşımıyor", src)
		}
	}

	msgExpr, _ := groupKeyExpr("messaging.destination", true)
	for _, src := range []string{
		"nullIf(attr_values[indexOf(attr_keys, 'messaging.destination.name')], '')",
		"nullIf(attr_values[indexOf(attr_keys, 'messaging.destination')], '')",
	} {
		if !strings.Contains(topo, src) {
			t.Errorf("topology.go artık %q kullanmıyor — zincirler ayrışmış olabilir", src)
		}
		if !strings.Contains(norm(msgExpr), src) {
			t.Errorf("group-by messaging zinciri %q taşımıyor", src)
		}
	}
}

// Mevcut anahtarların davranışı DEĞİŞMEMELİ — birleşik anahtar eklemek,
// tipli kolona çözülen anahtarları etkilememeli.
func TestExistingGroupKeysUnchanged(t *testing.T) {
	cases := map[string]string{
		"service.name": "service_name",
		"name":         "name",
		"kind":         "kind",
		"status_code":  "status_code",
	}
	for key, wantCol := range cases {
		expr, args := groupKeyExpr(key, true)
		if len(args) != 0 {
			t.Errorf("%s bağlı argüman istememeli: %v", key, args)
		}
		if !strings.Contains(expr, wantCol) {
			t.Errorf("%s artık %q kolonuna çözülmüyor: %s", key, wantCol, expr)
		}
		if strings.Contains(expr, "coalesce") {
			t.Errorf("%s yanlışlıkla birleşik anahtar olmuş: %s", key, expr)
		}
	}
}
