package logstore

import "testing"

// v0.9.1384 — dedektör saf ve tablo-testli, çünkü İKİ yönü de pahalı:
// yanlış pozitif operatörün geçerli aramasını reddeder, yanlış negatif
// sessizce ateşlenmeyen bir alarmı geçirir.
func TestLooksLikeFieldQuery(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		// ── alan yazımı: CH'de sessizce sıfır ──────────────────────────
		{"servis kapsamı (kodun kendi şerhinin önerdiği yazım)", `service.name:"checkout"`, true},
		{"seviye", `level:error`, true},
		{"noktalı alan", `k8s.pod.name:web-1`, true},
		{"tireli alan", `trace-id:abc`, true},
		{"bileşik ifadenin İÇİNDE", `"disk full" AND service.name:"db"`, true},
		{"alt çizgili alan", `http_status:500`, true},

		// ── düz metin: CH'de ÇALIŞIR, reddedilmemeli ──────────────────
		{"tek kelime", `timeout`, false},
		{"tırnaklı ifade", `"no space left on device"`, false},
		{"boş", ``, false},
		{"OR grubu, alan yok", `("timeout" OR "refused")`, false},

		// ── ayırt edici vakalar: her biri bir yanlış-pozitifi kesiyor ──
		{"saat alan sorgusu DEĞİL (rakamla başlıyor)", `12:30 civarı`, false},
		{"URL şeması alan sorgusu DEĞİL (: ardından /)", `http://host/path`, false},
		{"iki nokta + BOŞLUK düz metindir", `ERROR: connection lost`, false},
		{"TIRNAK İÇİNDEKİ iki nokta literaldir", `"connection refused: timeout"`, false},
		{"tırnak içi literal + dışarıda alan yok", `"level:error yazan satır"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := LooksLikeFieldQuery(tc.in); got != tc.want {
				t.Errorf("LooksLikeFieldQuery(%q) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestBackendUnderstandsFieldQuery(t *testing.T) {
	// ClickHouse VARSAYILAN arka uç — bu satır kapının bütün gerekçesi.
	if BackendUnderstandsFieldQuery("clickhouse") {
		t.Error("clickhouse alan yazımını ayrıştırmıyor; true dönmek sessiz alarmı meşrulaştırır")
	}
	if !BackendUnderstandsFieldQuery("elasticsearch") {
		t.Error("elasticsearch query_string kullanıyor, alan yazımı orada GERÇEK")
	}
	// Bilinmeyen/boş arka uç: ayrıştırmadığını VARSAY. Yanlış yön burada
	// ucuz (fazladan uyarı), diğer yön pahalı (sessiz alarm).
	for _, b := range []string{"", "unknown", "CLICKHOUSE"} {
		if BackendUnderstandsFieldQuery(b) {
			t.Errorf("bilinmeyen arka uç %q için ayrıştırma VARSAYILMAMALI", b)
		}
	}
}
