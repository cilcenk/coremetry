package chstore

import (
	"strings"
	"testing"
)

// v0.9.622 — filtre yolu terfi kolonu haritasına HİÇ bakmıyordu.
//
// Operator-reported: /traces?range=6h&filters=[{"k":"channel_code",…}]
// 26 sn sürüp ClickHouse'un max_execution_time=25s sınırına takılıyordu.
// Gösterim kolonları (traceExtrasProjection) ve kırılımlar
// (businessDimExpr) haritayı kullanıyordu; yalnız FİLTRE — yani pahalı
// olan taraf — koşulsuz dizi aramasına düşüyordu.

func withPromoted(t *testing.T, key, col string) {
	t.Helper()
	prev := promotedColsPtr.Load()
	registerTraceAttrMaterialized(map[string]string{key: col})
	t.Cleanup(func() { promotedColsPtr.Store(prev) })
}

func TestFilterRoutesPromotedAttrToColumn(t *testing.T) {
	withPromoted(t, "channel_code", "attr_channel_code")

	f := FilterExpr{Key: "channel_code", Op: "=", Values: []string{"030101"}}
	sql, args, err := f.SQL()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "attr_channel_code") {
		t.Fatalf("terfi kolonuna yönlenmeliydi, üretilen: %s", sql)
	}
	if strings.Contains(sql, "indexOf(attr_keys") {
		t.Fatalf("dizi aramasında kalmış — düzeltmenin tamamı bu: %s", sql)
	}
	// Anahtar artık bind arg olarak gitmiyor; yalnız DEĞER kalıyor.
	if len(args) != 1 || args[0] != "030101" {
		t.Fatalf("yalnız değer bind edilmeli, alınan: %v", args)
	}
}

// span.<anahtar> öneki de aynı yola girmeli — Tempo tarzı yazımı
// kullanan operatör yavaş yolda kalmamalı.
func TestFilterRoutesPromotedAttrUnderSpanPrefix(t *testing.T) {
	withPromoted(t, "channel_code", "attr_channel_code")

	sql, _, err := FilterExpr{Key: "span.channel_code", Op: "=", Values: []string{"030101"}}.SQL()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "attr_channel_code") || strings.Contains(sql, "indexOf(attr_keys") {
		t.Fatalf("span. öneki de terfi kolonuna yönlenmeliydi: %s", sql)
	}
}

// EN ÖNEMLİ GÜVENLİK ÖZELLİĞİ: harita boşken (probe kolonun doğru
// dolduğunu KANITLAYAMADI) filtre dizi yolunda kalmalı. Aksi halde
// boş bir kolona yönlenip BOŞ sonuç dönerdi — yani "hiç trace yok".
// Yanlış sonuç, yavaş sonuçtan kötüdür.
func TestFilterStaysOnArrayPathWhenNotProbed(t *testing.T) {
	if _, registered := promotedCols()["channel_code"]; registered {
		t.Fatal("ön koşul: bu testte harita boş olmalı")
	}
	sql, args, err := FilterExpr{Key: "channel_code", Op: "=", Values: []string{"030101"}}.SQL()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "indexOf(attr_keys") {
		t.Fatalf("kanıtlanmamış kolona yönlenmemeliydi: %s", sql)
	}
	if len(args) != 2 {
		t.Fatalf("dizi yolunda anahtar+değer bind edilir, alınan: %v", args)
	}
}

// v0.9.619'un dersi, bu sefer terfi kolonları için: paylaşılan üreteç
// haritayı GÖMMEZ, parametre alır. metric_points'te attr_channel_code
// kolonu YOK — oraya sızarsa ClickHouse sorguyu reddeder.
func TestMetricPointsNeverRoutesToPromotedColumn(t *testing.T) {
	withPromoted(t, "channel_code", "attr_channel_code")

	sql, _, err := FilterExpr{Key: "channel_code", Op: "=", Values: []string{"030101"}}.SQLForMetricPoints()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sql, "attr_channel_code") {
		t.Fatalf("spans'a özgü kolon metric_points sorgusuna sızdı: %s", sql)
	}
	if !strings.Contains(sql, "indexOf(attr_keys") {
		t.Fatalf("metric_points dizi yolunda kalmalı: %s", sql)
	}
}

// EXISTS bilinçli olarak dizi yolunda: has(attr_keys,'k') ile
// col != '' eşdeğer değil (anahtar BOŞ DEĞERLE varsa ayrışırlar).
func TestExistsStaysOnArrayPath(t *testing.T) {
	withPromoted(t, "channel_code", "attr_channel_code")

	sql, _, err := FilterExpr{Key: "channel_code", Op: "EXISTS"}.SQL()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "has(attr_keys") {
		t.Fatalf("EXISTS has(attr_keys, …) olarak kalmalı: %s", sql)
	}
}

// Çözümleme sırası traceExtrasProjection ve businessDimExpr ile AYNI
// olmalı: terfi → well-known → dizi. Üç yerde üç sıra olsaydı aynı
// anahtar aynı sayfada farklı kolonlardan okunurdu.
func TestPromotedTakesPrecedenceOverWellKnown(t *testing.T) {
	// http.method well-known bir anahtar; terfi ettirilirse terfi kazanır.
	if _, ok := wellKnown["http.method"]; !ok {
		t.Skip("http.method well-known değil — test öncülü geçersiz")
	}
	withPromoted(t, "http.method", "attr_promoted_probe")

	sql, _, err := FilterExpr{Key: "http.method", Op: "=", Values: []string{"GET"}}.SQL()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "attr_promoted_probe") {
		t.Fatalf("terfi kolonu well-known'dan önce gelmeli: %s", sql)
	}

	// Aynı öncelik gösterim yolunda da geçerli mi (iki yer, tek kural)?
	proj, _ := traceExtrasProjection([]string{"http.method"})
	if !strings.Contains(proj, "attr_promoted_probe") {
		t.Fatalf("gösterim yolu farklı sıra kullanıyor — iki yer iki kural: %s", proj)
	}
}

// v0.10.127 — resource.<anahtar> öneki de terfi kolonuna düşer.
//
// K8s kolonları resource kapsamından türer; FilterBuilder resource
// kapsamındaki anahtarı `resource.` önekiyle önerir. Önek dalı yalnız
// resourceWellKnown'a bakıp diziye düşseydi önerilen yol yine yavaş
// yol olurdu (v0.9.619'un aynı dersi). Probe resource kapsamlı bir
// anahtarı İKİ yazımla kaydeder: çıplak ve `resource.` önekli.
func TestFilterRoutesPromotedResourceKeyUnderResourcePrefix(t *testing.T) {
	prev := promotedColsPtr.Load()
	registerTraceAttrMaterialized(map[string]string{
		"k8s.pod.name":          "k8s_pod",
		"resource.k8s.pod.name": "k8s_pod",
	})
	t.Cleanup(func() { promotedColsPtr.Store(prev) })
	for _, key := range []string{"resource.k8s.pod.name", "k8s.pod.name"} {
		f := FilterExpr{Key: key, Op: "=", Values: []string{"api-7d9f-x1"}}
		sql, args, err := f.SQL()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(sql, "k8s_pod") || strings.Contains(sql, "indexOf(") {
			t.Fatalf("%s terfi kolonuna yönlenmeliydi: %s", key, sql)
		}
		if len(args) != 1 || args[0] != "api-7d9f-x1" {
			t.Fatalf("%s yalnız değer bind edilmeli: %v", key, args)
		}
	}
}

// probe kayıt çıktısı: resource kapsamlı anahtar iki yazımla, attr
// kapsamlı anahtar tek yazımla haritaya girer (saf yardımcı).
func TestPromotedProbeKeysForScope(t *testing.T) {
	if got := promotedMapKeys(promotedAttr{res: true}, "k8s.pod.name"); len(got) != 2 || got[0] != "k8s.pod.name" || got[1] != "resource.k8s.pod.name" {
		t.Fatalf("resource kapsamı iki yazım vermeli, alınan %v", got)
	}
	if got := promotedMapKeys(promotedAttr{}, "channel_code"); len(got) != 1 || got[0] != "channel_code" {
		t.Fatalf("attr kapsamı tek yazım vermeli, alınan %v", got)
	}
}
