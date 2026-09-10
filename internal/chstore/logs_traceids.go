package chstore

// logs_traceids.go — v0.10.584. CH log yolunda trace/span id yüklemlerinin
// TEK gövdesi: normalize (küçük harf + kırpma, ES paritesi) ve çoğul liste
// için tavanlı `trace_id IN (…)`.
//
// Neden tek gövde: logsWhere (liste/sayım/tail) ve logstore.CHStore.Histogram
// aynı yüklemi ayrı ayrı kuruyordu; birinde düzeltilen kural diğerinde
// sessizce eksik kalırdı. Şimdi ikisi de buradan geçer.

import "strings"

// LogsTraceIDsCap — çoğul trace listesinin üst sınırı. Üst akış bugün en çok
// 50 gönderiyor (influx enrichMaxRows); 200 onun üstünde ama sınırsız değil:
// sınırsız IN listesi sorgu metnini ve bind sayısını girdiyle orantılı büyütür.
const LogsTraceIDsCap = 200

// normalizeLogID — OTLP yazımı küçük harf hex; büyük harfli bir id kesin
// eşitlikte 0 satır verirdi. ES tarafı ToLower yapıyor, CH de yapmalı.
func normalizeLogID(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

// LogTraceIDsConjunct — `trace_id IN (?,?,…)` + normalize edilmiş bind'ler.
// Boş liste → ("", nil): "boş IN ()" CH'de sözdizimi hatasıdır, çağıran
// boş ifadeyi eklememeli. Boş/yalnız-boşluk id'ler atılır.
func LogTraceIDsConjunct(ids []string) (string, []any) {
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		if n := normalizeLogID(id); n != "" {
			args = append(args, n)
		}
		if len(args) >= LogsTraceIDsCap {
			break
		}
	}
	if len(args) == 0 {
		return "", nil
	}
	return "trace_id IN (" + strings.TrimRight(strings.Repeat("?,", len(args)), ",") + ")", args
}
