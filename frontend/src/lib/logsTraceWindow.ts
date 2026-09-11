import type { TimeRange } from './types';

// logsUseTimeRange — v0.10.690 (operatör, prod: kiosk'ta trace logları var,
// Logs sayfasında "log backend yavaş"). Logs sayfası traceId seçiliyken
// pencereyi hiç göndermiyordu (tüm saklama süresi; yapıştırılan eski trace
// için doğru, ama ES'te 3 sn pivot bütçesini aşıyor). Kural:
//   - traceId yok → sayfanın aralığı (eskisi gibi);
//   - traceId + MUTLAK (custom) aralık → gönderilir: trace/kiosk derin bağlantısı
//     span'lere çapalı pencereyi zaten taşıyor (logsRangeParam), sorgu sınırlı;
//   - traceId + GÖRELİ aralık → gönderilmez: sunucu trace'in kendi penceresini
//     trace_summary_5m'den çözer (/api/logs/search v0.10.690), bulamazsa tüm
//     saklama (yapıştırılan id eski olabilir).
export function logsUseTimeRange(traceId: string, range: TimeRange): boolean {
  if (!traceId) return true;
  return range.preset === 'custom' && !!range.fromMs && !!range.toMs;
}
