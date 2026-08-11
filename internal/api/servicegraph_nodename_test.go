package api

import "testing"

// decodeNodeName — v0.9.972 (G2 messaging köprüsü).
//
// ORİJİNAL BELİRTİ: topoloji grafiğindeki bir kuyruk düğümünün TEK
// eylemi "Recenter"dı; `/messaging`e köprü kurulamıyordu. Sebebin VERİ
// eksikliği olduğu sanılıyordu — değilmiş: `queue:` dalı sistemi zaten
// elinde tutuyor ama boş dize olarak döndürüyordu, yani istemciye
// kind="queue", system="" gidiyordu. Köprünün tıkandığı yer buydu.
//
// Sistem SUNUCUDA ayıklanır, istemcide DEĞİL: kural zaten üç yerde
// yaşıyor (toplayıcı, bu çözücü, grafik çizici) ve dördüncü bir ayna
// eklemek onları ayrışmaya davet ederdi.
func TestDecodeNodeName(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantName string
		wantSys  string
	}{
		// ── kuyruk: üç ad şekli de sahada var ────────────────────────
		{"queue sys:dest", "queue:kafka:payment.settled", "kafka:payment.settled", "kafka"},
		{"queue sys@host", "queue:rabbitmq@broker-1", "rabbitmq@broker-1", "rabbitmq"},
		{"queue sys only", "queue:sqs", "sqs", "sqs"},
		// Destination kendi içinde ayırıcı taşıyabilir — İLK ayırıcıda böl.
		{"queue dest has colon", "queue:kafka:tenant:orders", "kafka:tenant:orders", "kafka"},
		// '@' önce gelirse sistem orada biter (host adı ':' içerebilir).
		{"queue host has port", "queue:rabbitmq@broker-1:5672", "rabbitmq@broker-1:5672", "rabbitmq"},

		// ── db dalı: DEĞİŞMEDİ, regresyon koruması ───────────────────
		{"db sys@instance", "db:postgresql@pg-1", "postgresql@pg-1", "postgresql"},
		{"db sys only", "db:h2", "h2", "h2"},
		// instance adı '@' içerebilir; sistem İLK '@'ten önce biter.
		{"db instance has at", "db:postgresql@pg@replica", "postgresql@pg@replica", "postgresql"},

		// ── diğer önekler: sistem yok ────────────────────────────────
		{"ext", "ext:api.stripe.com", "api.stripe.com", ""},
		{"bare service", "payment-service", "payment-service", ""},
		// Servis adı ':' içerse bile önek yoksa AYIKLAMA YAPILMAZ —
		// kind node_kind'dan gelir, önek yalnız görünen adı kodlar.
		{"bare with colon", "weird:name", "weird:name", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotName, gotSys := decodeNodeName(c.raw)
			if gotName != c.wantName {
				t.Errorf("decodeNodeName(%q) name = %q, want %q", c.raw, gotName, c.wantName)
			}
			if gotSys != c.wantSys {
				t.Errorf("decodeNodeName(%q) system = %q, want %q", c.raw, gotSys, c.wantSys)
			}
		})
	}
}
