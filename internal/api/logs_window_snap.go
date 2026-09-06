package api

// logs_window_snap.go — v0.10.442 (log arama denetimi A6, V1 "yalnız
// anahtar") → v0.10.446 V2 "uç TTL'i kadar" (operatör kararı 2026-09-06):
// /logs penceresi istemciden ms hassasiyetli gelir
// (timeRangeToNs → Date.now()), yani her istek benzersiz from/to taşır ve
// serveCached anahtarı iki sekme / iki operatör / iki render arasında ASLA
// paylaşılmaz — 15/30/60 sn TTL'li önbellek fiilen dekoratifti.
//
// V2 (v0.10.446): canlı pencere UCUN KENDİ TTL'İNE (liste 15 sn, histogram/
// desenler 30 sn, alanlar 60 sn) oturtulur ve SORGU da o pencereyle koşar:
// istek gövdesi TTL içinde byte-byte aynı → hem serveCached hem ES
// request_cache isabet eder. En kötü gecikme zaten harcanan TTL'i aşmaz
// (10 dk seçeneği "operatör gördüğünü sanmadığı pencereyi okur" sınıfıydı,
// reddedildi). Pencere UZUNLUĞU korunur (snapRangeS "pencere daralmaz"
// kuralı); yalnız sağ uç ≤ TTL geriye kayar. Mutlak pencere (paylaşılan
// link / zoom: to şimdiden TTL'den fazla geride) byte-byte aynen; canlı
// kuyruk (SSE) pencere taşımaz, etkilenmez.
//
// Tek uç için emsal: getLogsFieldValues saat başına oturtur (v0.9.291).

import (
	"strconv"
	"time"

	"github.com/cilcenk/coremetry/internal/logstore"
)

const logsKeySnap = 10 * time.Minute // V1 kalıntısı; V2 uç TTL'ini kullanır (snapLogsWindow)

// snapLogsKeyWindow — SAF: (from, to, now) → anahtar için pencere.
// ok=false: bir sınır boş (trace-pinli/sınırsız sorgu) ya da pencere
// canlı değil (to şimdiden snap'ten fazla geride ya da gelecekte).
// Süre korunur: from' = to' − (to − from).
func snapLogsKeyWindow(from, to, now time.Time, snap time.Duration) (time.Time, time.Time, bool) {
	if from.IsZero() || to.IsZero() || !to.After(from) || snap <= 0 {
		return from, to, false
	}
	if now.Sub(to) > snap || to.Sub(now) > snap {
		return from, to, false
	}
	toS := to.Truncate(snap)
	return toS.Add(-to.Sub(from)), toS, true
}

// snapLogsWindow — V2 handler yardımcısı: canlı pencereyi ucun TTL'ine
// oturtur, SORGUYU da (f.From/f.To) taşır ve anahtar dizelerini döner.
// Mutlak/boş pencerede sorgu aynen, anahtar ham dizeler (varsayılan
// pencere kurulduysa o).
func snapLogsWindow(f *logstore.Filter, fromRaw, toRaw string, ttl time.Duration) (string, string) {
	if a, b, ok := snapLogsKeyWindow(f.From, f.To, time.Now(), ttl); ok {
		f.From, f.To = a, b
		return strconv.FormatInt(a.UnixNano(), 10), strconv.FormatInt(b.UnixNano(), 10)
	}
	if fromRaw == "" && !f.From.IsZero() {
		fromRaw = strconv.FormatInt(f.From.UnixNano(), 10)
	}
	if toRaw == "" && !f.To.IsZero() {
		toRaw = strconv.FormatInt(f.To.UnixNano(), 10)
	}
	return fromRaw, toRaw
}

// logsKeyWindow — V1 yardımcısı (yalnız anahtar); V2'de kullanılmıyor,
// snapLogsKeyWindow'un test edilebilir kabuğu olarak kalır. Canlı pencere oturtulur; aksi hâlde istemcinin dizeleri
// aynen (boş dahil — ama sunucu varsayılan pencere kurduysa o pencere
// de anahtara girer; eskiden desenler ucu boş dizeyi anahtarlayıp
// kayan pencereyi tek girdide paylaşıyordu).
func logsKeyWindow(from, to time.Time, fromRaw, toRaw string) (string, string) {
	if a, b, ok := snapLogsKeyWindow(from, to, time.Now(), logsKeySnap); ok {
		return strconv.FormatInt(a.UnixNano(), 10), strconv.FormatInt(b.UnixNano(), 10)
	}
	if fromRaw == "" && !from.IsZero() {
		fromRaw = strconv.FormatInt(from.UnixNano(), 10)
	}
	if toRaw == "" && !to.IsZero() {
		toRaw = strconv.FormatInt(to.UnixNano(), 10)
	}
	return fromRaw, toRaw
}
