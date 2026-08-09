package chstore

// sqlargs.go — bind-argüman yardımcıları (IN listeleri).
//
// İkisi de v0.8.391'de db_waitlock.go'da doğmuştu ama zamanla o dosyanın
// dışına yayıldılar: incident.go, topology.go, problem.go ve
// anomaly_event.go bugün ikisini de çağırıyor. Wait/lock yüzeyi
// v0.9.852'de tamamen sökülürken (operatör: "wait lock'ı kaldırabilirsin
// — db metriklerini almıyorum") bu iki fonksiyon dosyayla birlikte
// gitseydi dört ayrı okuma yolu derlenmezdi.
//
// Bu yüzden söküm sırasında AYNEN buraya taşındılar: kapsamı waitlock'la
// sınırlı bir silme işlemi, kendi kapsamı dışındaki çağıranları kırmamalı.
// Davranış bayt-bayt aynı; değişen tek şey dosya adı.

import "strings"

// chPlaceholders renders n comma-separated bind markers for an IN
// list. n is always a small spec-derived constant at every call site.
func chPlaceholders(n int) string {
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

// toAnySlice widens a []string into the []any a query bind needs. Hoisted so
// the several IN/NOT IN builders don't each grow their own loop.
func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
