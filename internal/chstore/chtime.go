package chstore

import "time"

// chDateTime64Arg formats t for binding into a `toDateTime64(?, 9, 'UTC')`
// argument.
//
// v0.8.197 — operator-reported PRODUCTION (code 6: "Cannot parse string
// '2026-06-27T18:51:27.714Z' as DateTime64(9,'UTC'): syntax error at position
// 23"): the /ai usage page + the noisy-rules read bound their time bounds with
// time.RFC3339Nano, which emits a trailing 'Z' (and a 'T' separator). CH's
// DateTime64 string parser accepts "2006-01-02 15:04:05.fffffffff" but REJECTS
// the 'Z', so every one of those queries errored. This formats UTC with a space
// separator and NO timezone designator, matching the 'UTC' argument exactly.
func chDateTime64Arg(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05.999999999")
}

// chDateTimeArg formats t for binding against a plain `DateTime` column
// (SECOND granularity, no fraction).
//
// v0.9.581 — operatör raporu, PROD (code 53: "Cannot convert string
// '2026-08-02 08:26:15.176482289' to type DateTime: while executing
// function greaterOrEquals on arguments __table1.time_bucket DateTime").
//
// v0.9.578'de trace pencere aramasına zaman sınırı eklerken
// chDateTime64Arg kullandım — ama trace_summary_5m'in time_bucket'ı
// DateTime, DateTime64 DEĞİL. Sebep v0.9.572'de öğrendiğimiz şeyin ta
// kendisi: time_bucket `toStartOfInterval(time, INTERVAL 5 MINUTE)`
// ile üretiliyor ve o, saniye grenli bir INTERVAL ile DateTime64
// girdiden DÜZ DateTime döndürüyor.
//
// Yani aynı ders bu oturumda ÜÇÜNCÜ kez ısırdı (v0.9.572 dönüş tipi,
// v0.9.578 bağlama tipi). Kural artık burada yazılı:
//
//	toStartOfInterval(...) üzerine kurulmuş HER kolon DateTime'dır.
//	Ona karşı bağlarken chDateTimeArg, DateTime64 kolonlarda
//	chDateTime64Arg.
//
// Kesirli kısım OLMAMALI: CH'nin DateTime ayrıştırıcısı onu reddeder
// (RFC3339'un 'Z'sini reddettiği gibi — v0.8.197 aynı aile).
func chDateTimeArg(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05")
}
