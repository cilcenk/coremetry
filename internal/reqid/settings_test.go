package reqid

import (
	"context"
	"errors"
	"testing"
	"time"
)

// settings_test.go — v0.9.1142. Saat dilimi ayarının HER düşüş yolu
// varsayılana gitmeli: sessizce UTC'ye düşmek ±10dk'lık pencerede
// garantili ıskadır (bkz. TestLocationAndLocalReading).

type settingStub struct {
	blob []byte
	err  error
	key  string
}

func (s *settingStub) GetSetting(_ context.Context, key string) ([]byte, error) {
	s.key = key
	return s.blob, s.err
}

func TestDecodeSettings(t *testing.T) {
	cases := []struct {
		name string
		blob string
		want string
	}{
		{"boş blob", "", ""},
		{"tz yok", `{}`, ""},
		{"tz var", `{"tz":"Europe/Berlin"}`, "Europe/Berlin"},
		{"boşluk kırpılır", `{"tz":"  Europe/Berlin  "}`, "Europe/Berlin"},
		{"bozuk JSON varsayılana düşer", `{tz:`, ""},
		{"yanlış tip varsayılana düşer", `{"tz":42}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DecodeSettings([]byte(c.blob)).TZ; got != c.want {
				t.Fatalf("TZ = %q, beklenen %q", got, c.want)
			}
		})
	}
}

func TestLocationFrom(t *testing.T) {
	ctx := context.Background()
	defOff := offsetOf(t, Location(""))

	t.Run("ayar yoksa varsayılan", func(t *testing.T) {
		st := &settingStub{}
		if got := offsetOf(t, LocationFrom(ctx, st)); got != defOff {
			t.Fatalf("ofset %d, beklenen %d", got, defOff)
		}
		if st.key != SettingKey {
			t.Fatalf("okunan anahtar %q", st.key)
		}
	})

	t.Run("okuma hatası varsayılana düşer", func(t *testing.T) {
		st := &settingStub{err: errors.New("ch down")}
		if got := offsetOf(t, LocationFrom(ctx, st)); got != defOff {
			t.Fatalf("ofset %d — hata hâlinde UTC'ye düşmek sessiz ıskadır", got)
		}
	})

	t.Run("nil reader varsayılan", func(t *testing.T) {
		if got := offsetOf(t, LocationFrom(ctx, nil)); got != defOff {
			t.Fatalf("ofset %d", got)
		}
	})

	t.Run("ayarlı tz onurlandırılır", func(t *testing.T) {
		st := &settingStub{blob: []byte(`{"tz":"UTC"}`)}
		if got := offsetOf(t, LocationFrom(ctx, st)); got != 0 {
			t.Fatalf("açık UTC ayarı yok sayıldı (ofset %d)", got)
		}
	})

	t.Run("geçersiz tz adı varsayılana düşer", func(t *testing.T) {
		st := &settingStub{blob: []byte(`{"tz":"Mars/Olympus"}`)}
		if got := offsetOf(t, LocationFrom(ctx, st)); got != defOff {
			t.Fatalf("ofset %d — tanınmayan ad UTC'ye düşmemeli", got)
		}
	})
}

// offsetOf — locu, KİMLİĞİN damgasında ölçülen UTC ofseti üzerinden
// karşılaştırır. Sabit bir an seçmek testi yaz saati geçişlerinden
// bağımsız kılıyor.
func offsetOf(t *testing.T, loc *time.Location) int {
	t.Helper()
	if loc == nil {
		t.Fatal("nil location")
	}
	id, ok := Parse(validID(), loc)
	if !ok {
		t.Fatal("sentetik kimlik ayrıştırılamadı")
	}
	_, off := id.TS.Zone()
	return off
}
