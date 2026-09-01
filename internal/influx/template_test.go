package influx

import (
	"strings"
	"testing"
)

// template_test.go — v0.10.222 (Influx D1, audit §2 template.go + R1).
//
// Sözleşme: {{ad}} yer tutucuları doldurulur; DEĞER kapısı dar
// (^[A-Za-z0-9_.:\-]{1,64}$) — Flux string-literal'ına giren her şey bu
// kapıdan geçer, yani `"` / `\` / yeni satır ASLA enjekte edilemez (R1).
// Bilinmeyen yer tutucu hata: sessizce boş kalan bir filtre "tüm
// gruplar"a genişlerdi (sessizce genişleyen soru sınıfı).

func TestFillTemplate(t *testing.T) {
	tpl := `from(bucket: "B") |> range(start: {{from}}, stop: {{ to }})
  |> filter(fn: (r) => r.OPERATIONCODE == "{{op}}" and r.ERRORCODE == "{{err}}")`
	got, err := FillTemplate(tpl, map[string]string{
		"from": "2026-09-01T10:00:00Z", "to": "2026-09-01T10:05:00Z", "op": "OP_1.a:b-c", "err": "E1",
	})
	if err != nil {
		t.Fatalf("fill: %v", err)
	}
	for _, want := range []string{`start: 2026-09-01T10:00:00Z`, `stop: 2026-09-01T10:05:00Z`, `== "OP_1.a:b-c"`, `== "E1"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "{{") {
		t.Fatalf("placeholder left behind:\n%s", got)
	}
}

func TestFillTemplate_Rejects(t *testing.T) {
	cases := []struct {
		name string
		tpl  string
		vars map[string]string
		want string // hata mesajı parçası
	}{
		{"unknown placeholder", `x == "{{op}}"`, map[string]string{"err": "E"}, "op"},
		{"quote injection", `x == "{{op}}"`, map[string]string{"op": `a" or true or "`}, "op"},
		{"backslash", `x == "{{op}}"`, map[string]string{"op": `a\b`}, "op"},
		{"newline", `x == "{{op}}"`, map[string]string{"op": "a\nb"}, "op"},
		{"empty value", `x == "{{op}}"`, map[string]string{"op": ""}, "op"},
		{"too long", `x == "{{op}}"`, map[string]string{"op": strings.Repeat("a", 129)}, "op"},
		{"space", `x == "{{op}}"`, map[string]string{"op": "a b"}, "op"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := FillTemplate(c.tpl, c.vars)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error mentioning %q, got %v", c.want, err)
			}
		})
	}
}

func TestValidValue(t *testing.T) {
	for _, ok := range []string{"OP1", "a.b", "x:y", "a-b_c", "2026-09-01T10:00:00Z", "10.5", "CASHMANAGEMENT_NYT_INSTRUCTION_INQUIRY"} {
		if !ValidValue(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", " ", `"`, "a b", "ç", "a\tb", "x;y", "a|b"} {
		if ValidValue(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
