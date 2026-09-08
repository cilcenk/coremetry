package chstore

import (
	"strings"
	"testing"
)

// external_links_test.go — v0.10.345 sözleşmesi (external_links.go başlığı).

const sampleLogLink = "https://logs.example/masterlog?date={{attrTime.function_id:ddMMyyyyHHmm}}&functionId={{attr.function_id}}&channelCode={{attr.channel_code}}&t={{traceId}}&s={{service}}&at={{time:yyyyMMdd}}"

func TestExternalLinkVars(t *testing.T) {
	req, err := ExternalLinkVars(sampleLogLink)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(req, ",") != "function_id,channel_code" {
		t.Fatalf("gerekli anahtarlar (tekil, sırayla): %v", req)
	}
	for _, bad := range []string{
		"https://x/{{attr}}", "https://x/{{attrTime.function_id}}", "https://x/{{time}}", "https://x/{{time:abc}}",
		"https://x/{{foo.bar}}", "https://x/{{traceId.x}}", "https://x/{{attr.k:dd}}",
	} {
		if _, err := ExternalLinkVars(bad); err == nil {
			t.Fatalf("%q reddedilmeli", bad)
		}
	}
}

func TestNormalizeExternalLinks(t *testing.T) {
	cfg, err := NormalizeExternalLinks(ExternalLinkSettings{Links: []ExternalLink{{Label: " Log İzleme ", URLTemplate: sampleLogLink}}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Links[0].Label != "Log İzleme" || len(cfg.Links[0].Requires) != 2 {
		t.Fatalf("normalize: %+v", cfg.Links[0])
	}
	// v0.10.346 — renk: #rrggbb, küçük harfe indirilir; bozuk renk reddedilir.
	if c, err := NormalizeExternalLinks(ExternalLinkSettings{Links: []ExternalLink{{Label: "a", URLTemplate: "https://x", Color: " #D32F2F "}}}); err != nil || c.Links[0].Color != "#d32f2f" {
		t.Fatalf("renk normalize: %+v %v", c, err)
	}
	bad := []ExternalLinkSettings{
		{Links: []ExternalLink{{Label: "a", URLTemplate: "https://x", Color: "red"}}},
		{Links: []ExternalLink{{Label: "", URLTemplate: "https://x"}}},
		{Links: []ExternalLink{{Label: "a", URLTemplate: "ftp://x"}}},
		{Links: []ExternalLink{{Label: "a", URLTemplate: "https://x/{{nope}}"}}},
		{Links: []ExternalLink{{Label: "a", URLTemplate: "https://x/a b"}}},
		{Links: []ExternalLink{{Label: "a", URLTemplate: "https://x"}, {Label: "a", URLTemplate: "https://y"}}},
	}
	for i, b := range bad {
		if _, err := NormalizeExternalLinks(b); err == nil {
			t.Fatalf("vaka %d reddedilmeli", i)
		}
	}
	empty, err := NormalizeExternalLinks(ExternalLinkSettings{})
	if err != nil || empty.Links == nil || len(empty.Links) != 0 {
		t.Fatalf("boş blob → boş liste (nil değil): %+v %v", empty, err)
	}
	registerExternalLinks(cfg)
	if got := CurrentExternalLinks(); len(got) != 1 || got[0].Label != "Log İzleme" {
		t.Fatalf("kayıt: %v", got)
	}
	registerExternalLinks(empty)
	if got := CurrentExternalLinks(); len(got) != 0 {
		t.Fatalf("boş kayıt: %v", got)
	}
}

// v0.10.371 — {{endTime:FMT}}: trace bitişi. Operatör: log platformunun
// dakika penceresi function_id'nin gömülü zamanından sonra biten trace'in
// loglarını kaçırıyordu; şablon artık bitişi taşıyabilir.
func TestExternalLinkVarsEndTime(t *testing.T) {
	req, err := ExternalLinkVars("https://x/?date={{endTime:ddMMyyyyHHmm}}&f={{attr.function_id}}")
	if err != nil {
		t.Fatalf("endTime:FMT kabul edilmeli: %v", err)
	}
	if len(req) != 1 || req[0] != "function_id" {
		t.Fatalf("endTime attribute gerektirmez; req = %v", req)
	}
	for _, bad := range []string{"{{endTime}}", "{{endTime:xx}}", "{{endTime.k:HHmm}}"} {
		if _, err := ExternalLinkVars(bad); err == nil {
			t.Fatalf("%s reddedilmeli", bad)
		}
	}
}

// v0.10.566 — {{requestId}}: trace'in LOGLARININ gövdesinden çözülen istek
// kimliği. Operatör kuralı: log gövdesinde request_id varsa link onunla
// üretilir, yoksa mevcut function_id/channel_code yolu sürer. Attribute
// OLMADIĞI için Requires'a girmemeli — girerse istemci "eksik alan" deyip
// düğmeyi kalıcı pasif bırakırdı (çözüm sunucuda, span'de değil).
func TestExternalLinkVarsRequestID(t *testing.T) {
	req, err := ExternalLinkVars("https://x/?rid={{requestId}}&t={{traceId}}")
	if err != nil {
		t.Fatalf("{{requestId}} kabul edilmeli: %v", err)
	}
	if len(req) != 0 {
		t.Fatalf("requestId attribute DEĞİL; Requires boş olmalı, oysa %v", req)
	}
	// Karışık şablon: requestId Requires'ı KİRLETMEZ, attr.* yine girer.
	req, err = ExternalLinkVars("https://x/?rid={{requestId}}&f={{attr.function_id}}")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(req, ",") != "function_id" {
		t.Fatalf("Requires yalnız attribute anahtarları olmalı: %v", req)
	}
	for _, bad := range []string{
		"https://x/{{requestId.x}}",  // anahtar almaz
		"https://x/{{requestId:dd}}", // biçim almaz
		"https://x/{{requestID}}",    // bilinmeyen değişken (yazım)
	} {
		if _, err := ExternalLinkVars(bad); err == nil {
			t.Fatalf("%q reddedilmeli", bad)
		}
	}
}

// v0.10.566 — Group: aynı gruptaki linklerden çözülen İLKİ çizilir.
// Normalize + tavan burada; anlam istemcide.
func TestNormalizeExternalLinksGroup(t *testing.T) {
	cfg, err := NormalizeExternalLinks(ExternalLinkSettings{Links: []ExternalLink{
		{Label: "rid", URLTemplate: "https://x/?r={{requestId}}", Group: "  log-izleme  "},
		{Label: "fid", URLTemplate: "https://x/?f={{attr.function_id}}", Group: "log-izleme"},
		{Label: "yalnız", URLTemplate: "https://x/?t={{traceId}}", Group: "   "},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Links[0].Group != "log-izleme" || cfg.Links[1].Group != "log-izleme" {
		t.Fatalf("grup trim edilip aynen korunmalı (aynı grup TEKRAR EDEBİLİR): %+v", cfg.Links)
	}
	if cfg.Links[2].Group != "" {
		t.Fatalf("yalnız boşluktan oluşan grup boşa inmeli: %q", cfg.Links[2].Group)
	}
	// Tavan RUNE cinsinden: 40 Türkçe karakter geçer, 41 geçmez.
	ok40 := strings.Repeat("ö", externalLinkGroupMax)
	if _, err := NormalizeExternalLinks(ExternalLinkSettings{Links: []ExternalLink{
		{Label: "a", URLTemplate: "https://x", Group: ok40},
	}}); err != nil {
		t.Fatalf("%d karakterlik grup kabul edilmeli (bayt değil rune sayılır): %v", externalLinkGroupMax, err)
	}
	if _, err := NormalizeExternalLinks(ExternalLinkSettings{Links: []ExternalLink{
		{Label: "a", URLTemplate: "https://x", Group: ok40 + "ö"},
	}}); err == nil {
		t.Fatalf("%d+1 karakterlik grup reddedilmeli", externalLinkGroupMax)
	}
}
