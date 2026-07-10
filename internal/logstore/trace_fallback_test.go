package logstore

// v0.8.466 regresyon — FallbackTraceContext: TRACE kolonu boşken
// (operatör-reported) attribute path'lerinden ve gövde JSON'ından
// görüntü dolgusu. Tablo-testli; her iki durum (gömülü-JSON /
// alışılmadık-path) + reddedilen şekiller pinli.

import "testing"

const (
	tid = "4bf92f3577b34da6a3ce929d0e0e4736"
	sid = "00f067aa0ba902b7"
)

func TestFallbackTraceContext(t *testing.T) {
	cases := []struct {
		name      string
		attrs     map[string]string
		body      string
		wantTrace string
		wantSpan  string
	}{
		{
			name:      "durum B — iç path'li attribute (message.trace_id)",
			attrs:     map[string]string{"message.trace_id": tid, "message.span_id": sid},
			wantTrace: tid, wantSpan: sid,
		},
		{
			name:      "durum B — camelCase mdc.traceId",
			attrs:     map[string]string{"mdc.traceId": tid},
			wantTrace: tid,
		},
		{
			name:      "durum A — gövde JSON'unda trace_id",
			body:      `{"level":"INFO","trace_id":"` + tid + `","span_id":"` + sid + `","msg":"ödeme tamam"}`,
			wantTrace: tid, wantSpan: sid,
		},
		{
			name:      "durum A — bir seviye içte (fields sarmalayıcısı)",
			body:      `{"ts":"2026-07-10","fields":{"traceId":"` + tid + `"}}`,
			wantTrace: tid,
		},
		{
			name:      "ECS iç nesnesi {\"trace\":{\"id\":…}}",
			body:      `{"trace":{"id":"` + tid + `"},"message":"x"}`,
			wantTrace: tid,
		},
		{
			name:      "tireli UUID biçimi kabul, tiresiz normalize",
			attrs:     map[string]string{"trace_id": "4bf92f35-77b3-4da6-a3ce-929d0e0e4736"},
			wantTrace: tid,
		},
		{
			name:  "sıfır id (örneklenmemiş) basılmaz",
			attrs: map[string]string{"trace_id": "00000000000000000000000000000000"},
		},
		{
			name:  "hex olmayan / yanlış uzunluk reddedilir",
			attrs: map[string]string{"trace_id": "not-a-trace", "span_id": "kısa"},
			body:  `{"trace_id":"zzzz2f3577b34da6a3ce929d0e0e4736"}`,
		},
		{
			name: "JSON olmayan gövde no-op",
			body: `plain text log line trace_id=` + tid, // key=value biçimi bilinçli kapsam dışı
		},
		{
			name:      "attribute gövdeye göre önceliklidir",
			attrs:     map[string]string{"trace_id": tid},
			body:      `{"trace_id":"aaaa2f3577b34da6a3ce929d0e0e4736"}`,
			wantTrace: tid,
		},
		{
			name: "boş girişler",
		},
		// v0.8.466 review-fix — Datadog decimal kimlikleri: dd.* anahtarında
		// tamamı-rakam değer decimal uint64 kabul edilip hex'e çevrilir.
		{
			name:      "dd.span_id 16 haneli decimal → hex çevrimi",
			attrs:     map[string]string{"dd.span_id": "1234567890123456", "dd.trace_id": "9876543210"},
			wantTrace: "0000000000000000000000024cb016ea",
			wantSpan:  "000462d53c8abac0",
		},
		{
			name:  "dd.trace_id uint64 taşması reddedilir",
			attrs: map[string]string{"dd.trace_id": "99999999999999999999999999999999"},
		},
		{
			name:      "dd DIŞI tamamı-rakam 32'lik hex olarak kalır",
			attrs:     map[string]string{"trace_id": "12345678901234567890123456789012"},
			wantTrace: "12345678901234567890123456789012",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotT, gotS := FallbackTraceContext(c.attrs, c.body)
			if gotT != c.wantTrace {
				t.Fatalf("trace: %q != %q", gotT, c.wantTrace)
			}
			if gotS != c.wantSpan {
				t.Fatalf("span: %q != %q", gotS, c.wantSpan)
			}
		})
	}
}
