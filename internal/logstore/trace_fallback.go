package logstore

// trace_fallback — v0.8.466. Operatör-reported: log gövdesindeki JSON
// trace_id taşıyor ama Logs sayfasının TRACE kolonu boş. Kolon yapısal
// alandan okunur; iki gerçek-dünya şekli onu ıskalıyordu:
//
//   A) trace_id log SATIRININ İÇİNDEKİ JSON metninde gömülü
//      ({"message": "{\"trace_id\":\"…\"}"}) — path okuyucu string'in
//      içine inemez.
//   B) trace_id yapısal ama alışılmadık bir path'te (message.trace_id,
//      json.trace_id, mdc.traceId …) — readPathAny'nin dört adayının
//      dışında.
//
// FallbackTraceContext yalnız DÖNEN satırlarda ve alan boşken çalışır
// (sayfa başına ≤~100 parse; sorgu maliyeti sıfır). Önce düzleştirilmiş
// attribute path'lerini son-segment eşleşmesiyle tarar (B), sonra gövde
// JSON'ına bakar (A). Bu bir GÖRÜNTÜ dolgusu: trace→log pivot'u yine
// gerçek alan/kolon üzerinden arar — kalıcı çözüm pipeline'da parse
// (ES ingest pipeline / filelog json_parser); bkz. es_trace_context.go
// pivotReady teşhisi.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// fallbackBodyMax — gövde JSON parse tavanı; log satırları tipik <8KB,
// patolojik bir dev gövde satır-başına maliyeti şişirmesin.
const fallbackBodyMax = 64 << 10

// FallbackTraceContext, boş kalan trace/span kimliklerini attribute
// path'lerinden ve gövde JSON'ından çıkarır. Bulamadığı için boş dönen
// değerler çağıranın mevcut boşunu değiştirmez.
func FallbackTraceContext(attrs map[string]string, body string) (traceID, spanID string) {
	traceID, spanID = scanAttrKeys(attrs)
	if traceID != "" && spanID != "" {
		return traceID, spanID
	}
	bt, bs := scanBodyJSON(body)
	if traceID == "" {
		traceID = bt
	}
	if spanID == "" {
		spanID = bs
	}
	return traceID, spanID
}

// scanAttrKeys — anahtarın SON dotted segmenti normalize edilip
// (küçük harf, _ ve - atılır) traceid/spanid ile eşleşir; değer hex
// doğrulamasından geçmek zorunda. Deterministik seçim için anahtarlar
// sıralı gezilir.
func scanAttrKeys(attrs map[string]string) (traceID, spanID string) {
	if len(attrs) == 0 {
		return "", ""
	}
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch normalizeIDKey(k) {
		case "traceid":
			if traceID == "" {
				traceID = normalizeIDValue(k, attrs[k], 32)
			}
		case "spanid":
			if spanID == "" {
				spanID = normalizeIDValue(k, attrs[k], 16)
			}
		}
		if traceID != "" && spanID != "" {
			break
		}
	}
	return traceID, spanID
}

// scanBodyJSON — gövde JSON nesnesiyse üst seviye + BİR seviye iç
// nesneleri tarar (sınırlı: derin ağaçlarda satır-başı maliyet
// büyümesin; gerçek dünyada trace_id ya kökte ya tek sarmalayıcının
// altında).
func scanBodyJSON(body string) (traceID, spanID string) {
	b := strings.TrimSpace(body)
	if len(b) == 0 || len(b) > fallbackBodyMax || b[0] != '{' {
		return "", ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(b), &m); err != nil {
		return "", ""
	}
	traceID, spanID = scanJSONMap(m)
	if traceID != "" && spanID != "" {
		return traceID, spanID
	}
	for _, v := range sortedMapValues(m) {
		if inner, ok := v.(map[string]any); ok {
			t, s := scanJSONMap(inner)
			if traceID == "" {
				traceID = t
			}
			if spanID == "" {
				spanID = s
			}
			if traceID != "" && spanID != "" {
				break
			}
		}
	}
	return traceID, spanID
}

func scanJSONMap(m map[string]any) (traceID, spanID string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sv, ok := m[k].(string)
		if !ok {
			// {"trace":{"id":"…"}} — ECS iç nesne şekli.
			if inner, isMap := m[k].(map[string]any); isMap && normalizeIDKey(k) == "trace" {
				if id, idOK := inner["id"].(string); idOK && traceID == "" {
					traceID = normalizeHexID(id, 32)
				}
			}
			continue
		}
		switch normalizeIDKey(k) {
		case "traceid":
			if traceID == "" {
				traceID = normalizeIDValue(k, sv, 32)
			}
		case "spanid":
			if spanID == "" {
				spanID = normalizeIDValue(k, sv, 16)
			}
		}
	}
	return traceID, spanID
}

// sortedMapValues — deterministik iç-nesne gezinimi.
func sortedMapValues(m map[string]any) []any {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, m[k])
	}
	return out
}

// normalizeIDKey — "message.Trace_Id" → "traceid"; yalnız son segment.
func normalizeIDKey(k string) string {
	if i := strings.LastIndexByte(k, '.'); i >= 0 {
		k = k[i+1:]
	}
	k = strings.ToLower(k)
	k = strings.ReplaceAll(k, "_", "")
	k = strings.ReplaceAll(k, "-", "")
	return k
}

// normalizeIDValue — key bağlamlı değer normalizasyonu (v0.8.466
// review-fix): Datadog log-correlation'ı (dd.trace_id / dd.span_id)
// kimliği DECIMAL uint64 string yazar; 16 haneli decimal'i hex sanmak
// BAŞKA bir span'e işaret ederdi. dd.* anahtarında tamamı-rakam değer
// decimal kabul edilip hex'e çevrilir (OTel'in Datadog çevirisiyle
// aynı: %016x / 32'ye sol-dolgu). dd.* dışındaki tamamı-rakam değerler
// hex olarak KALIR — meşru hex kimliklerin ~%0.05'i tamamı-rakamdır,
// battaniye reddi onları düşürürdü.
func normalizeIDValue(key, v string, wantLen int) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(strings.ToLower(key), "dd.") && isAllDigits(v) {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil || n == 0 {
			return ""
		}
		return fmt.Sprintf("%0*x", wantLen, n)
	}
	return normalizeHexID(v, wantLen)
}

func isAllDigits(v string) bool {
	if v == "" {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < '0' || v[i] > '9' {
			return false
		}
	}
	return true
}

// normalizeHexID — hex kimlik doğrulaması. Tireli UUID biçimini kabul
// edip tireleri atar (bazı pipeline'lar trace id'yi UUID gibi loglar);
// sonuç TAM wantLen hex olmalı, yoksa boş döner (sıfır id dahil —
// 000…0 W3C'de "geçersiz/örneklenmemiş" demektir, kolona basılmaz).
func normalizeHexID(v string, wantLen int) string {
	v = strings.TrimSpace(v)
	if wantLen == 32 {
		v = strings.ReplaceAll(v, "-", "")
	}
	if len(v) != wantLen {
		return ""
	}
	allZero := true
	for i := 0; i < len(v); i++ {
		c := v[i]
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return ""
		}
		if c != '0' {
			allZero = false
		}
	}
	if allZero {
		return ""
	}
	return strings.ToLower(v)
}

// isBareHexID reports whether the free-text query is a bare trace/span
// id (32 or 16 hex chars, optional surrounding whitespace). v0.8.521:
// Logs'un Search kutusuna yapıştırılan id, id'yi body'ye YAZMAYAN
// kurulumlarda da bulunsun diye kolon eşleşmesine yükseltilir.
func isBareHexID(q string) bool {
	q = strings.TrimSpace(q)
	if len(q) != 32 && len(q) != 16 {
		return false
	}
	for _, c := range q {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
