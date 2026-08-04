package chstore

import (
	"strings"
	"sync"
	"testing"
)

// v0.9.625 — terfi kolonu haritası artık BOOT SONRASI da yazılıyor.
//
// Sebep: küme kipinde boot DDL'i erteliyor (ddl_defer.go), yani onarım
// probe'tan SONRA uygulanıyor. Ertelenen DDL bitince yeniden probe edip
// haritayı güncellemezsek düzeltme ikinci bir restart'a kadar kapalı
// kalır — operatör deploy'u çeker, hiçbir şey değişmemiş görür.
//
// Ama v0.9.198'den beri sözleşme "BOOT-ONLY yazım, kilitsiz okuma"ydı.
// Çıplak bir map'e boot sonrası yazmak Go'da
//
//	fatal error: concurrent map read and map write
//
// demekti — kurtarılamaz, süreç ölür. Yani /traces isteklerini
// karşılarken haritayı güncellemek API pod'unu düşürürdü.
//
// Bu test `go test -race` altında koşar ve kopyala-değiştir + atomic
// pointer'ın okuyucularla yarışmadığını kanıtlar. -race olmadan da
// anlamlıdır: çıplak map'e dönülürse fatal ile patlar.
func TestPromotedColsSafeUnderConcurrentReprobe(t *testing.T) {
	prev := promotedColsPtr.Load()
	t.Cleanup(func() { promotedColsPtr.Store(prev) })

	const readers = 8
	const rounds = 200

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Okuyucular: gerçek okuma yollarının üçü de.
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _, _ = FilterExpr{Key: "channel_code", Op: "=", Values: []string{"030101"}}.SQL()
				_, _ = traceExtrasProjection([]string{"channel_code"})
				_, _ = businessDimExpr("CHANNEL_CODE")
			}
		}()
	}

	// Yazar: ertelenen DDL bitince koşan reprobe'un yaptığı şey.
	for i := 0; i < rounds; i++ {
		registerTraceAttrMaterialized(map[string]string{
			"channel_code": "attr_channel_code",
		})
	}
	close(stop)
	wg.Wait()

	// Yayınlanan harita gerçekten yerine oturmuş olmalı.
	sql, _, err := FilterExpr{Key: "channel_code", Op: "=", Values: []string{"030101"}}.SQL()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "attr_channel_code") {
		t.Fatalf("yeniden kayıt sonrası filtre kolona yönlenmeliydi: %s", sql)
	}
}

// registerTraceAttrMaterialized BİRLEŞTİRİR, üzerine yazmaz: reprobe
// yalnız doğrulayabildiği yazımları döndürür ve önceki kayıtları
// silmemelidir.
func TestRegisterMerges(t *testing.T) {
	prev := promotedColsPtr.Load()
	t.Cleanup(func() { promotedColsPtr.Store(prev) })

	registerTraceAttrMaterialized(map[string]string{"channel_code": "attr_channel_code"})
	registerTraceAttrMaterialized(map[string]string{"function_code": "attr_function_code"})

	m := promotedCols()
	if m["channel_code"] != "attr_channel_code" || m["function_code"] != "attr_function_code" {
		t.Fatalf("ikinci kayıt birincisini silmemeli, harita: %v", m)
	}
}

// Dönen map SALT OKUNUR sözleşmesi: yayınlanan kopya paylaşılıyor, ona
// yazmak diğer okuyucularla yarışır. Bu test sözleşmeyi belgeliyor —
// promotedCols() ASLA doğrudan mutasyona uğratılmamalı.
func TestPromotedColsReturnsPublishedCopy(t *testing.T) {
	prev := promotedColsPtr.Load()
	t.Cleanup(func() { promotedColsPtr.Store(prev) })

	registerTraceAttrMaterialized(map[string]string{"channel_code": "attr_channel_code"})
	first := promotedCols()
	registerTraceAttrMaterialized(map[string]string{"function_code": "attr_function_code"})

	// Eski anlık görüntü DEĞİŞMEMELİ — kopyala-ve-değiştir olduğunun
	// kanıtı; yerinde mutasyon olsaydı burada iki anahtar görünürdü.
	if _, leaked := first["function_code"]; leaked {
		t.Fatal("yeni kayıt eski anlık görüntüyü değiştirdi — kopyala-değiştir bozulmuş")
	}
}
