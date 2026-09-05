package api

// logs_window_snap.go — v0.10.442 (log arama denetimi A6, V1 "yalnız
// anahtar"): /logs penceresi istemciden ms hassasiyetli gelir
// (timeRangeToNs → Date.now()), yani her istek benzersiz from/to taşır ve
// serveCached anahtarı iki sekme / iki operatör / iki render arasında ASLA
// paylaşılmaz — 15/30/60 sn TTL'li önbellek fiilen dekoratifti.
//
// V1: yalnız ANAHTAR penceresi 10 dk sınıra oturtulur; sorgunun kendisi
// (f.From/f.To) ms hassasiyetiyle aynen koşar → operatörün gördüğü veri
// değişmez, bayatlık uçun kendi TTL'iyle sınırlı kalır (zaten o kadar
// bayat olabiliyordu). Yalnız CANLI pencere (to ≈ şimdi) oturtulur;
// paylaşılan link / zoom gibi mutlak bir pencere byte-byte kendi
// anahtarını korur. V2 (pencereyi de oturtmak → ES request_cache isabeti,
// 10 dk kör uç) davranış değişikliğidir — operatöre sorulacak.
//
// Tek uç için emsal: getLogsFieldValues saat başına oturtur (v0.9.291).

import (
	"strconv"
	"time"
)

const logsKeySnap = 10 * time.Minute

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

// logsKeyWindow — handler yardımcısı: anahtara girecek ham from/to
// dizeleri. Canlı pencere oturtulur; aksi hâlde istemcinin dizeleri
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
