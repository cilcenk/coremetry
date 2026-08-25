package chstore

import (
	"strings"
	"testing"
)

// v0.9.1385 — /logs serbest-metin yükleminin sözleşmesi.
//
// Saf ve tablo-testli, çünkü kusurun hiçbiri hata vermiyordu: yanlış
// yüklem HTTP 200 ve makul görünen bir liste döndürüyor, yalnız yanlış
// popülasyondan. Ölçülen belirti "dolu histogramın altında boş tablo"ydu.

// v0.10.2 — TestEscapeLikeNeedle SİLİNDİ. Kaçış, LIKE'ın `%`/`_`
// jokerleri için vardı; yüklem artık LIKE değil ve
// multiSearchAnyCaseInsensitive iğneyi LİTERAL alıyor. Korunacak bir
// davranış kalmadı — testi bırakmak, olmayan bir fonksiyonu çivilemek
// olurdu.

func TestIsBareHexID(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"32 hex trace id", "4bf92f3577b34da6a3ce929d0e0e4736", true},
		{"16 hex span id", "00f067aa0ba902b7", true},
		{"büyük harf hex", "4BF92F3577B34DA6A3CE929D0E0E4736", true},
		{"çevresinde boşluk", "  00f067aa0ba902b7  ", true},
		{"31 karakter — id değil", "4bf92f3577b34da6a3ce929d0e0e473", false},
		{"hex olmayan karakter", "4bf92f3577b34da6a3ce929d0e0e473g", false},
		{"düz kelime", "timeout", false},
		{"boş", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsBareHexID(tc.in); got != tc.want {
				t.Errorf("IsBareHexID(%q) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestLogSearchConjunct(t *testing.T) {
	t.Run("düz metin — histogramla AYNI yüklem", func(t *testing.T) {
		expr, args := logSearchConjunct("timeout")
		if !strings.Contains(expr, "multiSearchAnyCaseInsensitive(body, [?])") {
			t.Errorf("expr = %q; histogram yolunun yüklemi bekleniyordu", expr)
		}
		if strings.Contains(expr, "LIKE") {
			t.Errorf("expr = %q; LIKE geri gelmiş — tablo ile histogram yine ıraksar", expr)
		}
		// İğne LİTERAL: sarmalayıcı yüzde yok, kaçış yok.
		if len(args) != 1 || args[0] != "timeout" {
			t.Errorf("args = %v; [timeout] bekleniyordu", args)
		}
	})

	t.Run("operatörün yazdığı yüzde ARTIK sorun değil", func(t *testing.T) {
		// Eski LIKE yolunda `%` jokerdi ve kaçışlanması gerekiyordu.
		// multiSearchAny iğneyi olduğu gibi arıyor: kaçış kavramı yok.
		_, args := logSearchConjunct("disk 90%")
		if args[0] != "disk 90%" {
			t.Errorf("args[0] = %q; iğne AYNEN taşınmalıydı", args[0])
		}
	})

	// v0.8.521 sözleşmesi. Bu dal liste yolunda YOKTU: "Search'e trace id
	// yapıştır, bulsun" ekranın yalnız histogram yarısında çalışıyordu —
	// histogram sayıyor, tablo göstermiyordu.
	t.Run("çıplak hex id kolonlara da bakar", func(t *testing.T) {
		expr, args := logSearchConjunct("4BF92F3577B34DA6A3CE929D0E0E4736")
		for _, want := range []string{"multiSearchAnyCaseInsensitive(body, [?])", "trace_id = ?", "span_id = ?", " OR "} {
			if !strings.Contains(expr, want) {
				t.Errorf("expr = %q; %q içermeliydi", expr, want)
			}
		}
		if len(args) != 3 {
			t.Fatalf("args = %v; üç argüman bekleniyordu", args)
		}
		// Kolon karşılaştırması KÜÇÜK harfe indirilmiş olmalı: id'ler
		// tabloda küçük harf saklanıyor, operatör BÜYÜK harf yapıştırabilir.
		if args[1] != "4bf92f3577b34da6a3ce929d0e0e4736" || args[2] != args[1] {
			t.Errorf("kolon argümanları = %v, %v; küçük harfe indirilmiş id bekleniyordu", args[1], args[2])
		}
	})

	t.Run("düz metinde hex dalı YOK — ayırt edici", func(t *testing.T) {
		// 32 karakterlik ama hex OLMAYAN bir metin kolon dalını
		// tetiklememeli; tetiklerse her uzun arama iki gereksiz kolon
		// karşılaştırması taşır.
		expr, args := logSearchConjunct(strings.Repeat("z", 32))
		if strings.Contains(expr, "trace_id") || len(args) != 1 {
			t.Errorf("expr = %q, args = %v; hex olmayan metin için tek LIKE bekleniyordu", expr, args)
		}
	})
}

// TestListPathUsesTheSharedPredicate — logsWhere gerçekten bu yüklemi mi
// kullanıyor?
//
// Saf çekirdek yeşil ama çağrıldığı yer pinli değilse, kusur yerinde
// kalır. logsWhere saf bir dikiş olduğu için doğrudan çağrılabiliyor.
func TestListPathUsesTheSharedPredicate(t *testing.T) {
	wc := logsWhere(LogFilter{Search: "disk 90%"})
	joined := strings.Join(wc.conds, " AND ")
	if !strings.Contains(joined, "multiSearchAnyCaseInsensitive") {
		t.Errorf("logsWhere histogramla aynı yüklemi kurmuyor: %q", joined)
	}
	if strings.Contains(joined, "LIKE") {
		t.Errorf("logsWhere hâlâ LIKE üretiyor: %q", joined)
	}
	var found bool
	for _, a := range wc.args {
		if s, ok := a.(string); ok && s == "disk 90%" {
			found = true
		}
	}
	if !found {
		t.Errorf("logsWhere iğneyi aynen taşımıyor: %v", wc.args)
	}

	hex := logsWhere(LogFilter{Search: "00f067aa0ba902b7"})
	if !strings.Contains(strings.Join(hex.conds, " AND "), "trace_id = ?") {
		t.Errorf("logsWhere çıplak hex id için kolon dalını kurmuyor: %v", hex.conds)
	}
}
