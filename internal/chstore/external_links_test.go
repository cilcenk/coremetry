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
	bad := []ExternalLinkSettings{
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
