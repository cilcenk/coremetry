package chstore

// settings_refresh_test.go — v0.10.259 sözleşmesi: ctx'te anlık görüntü varsa
// GetSetting CH'ye gitmez (sıfır Store ile çağrılabilir), olmayan anahtar
// nil/nil; yükleyiciler sırayla koşar ve biri düşünce diğerleri durmaz;
// nil anlık görüntü ctx'i değiştirmez.

import (
	"context"
	"errors"
	"testing"
)

func TestGetSettingFromSnapshot(t *testing.T) {
	snap := NewSettingsSnapshot(map[string][]byte{"tempo": []byte(`{"url":"x"}`)})
	ctx := WithSettingsSnapshot(context.Background(), snap)
	var st Store // conn nil — anlık görüntü yolu CH'ye dokunmamalı
	v, err := st.GetSetting(ctx, "tempo")
	if err != nil || string(v) != `{"url":"x"}` {
		t.Fatalf("snapshot okuması: %q %v", v, err)
	}
	v, err = st.GetSetting(ctx, "missing")
	if err != nil || v != nil {
		t.Fatalf("olmayan anahtar nil/nil olmalı: %q %v", v, err)
	}
	if snap.Len() != 1 {
		t.Errorf("Len %d", snap.Len())
	}
	if WithSettingsSnapshot(context.Background(), nil) != context.Background() {
		t.Error("nil anlık görüntü ctx'i değiştirmemeli")
	}
}

func TestRunSettingsLoadersIsolatesErrors(t *testing.T) {
	var order []string
	loaders := []SettingsLoader{
		{Name: "a", Load: func(context.Context) error { order = append(order, "a"); return nil }},
		{Name: "b", Load: func(context.Context) error { order = append(order, "b"); return errors.New("boom") }},
		{Name: "c", Load: func(context.Context) error { order = append(order, "c"); return nil }},
	}
	if failed := runSettingsLoaders(context.Background(), loaders); failed != 1 {
		t.Errorf("failed=%d, istenen 1", failed)
	}
	if len(order) != 3 || order[2] != "c" {
		t.Errorf("hata sonrası yükleyiciler koşmalı: %v", order)
	}
	r := NewSettingsRefresher(nil, 0)
	r.Add("x", nil) // nil yükleyici yok sayılır
	r.Add("y", func(context.Context) error { return nil })
	if len(r.loaders) != 1 || r.interval <= 0 {
		t.Errorf("Add/interval: %+v", r)
	}
	r.RunOnce(context.Background()) // store nil → düz ctx ile koşar, panic yok
}
