package oracle

// poller_test.go — v0.10.601 (Oracle Aşama 2; audit test listesi
// "poller_test.go: watermark, overlap, idempotent"). Oracle ve CH yok:
// satır okuyucu, satır havuzu ve state deposu sahte.
//
// Sözleşmeler: ilk poll now−15dk'dan · pencere (wm−overlap, now] ve bind
// değerleri kaynağın DUVAR SAATİ (İstanbul +3) · watermark en büyük satır
// zamanına ilerler, KALICI (yeni Worker aynı state'ten devam eder), asla
// gerilemez · aynı satır iki tikte aynı row_id (idempotent yazım) · hata
// veren tik watermark'ı ilerletmez, sink'e yazmaz, sağlık kancasını hatayla
// çağırır · CH yazımı düşerse watermark ilerlemez · tavana çarpan tik
// "capped" + nextDue=now, taban uygulanmaz · boş tik watermark'ı tabana çeker
// · kapalı kaynak poll'lanmaz, aralık dolmadan ikinci poll yok · poll
// sorgusu metni (> :1, <= :2, ASC, FETCH FIRST 5000) · durum blobu yayını.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

type fakeSink struct {
	mu     sync.Mutex
	calls  [][]chstore.OracleErrorRow
	failAt int // 1-based: bu çağrı hata döner (0 = asla)
}

func (f *fakeSink) InsertOracleErrors(_ context.Context, rows []chstore.OracleErrorRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, rows)
	if f.failAt == len(f.calls) {
		return errors.New("ch down")
	}
	return nil
}

type fakeState struct {
	mu sync.Mutex
	kv map[string][]byte
}

func (f *fakeState) GetSetting(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.kv[key], nil
}
func (f *fakeState) PutSetting(_ context.Context, key string, v []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.kv == nil {
		f.kv = map[string][]byte{}
	}
	f.kv[key] = append([]byte(nil), v...)
	return nil
}

type queryCall struct {
	sql  string
	args []any
}

func testSource() SourceConfig {
	return SourceConfig{ID: "o-aaaaaaaa", Name: "prod-eu", Enabled: true, Schema: "SHOP", Table: "MCA_TERROR_LOG",
		User: "u", Password: "p", Host: "db", Port: 1521, ServiceName: "svc", IntervalSec: 60}
}

func oracleRow(ts time.Time, msg string) map[string]any {
	return map[string]any{
		"MCA_ERR_TIMESTAMP": ts, "MCA_ERR_SEVERITY": "E", "MCA_ERR_MESSAGE": msg,
		"MCA_ERR_TRACEID": "4bf92f3577b34da6a3ce929d0e0e4736", "MCA_ERR_TYPE": "T", "MCA_ERR_CODE": "ERR_020",
	}
}

func newTestWorker(t *testing.T, src SourceConfig, state *fakeState, rows func(call int, args []any) ([]map[string]any, error)) (*Worker, *fakeSink, *[]queryCall, *time.Time) {
	t.Helper()
	svc := New()
	svc.Configure(Settings{Sources: []SourceConfig{src}})
	sink := &fakeSink{}
	w := NewWorker(svc, sink, state)
	now := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC) // 12:00 İstanbul
	w.now = func() time.Time { return now }
	calls := &[]queryCall{}
	n := 0
	w.queryRows = func(_ context.Context, _ SourceConfig, sqlText string, args []any) ([]map[string]any, error) {
		n++
		*calls = append(*calls, queryCall{sql: sqlText, args: args})
		return rows(n, args)
	}
	return w, sink, calls, &now
}

func TestPollFirstWindowAndBindWallClock(t *testing.T) {
	state := &fakeState{}
	w, sink, calls, now := newTestWorker(t, testSource(), state, func(int, []any) ([]map[string]any, error) {
		return []map[string]any{oracleRow(time.Date(2026, 9, 10, 11, 58, 0, 0, time.UTC), "a")}, nil // 11:58 duvar saati (İstanbul)
	})
	w.Tick(context.Background())
	if len(*calls) != 1 {
		t.Fatalf("1 sorgu bekleniyor, %d", len(*calls))
	}
	c := (*calls)[0]
	for _, want := range []string{"FROM SHOP.MCA_TERROR_LOG", "MCA_ERR_TIMESTAMP > :1 AND MCA_ERR_TIMESTAMP <= :2", "MCA_ERR_TYPE IN (:3)", "ORDER BY MCA_ERR_TIMESTAMP ASC", "FETCH FIRST 5000 ROWS ONLY"} {
		if !strings.Contains(c.sql, want) {
			t.Errorf("poll SQL %q içermeli:\n%s", want, c.sql)
		}
	}
	// İlk pencere: (now−15dk−overlap, now] — bind değerleri İstanbul duvar saati.
	from, to := c.args[0].(time.Time), c.args[1].(time.Time)
	wantFrom := time.Date(2026, 9, 10, 11, 43, 0, 0, time.UTC) // 09:00Z−17dk = 08:43Z = 11:43 İstanbul
	wantTo := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	if !from.Equal(wantFrom) || !to.Equal(wantTo) {
		t.Errorf("bind: from=%v to=%v, want %v..%v", from, to, wantFrom, wantTo)
	}
	if c.args[2] != "T" {
		t.Errorf("tip bind: %v", c.args[2])
	}
	if len(sink.calls) != 1 || len(sink.calls[0]) != 1 {
		t.Fatalf("sink: %d çağrı", len(sink.calls))
	}
	// Satır 11:58 İstanbul = 08:58Z; taban now−15dk = 08:45Z; watermark = 08:58Z.
	st := w.Status()[0]
	if got := time.Unix(0, st.WatermarkNs).UTC(); !got.Equal(time.Date(2026, 9, 10, 8, 58, 0, 0, time.UTC)) {
		t.Errorf("watermark %v", got)
	}
	if st.LastRows != 1 || st.LastMapped != 1 || st.LastError != "" || st.Capped {
		t.Errorf("durum %+v", st)
	}
	if st.NextDueAt != now.Add(60*time.Second).UnixMilli() {
		t.Errorf("nextDue %d", st.NextDueAt)
	}
	// Kalıcı: state deposunda watermark + durum blobu.
	if raw := state.kv[pollStateKeyPrefix+"o-aaaaaaaa"]; !strings.Contains(string(raw), `"watermarkNs":`) {
		t.Errorf("watermark kalıcı yazılmalı: %s", raw)
	}
	snap, ok := DecodeWorkerStatus(state.kv[WorkerStatusKey])
	if !ok || len(snap.Sources) != 1 || snap.Sources[0].LastRows != 1 {
		t.Errorf("durum blobu: %+v ok=%v", snap, ok)
	}
}

func TestPollOverlapResumeAndIdempotent(t *testing.T) {
	state := &fakeState{}
	first := time.Date(2026, 9, 10, 11, 58, 0, 0, time.UTC)
	rows := func(call int, _ []any) ([]map[string]any, error) {
		switch call {
		case 1:
			return []map[string]any{oracleRow(first, "a")}, nil
		default:
			// Overlap aynı satırı yeniden getirir + yeni bir satır.
			return []map[string]any{oracleRow(first, "a"), oracleRow(time.Date(2026, 9, 10, 12, 0, 30, 0, time.UTC), "b")}, nil
		}
	}
	w, sink, calls, now := newTestWorker(t, testSource(), state, rows)
	ctx := context.Background()
	w.Tick(ctx)
	// Aralık dolmadan ikinci tik sorgu ATMAZ.
	*now = now.Add(30 * time.Second)
	w.Tick(ctx)
	if len(*calls) != 1 {
		t.Fatalf("aralık dolmadan poll: %d sorgu", len(*calls))
	}
	*now = now.Add(31 * time.Second) // 09:01:01Z
	w.Tick(ctx)
	if len(*calls) != 2 {
		t.Fatalf("2 sorgu bekleniyor, %d", len(*calls))
	}
	// İkinci pencere watermark(08:58Z)−overlap = 08:56Z = 11:56 İstanbul.
	from := (*calls)[1].args[0].(time.Time)
	if !from.Equal(time.Date(2026, 9, 10, 11, 56, 0, 0, time.UTC)) {
		t.Errorf("overlap penceresi from=%v", from)
	}
	// İdempotent: aynı Oracle satırı iki tikte aynı row_id.
	if sink.calls[0][0].RowID != sink.calls[1][0].RowID {
		t.Errorf("aynı satır farklı row_id: %d vs %d", sink.calls[0][0].RowID, sink.calls[1][0].RowID)
	}
	if sink.calls[1][0].RowID == sink.calls[1][1].RowID {
		t.Error("farklı satırlar aynı row_id")
	}
	// Watermark ileri: 12:00:30 İstanbul = 09:00:30Z.
	if got := time.Unix(0, w.Status()[0].WatermarkNs).UTC(); !got.Equal(time.Date(2026, 9, 10, 9, 0, 30, 0, time.UTC)) {
		t.Errorf("watermark %v", got)
	}
	// Kalıcılık: YENİ worker aynı state'ten devam eder (bellek boş).
	w2, _, calls2, now2 := newTestWorker(t, testSource(), state, func(int, []any) ([]map[string]any, error) { return nil, nil })
	*now2 = now.Add(2 * time.Minute)
	w2.Tick(ctx)
	if len(*calls2) != 1 {
		t.Fatal("yeni worker poll'lamalı")
	}
	from2 := (*calls2)[0].args[0].(time.Time)
	if !from2.Equal(time.Date(2026, 9, 10, 11, 58, 30, 0, time.UTC)) { // 09:00:30Z−2dk = 08:58:30Z = 11:58:30 İstanbul
		t.Errorf("kalıcı watermark'tan devam etmeli, from=%v", from2)
	}
}

func TestPollErrorDoesNotAdvanceWatermark(t *testing.T) {
	state := &fakeState{}
	w, sink, _, now := newTestWorker(t, testSource(), state, func(call int, _ []any) ([]map[string]any, error) {
		if call == 1 {
			return nil, errors.New("ORA-12541: no listener")
		}
		return []map[string]any{oracleRow(time.Date(2026, 9, 10, 11, 59, 0, 0, time.UTC), "x")}, nil
	})
	var health []string
	w.SetHealthHook(func(_ context.Context, src SourceConfig, lastErr string) { health = append(health, src.ID+"|"+lastErr) })
	ctx := context.Background()
	w.Tick(ctx)
	st := w.Status()[0]
	if !strings.Contains(st.LastError, "ORA-12541") || len(sink.calls) != 0 {
		t.Fatalf("hata tiki: %+v sink=%d", st, len(sink.calls))
	}
	if _, ok := state.kv[pollStateKeyPrefix+"o-aaaaaaaa"]; ok {
		t.Error("hata tiki watermark YAZMAMALI")
	}
	if len(health) != 1 || !strings.Contains(health[0], "ORA-12541") {
		t.Errorf("sağlık kancası hatayla çağrılmalı: %v", health)
	}
	// Bir sonraki aralıkta aynı ilk pencereyle yeniden dener ve toparlar.
	*now = now.Add(time.Minute)
	w.Tick(ctx)
	st = w.Status()[0]
	if st.LastError != "" || len(sink.calls) != 1 {
		t.Errorf("toparlama: %+v", st)
	}
	if len(health) != 2 || health[1] != "o-aaaaaaaa|" {
		t.Errorf("sağlık kancası başarıda boş hatayla: %v", health)
	}
}

func TestPollSinkFailureKeepsWatermark(t *testing.T) {
	state := &fakeState{}
	w, sink, _, _ := newTestWorker(t, testSource(), state, func(int, []any) ([]map[string]any, error) {
		return []map[string]any{oracleRow(time.Date(2026, 9, 10, 11, 59, 0, 0, time.UTC), "x")}, nil
	})
	sink.failAt = 1
	w.Tick(context.Background())
	st := w.Status()[0]
	if !strings.Contains(st.LastError, "CH yazımı") {
		t.Fatalf("yazım hatası ilan edilmeli: %+v", st)
	}
	if _, ok := state.kv[pollStateKeyPrefix+"o-aaaaaaaa"]; ok {
		t.Error("yazım düşünce watermark ilerlemez (satır yeniden denenir)")
	}
}

func TestPollCappedDrainsImmediatelyWithoutFloor(t *testing.T) {
	state := &fakeState{}
	old := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC) // İstanbul duvar saati 09:00 = 06:00Z
	// Kalıcı watermark 05:00Z: pencere oradan başlar, 06:00Z'deki yığın
	// watermark'tan YENİ (ilk yazımda başlangıç now−15dk idi ve yığın ondan
	// eskiydi — "asla gerilemez" doğru çalışıp beklentiyi çürüttü).
	_ = state.PutSetting(context.Background(), pollStateKeyPrefix+"o-aaaaaaaa",
		[]byte(fmt.Sprintf(`{"watermarkNs":%d,"updatedAt":1}`, time.Date(2026, 9, 10, 5, 0, 0, 0, time.UTC).UnixNano())))
	w, _, _, now := newTestWorker(t, testSource(), state, func(int, []any) ([]map[string]any, error) {
		out := make([]map[string]any, pollRowCap)
		for i := range out {
			out[i] = oracleRow(old.Add(time.Duration(i)*time.Millisecond), "bulk")
		}
		return out, nil
	})
	w.Tick(context.Background())
	st := w.Status()[0]
	if !st.Capped || st.NextDueAt != now.UnixMilli() {
		t.Errorf("tavan: capped=%v nextDue=%d now=%d", st.Capped, st.NextDueAt, now.UnixMilli())
	}
	// Taban UYGULANMAZ: watermark en büyük satır zamanı (06:00:04.999Z), now−15dk DEĞİL.
	got := time.Unix(0, st.WatermarkNs).UTC()
	if want := time.Date(2026, 9, 10, 6, 0, 4, 999_000_000, time.UTC); !got.Equal(want) {
		t.Errorf("watermark %v, want %v (taban okunmamış satırları atlardı)", got, want)
	}
}

func TestPollEmptyTickFloorsWatermark(t *testing.T) {
	state := &fakeState{}
	_ = state.PutSetting(context.Background(), pollStateKeyPrefix+"o-aaaaaaaa", []byte(`{"watermarkNs":1757400000000000000,"updatedAt":1}`)) // 2025-09-09 — çok eski
	w, _, calls, now := newTestWorker(t, testSource(), state, func(int, []any) ([]map[string]any, error) { return nil, nil })
	w.Tick(context.Background())
	// Eski watermark'tan başlar (kalıcı değer okundu)…
	from := (*calls)[0].args[0].(time.Time)
	if from.Year() != 2025 {
		t.Errorf("kalıcı eski watermark okunmalı, from=%v", from)
	}
	// …boş tik sonrası taban: now−15dk.
	if got := time.Unix(0, w.Status()[0].WatermarkNs).UTC(); !got.Equal(now.Add(-pollInitialLookback)) {
		t.Errorf("boş tik watermark'ı tabana çekmeli: %v", got)
	}
}

func TestPollSkipsDisabledAndDropsStaleStatus(t *testing.T) {
	src := testSource()
	src.Enabled = false
	w, _, calls, _ := newTestWorker(t, src, &fakeState{}, func(int, []any) ([]map[string]any, error) { return nil, nil })
	w.Tick(context.Background())
	if len(*calls) != 0 || len(w.Status()) != 0 {
		t.Errorf("kapalı kaynak poll'lanmaz: %d sorgu, %d durum", len(*calls), len(w.Status()))
	}
}

func TestAdvanceWatermarkNeverRegresses(t *testing.T) {
	prev := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)
	to := prev.Add(5 * time.Minute)
	older := []chstore.OracleErrorRow{{Time: prev.Add(-time.Minute)}}
	if got := advanceWatermark(prev, older, to, true); !got.Equal(prev) {
		t.Errorf("eski satır watermark'ı geriletti: %v", got)
	}
	// Tavansız + satırsız: taban to−15dk < prev → prev kalır.
	if got := advanceWatermark(prev, nil, to, false); !got.Equal(prev) {
		t.Errorf("taban prev'in gerisindeyse prev kalır: %v", got)
	}
	// Tavansız + eski: taban çeker.
	far := to.Add(-time.Hour)
	if got := advanceWatermark(far, nil, to, false); !got.Equal(to.Add(-pollInitialLookback)) {
		t.Errorf("taban: %v", got)
	}
}

func TestBindTime(t *testing.T) {
	ist, _ := time.LoadLocation("Europe/Istanbul")
	in := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)
	if got := bindTime(in, ist, false); !got.Equal(time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("dilimsiz: duvar saati 12:00 olmalı, %v", got)
	}
	if got := bindTime(in, ist, true); !got.Equal(in) || got.Location() != time.UTC {
		t.Errorf("dilimli: UTC anı korunur, %v", got)
	}
}

func TestPollIntervalClamp(t *testing.T) {
	for in, want := range map[int]time.Duration{0: 60 * time.Second, 5: 60 * time.Second, 15: 15 * time.Second, 300: 300 * time.Second} {
		if got := pollInterval(SourceConfig{IntervalSec: in}); got != want {
			t.Errorf("interval %d → %v, want %v", in, got, want)
		}
	}
}

func TestBuildPollQueryRejectsBadConfig(t *testing.T) {
	src := testSource()
	src.ExtraWhere = "1=1; DROP TABLE x"
	if _, _, err := buildPollQuery(src, time.Now(), time.Now(), 10); err == nil {
		t.Error("extraWhere kapısı poll sorgusunda da çalışmalı")
	}
	src = testSource()
	src.Table = "bad-name"
	if _, _, err := buildPollQuery(src, time.Now(), time.Now(), 10); err == nil {
		t.Error("identifier kapısı")
	}
	src = testSource()
	src.ExtraWhere = "MCA_ERR_CHANNELCODE = 'MOB'"
	q, _, err := buildPollQuery(src, time.Now(), time.Now(), 0)
	if err != nil || !strings.Contains(q, "AND (MCA_ERR_CHANNELCODE = 'MOB')") || !strings.Contains(q, "FETCH FIRST 5000") {
		t.Errorf("extraWhere parantezli + limit 0 → tavan: %v\n%s", err, q)
	}
}
