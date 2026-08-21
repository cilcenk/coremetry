package api

// chat_step_ids.go — v0.9.1229. ⚙ adım çipinin KİMLİĞİ ve KANITI.
//
// v0.9.1181 çipe "veriyi göster" affordance'ı verdi: `step` olayı bir
// `i` taşır, tool çalıştıktan sonra `step-result` aynı `i` ile kanıtı
// yollar, frontend ikisini bu sayıyla eşler (useChatThread). O iş
// YALNIZ serbest tool döngüsünde yapılmıştı.
//
// Guided yol — yani cevapların ÇOĞUNLUĞU — adımlarını
// map[string]string{"tool","args"} olarak yayınlıyordu: `i` yok,
// `step-result` hiç yok. Frontend `i` yoksa detayı DÜŞÜRÜYOR, yani
// guided'ın çipleri tıklanamayan ölü etiketlerdi. Operatör serbest
// döngüde kanıt zincirini açabiliyor, asıl cevap yolunda
// açamıyordu — bir APM'de en yanlış yerdeki boşluk.
//
// Kimlik ÜRETİMİ tek yerde ve TEK sayaçta olmalı, çünkü bir istekte
// birden çok yol adım yayınlayabiliyor: guided bağlam çipini basıp
// rotayı devredebilir (copilot_guided.go), çekmece yolu kendi çipini
// basar, sonra serbest döngü çalışır. İki ayrı sayaç aynı sayıyı iki
// kez üretirdi ve frontend `i` ile eşlediği için kanıt YANLIŞ çipe
// yapışırdı. Bu yüzden sayaç SSE emit sarmalayıcısında yaşıyor:
// akıştaki her `step` olayı, kim yayınlarsa yayınlasın, sıradaki
// numarayı alır.

import "strings"

// withStepIDs — akıştaki her `step` olayına istek-boyunca tekil bir
// `i` damgalar. copilotChat'in SSE emit'ini bir kez sarar.
//
// map[string]any payload YERİNDE damgalanır: emitStepChip damgayı
// geri okuyup çağırana döndürüyor (eşli `step-result` bu kimliği
// ister). map[string]string payload (bağlam çipleri, çekmece yolu)
// yeni bir map'e kopyalanıp damgalanır — kimlikleri kimse geri
// okumuyor ama SAYI ATLAMAMALI: frontend çip şeridini `step`
// sırasıyla çiziyor ve detay dizisi yalnız `i` taşıyan olaylarla
// büyüyor; numarasız bir çip diziyi kaydırıp sonraki kanıtı yanlış
// çipe bindirirdi.
func withStepIDs(emit func(string, any)) func(string, any) {
	n := 0
	return func(kind string, payload any) {
		if kind == "step" {
			n++
			switch m := payload.(type) {
			case map[string]any:
				m["i"] = n
			case map[string]string:
				conv := make(map[string]any, len(m)+1)
				for k, v := range m {
					conv[k] = v
				}
				conv["i"] = n
				payload = conv
			}
		}
		emit(kind, payload)
	}
}

// emitStepChip — ⚙ çipini yayınlar ve sarmalayıcının verdiği kimliği
// döner. Çip tool ÇALIŞMADAN önce çıkar (ilerleme geri bildirimi),
// kanıt sonra ayrı bir olayla gelir.
//
// Sarmalanmamış bir emit'te (iç içe bundle çağrılarının no-op emit'i,
// testler) 0 döner ve eşli kanıt SESSİZCE yayınlanmaz — numarasız bir
// `step-result` frontend'de hiçbir çiple eşleşmez, en iyi ihtimalle
// gürültüdür.
func emitStepChip(emit func(string, any), tool, args string) int {
	m := map[string]any{"tool": tool, "args": args}
	emit("step", m)
	if i, ok := m["i"].(int); ok {
		return i
	}
	return 0
}

// emitStepEvidence — adımın KANITI: modelin gerçekten gördüğü metin,
// 4 KB tavanıyla kırpılmış ve kırpma İLAN EDİLEREK (clipStepPreview).
// `bytes` kırpılmamış gerçek boydur.
//
// İki durumda hiç yayınlanmaz:
//   - kimlik yoksa (yukarı bak),
//   - hata YOKKEN metin boşsa. Boş kanıt çipi düğmeye çevirirdi ve
//     açılan blok bomboş olurdu; ölü affordance (v0.9.592 dersi)
//     eksik affordance'tan kötüdür. Hata varsa metin daima dolu.
func emitStepEvidence(emit func(string, any), i int, tool, text string, err error) {
	if i <= 0 {
		return
	}
	ok := err == nil
	if err != nil {
		text = "error: " + err.Error()
	} else if strings.TrimSpace(text) == "" {
		return
	}
	preview, truncated := clipStepPreview(text)
	emit("step-result", map[string]any{
		"i": i, "tool": tool, "ok": ok,
		"preview": preview, "truncated": truncated, "bytes": len(text),
	})
}
