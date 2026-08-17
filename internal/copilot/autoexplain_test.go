package copilot

import (
	"encoding/json"
	"testing"
)

// v0.9.1138 — otomatik açıklayıcı vidası. Deploy-kanıtı bulgusu: AI'yı
// açmak arka plan worker'larını anında ateşliyor; ücretli sağlayıcıda
// operatör bunu kapatabilmeli. nil ⇒ AÇIK (Enabled idyomu — upgrade
// davranış değiştirmez).
func TestAutoExplainDefaultAndToggle(t *testing.T) {
	s := New("anthropic", "", "")
	if !s.AutoExplainEnabled() {
		t.Fatal("varsayılan AÇIK olmalı (nil)")
	}
	off := false
	s.SetAutoExplain(&off)
	if s.AutoExplainEnabled() {
		t.Fatal("kapatma uygulanmadı")
	}
	on := true
	s.SetAutoExplain(&on)
	if !s.AutoExplainEnabled() {
		t.Fatal("açma uygulanmadı")
	}
	s.SetAutoExplain(nil)
	if !s.AutoExplainEnabled() {
		t.Fatal("nil'e dönüş = varsayılan açık")
	}
}

func TestAutoExplainLegacyBlobDecodesOn(t *testing.T) {
	var p persisted
	if err := json.Unmarshal([]byte(`{"provider":"openai","apiKey":"k","model":"m"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.AutoExplain != nil {
		t.Fatal("eski blob nil çözülmeli (⇒ açık)")
	}
	if err := json.Unmarshal([]byte(`{"autoExplain":false}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.AutoExplain == nil || *p.AutoExplain {
		t.Fatal("açık false kaybolmamalı")
	}
}
