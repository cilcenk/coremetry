package chstore

// async_insert_settings_test.go — v0.10.240 (perf audit ING-1/2/3): ingest
// INSERT ayarları sözleşmesi. CLAUDE.md: async_insert mekanizması bozulmaz;
// dedup wait_for_async_insert=1 ister; busy timeout tabanı tavanı geçemez.

import "testing"

func TestAsyncInsertSettings(t *testing.T) {
	s := asyncInsertSettings()
	want := map[string]int{
		"async_insert":                     1,
		"wait_for_async_insert":            1,
		"async_insert_deduplicate":         1,
		"parallel_view_processing":         1,
		"async_insert_max_data_size":       10_485_760,
		"async_insert_busy_timeout_min_ms": 500,
		"async_insert_busy_timeout_ms":     1000,
		"async_insert_stale_timeout_ms":    1000,
	}
	for k, v := range want {
		got, ok := s[k]
		if !ok {
			t.Fatalf("setting %s missing", k)
		}
		if got.(int) != v {
			t.Fatalf("setting %s = %v, want %d", k, got, v)
		}
	}
	if s["async_insert_deduplicate"].(int) == 1 && s["wait_for_async_insert"].(int) != 1 {
		t.Fatal("async_insert_deduplicate requires wait_for_async_insert=1")
	}
	if s["async_insert_busy_timeout_min_ms"].(int) > s["async_insert_busy_timeout_ms"].(int) {
		t.Fatal("busy timeout floor must not exceed the ceiling")
	}
}

// v0.10.511 — C6 ölçüm anahtarı: paralel itiş kapatılınca yalnız
// parallel_view_processing değişir; öteki ayarlar (async_insert
// mekanizması, CLAUDE.md) aynen kalır. Varsayılan açık.
func TestAsyncInsertSettingsParallelViewsToggle(t *testing.T) {
	t.Cleanup(func() { SetParallelViewProcessing(true) })
	if asyncInsertSettings()["parallel_view_processing"].(int) != 1 {
		t.Fatal("varsayılan paralel itiş AÇIK olmalı")
	}
	SetParallelViewProcessing(false)
	off := asyncInsertSettings()
	if off["parallel_view_processing"].(int) != 0 {
		t.Fatal("kapatılınca 0 olmalı")
	}
	for k, v := range asyncInsertSettings() {
		if k == "parallel_view_processing" {
			continue
		}
		SetParallelViewProcessing(true)
		if asyncInsertSettings()[k] != v {
			t.Fatalf("anahtar %s'i etkilememeli", k)
		}
		SetParallelViewProcessing(false)
	}
}
