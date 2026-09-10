package api

import (
	"context"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/reqid"
)

// v0.10.578 — YAPILANDIRILMIŞ log alanından kimlik.
//
// Operatör raporu (2026-09-09, prod ekran görüntüsü): log kaydında
// `attributes.request_id` DOLU geliyor ama kimlik çözücü onu bulamıyor.
// Kök neden: çözücü yalnız gövde METNİNİ tarıyordu ve dosyanın kendi
// yorumu "Attributes hiçbir backend'de dolmuyor" diyordu — YANLIŞ:
// ES flatten() ile, CH arraysToMap ile dolduruyor.
//
// Bu test yapılandırılmış alanın gövdeden ÖNCE geldiğini ve anahtar
// adının uydurulmayıp OLDUĞU GİBİ taşındığını çiviler.
func TestIdentityFromLogAttrs(t *testing.T) {
	loc := reqid.Location("Europe/Istanbul")
	// Katı biçime uyan örnek — reqid.Parse geçmeli.
	strictVal := "A811001020102ATM_000000733820260909222312695979"

	cases := []struct {
		name    string
		attrs   map[string]string
		keys    []string
		wantVal string
		wantKey string
		wantOK  bool
	}{
		{
			name:    "request_id yapılandırılmış alandan okunur",
			attrs:   map[string]string{"request_id": strictVal, "user": ""},
			wantVal: strictVal, wantKey: "request_id", wantOK: true,
		},
		{
			name:    "camelCase yazım da tanınır",
			attrs:   map[string]string{"requestId": strictVal},
			wantVal: strictVal, wantKey: "requestId", wantOK: true,
		},
		{
			name:    "şablonun istediği anahtar varsayılanları YENER",
			attrs:   map[string]string{"request_id": strictVal, "function_id": "FN-42-XYZ"},
			keys:    []string{"function_id"},
			wantVal: "FN-42-XYZ", wantKey: "function_id", wantOK: true,
		},
		{
			name:    "boş değer atlanır, sonraki aday denenir",
			attrs:   map[string]string{"requestId": "", "request_id": strictVal},
			wantVal: strictVal, wantKey: "request_id", wantOK: true,
		},
		{
			name:   "aday anahtar yoksa bulunamaz — gövde yoluna düşer",
			attrs:  map[string]string{"thread": "default task-22", "user": ""},
			wantOK: false,
		},
		{
			name:   "nil attribute haritası panik etmez",
			attrs:  nil,
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			val, key, _, ok := identityFromLogAttrs(tc.attrs, tc.keys, loc)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, beklenen %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if val != tc.wantVal {
				t.Errorf("değer = %q, beklenen %q", val, tc.wantVal)
			}
			if key != tc.wantKey {
				t.Errorf("anahtar = %q, beklenen %q — anahtar UYDURULMAZ", key, tc.wantKey)
			}
		})
	}
}

// Katı biçime uymayan değer DÜŞÜRÜLMEZ: alan adı request_id ise değer
// kimliktir; yalnız "biçim doğrulanamadı" diye işaretlenir. Kaybetmek,
// şüpheyle göstermekten kötüdür.
func TestIdentityFromLogAttrsLooseIsKept(t *testing.T) {
	loc := reqid.Location("Europe/Istanbul")
	val, key, strict, ok := identityFromLogAttrs(
		map[string]string{"request_id": "KISA-KIMLIK-123"}, nil, loc)
	if !ok {
		t.Fatal("biçimi tutmayan değer DÜŞÜRÜLDÜ; işaretlenerek tutulmalıydı")
	}
	if strict {
		t.Error("strict = true, oysa biçim doğrulanamadı")
	}
	if val != "KISA-KIMLIK-123" || key != "request_id" {
		t.Errorf("val/key = %q/%q", val, key)
	}
	_ = time.Now
}

// Kablolama testi — saf yardımcı yeşil olsa da ÇAĞRILDIĞI yer pinlenmezse
// sözleşme ekranda yoktur. Burada aynı kayıt gövdesinde ridA, yapılandırılmış
// alanında ridB taşıyor: kazanan YAPILANDIRILMIŞ alan olmalı.
func TestResolveTraceLinkIdentity_StructuredAttrBeatsBody(t *testing.T) {
	rec := &logstore.LogRecord{
		TraceID: "abc", SpanID: "b",
		Body:       "islem tamam id=" + ridA,
		Attributes: map[string]string{"request_id": ridB, "thread": "task-22"},
	}
	s := &Server{logs: &scriptLogStore{bySpan: map[string][]*logstore.LogRecord{
		"b": {rec},
	}}}
	got := s.resolveTraceLinkIdentity(context.Background(), "abc", "", linkIdentitySpans(), "", nil)
	if got.Source != linkIdentitySourceLog {
		t.Fatalf("kaynak log olmalı: %+v", got)
	}
	if got.RequestID != ridB {
		t.Fatalf("yapılandırılmış alan gövdeyi YENMELİ: %q (gövdedeki %q kazandı)", got.RequestID, ridA)
	}
	if len(got.Identities) == 0 || got.Identities[0].Key != "request_id" {
		t.Fatalf("anahtar adı olduğu gibi taşınmalı: %+v", got.Identities)
	}
}
