package chstore

import "testing"

// db_saturation_test.go — v0.9.822.
//
// Sabitlenen sözleşmeler:
//   · Yüzde tavansız bir kullanımdan TÜRETİLMEZ (Inf/NaN JSON'a çıkarsa
//     frontend patlar — v0.5.301 sınıfı).
//   · Kontrol tablosu, DBInstance.System ile AYNI kelimeleri kullanır;
//     ayrışırsa karonun tıkı hiçbir satıra denk gelmez.
//   · Redis eviction bu tabloda OLMAMALI: rate'in tavanı yok, yüzdeye
//     çevrilemez.

func TestSaturationPct(t *testing.T) {
	cases := []struct {
		name         string
		usage, limit float64
		wantPct      float64
		wantOK       bool
	}{
		{"normal", 68.3, 1224, 68.3 / 1224 * 100, true},
		{"tam dolu", 100, 100, 100, true},
		{"tavanı aşmış (gerçek bir hâl)", 120, 100, 120, true},
		{"sıfır kullanım", 0, 100, 0, true},
		{"tavan 0 → yüzde YOK (Inf değil)", 5, 0, 0, false},
		{"tavan negatif → yüzde YOK", 5, -1, 0, false},
		{"negatif kullanım bir ölçüm değil", -5, 100, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pct, ok := saturationPct(c.usage, c.limit)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && pct != c.wantPct {
				t.Fatalf("pct = %v, want %v", pct, c.wantPct)
			}
			// Hiçbir yolda NaN/Inf sızmamalı.
			if pct != pct || pct > 1e12 {
				t.Fatalf("NaN/Inf sızdı: %v", pct)
			}
		})
	}
}

func TestDBSaturationChecksShape(t *testing.T) {
	if len(dbSaturationChecks) == 0 {
		t.Fatal("kontrol tablosu boş")
	}
	// System kelimesi DBInstance.System ile aynı olmalı: karonun tıkı
	// satır anahtarına (depRowKey → system|cluster|instance|dbName) denk
	// gelmezse hiçbir çekmece açılmaz — sessiz bir kırılma.
	// discoverReceiverInstances bu üç kelimeyi üretiyor.
	validSystems := map[string]bool{"oracle": true, "postgresql": true, "mysql": true, "redis": true}
	seen := map[string]bool{}
	for _, c := range dbSaturationChecks {
		if !validSystems[c.system] {
			t.Fatalf("bilinmeyen system %q — receiver satırlarıyla eşleşmez", c.system)
		}
		if c.prefix == "" || c.check == "" || c.read == nil {
			t.Fatalf("eksik kontrol tanımı: %+v", c)
		}
		// prefix ile system tutarlı olmalı (oracledb. → oracle).
		if c.prefix == "oracledb." && c.system != "oracle" {
			t.Fatalf("oracledb. öneki %q sistemine bağlanmış", c.system)
		}
		key := c.system + "/" + c.check
		if seen[key] {
			t.Fatalf("aynı kontrol iki kez: %s", key)
		}
		seen[key] = true
	}
	// Redis BİLEREK YOK — rate'in tavanı yok, yüzdeye çevrilemez.
	for _, c := range dbSaturationChecks {
		if c.system == "redis" {
			t.Fatal("redis eviction bir RATE; tavansız bir sayaç % doygunluk karosunda yer alamaz")
		}
	}
	// Oracle'ın üç kontrolü de bulunmalı (boyutlu tablespace dahil).
	for _, want := range []string{"oracle/sessions", "oracle/processes", "oracle/tablespace"} {
		if !seen[want] {
			t.Fatalf("%s kontrolü yok", want)
		}
	}
}

func TestDBSaturationEnvelopeDefaults(t *testing.T) {
	// LookbackSeconds okuma katmanının GERÇEK penceresini söylemeli;
	// sayfa range'i değil (gauge "şu anki doluluk" anlatıyor, 24 saate
	// ortalanmış bir doygunluk tam da görülmesi gereken zirveyi saklar).
	if int(capacityWindow.Seconds()) != 600 {
		t.Fatalf("capacityWindow = %v — karodaki metin 'son 10 dk' diyor, ayrıştı", capacityWindow)
	}
}
