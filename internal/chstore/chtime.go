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
