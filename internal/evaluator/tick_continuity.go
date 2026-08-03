// v0.9.588 — sessiz-kaynak süpürmesinin süreklilik kapısı.
//
// Operatör raporu: her rollout'ta ~6 problem "auto-resolved: source
// silent" damgasıyla kapanıyor ve hemen ardından yeniden açılıyor.
//
// KÖK NEDEN — süpürme kendi kör noktasını okuyor. Kural şu:
//
//	updated_at 3 tikten eskiyse → kaynak susmuş demektir
//
// Bu çıkarım, evaluator'ın O SÜRE BOYUNCA KOŞTUĞU varsayımına
// dayanıyor. Rollout'ta koşmuyor. Worker pod'u inip kalkarken hiçbir
// dedektör tik atamaz, dolayısıyla hiçbir problem tazelenemez ve yeni
// pod'un ilk tikinde HERKES bayat görünür. Süpürme de bunu "kaynak
// sustu" diye okur.
//
// İki hal ekranda BİREBİR aynı görünüyor:
//
//	kaynak sustu          → updated_at donuk
//	evaluator koşmuyordu  → updated_at donuk
//
// Ayırt eden tek şey, delili TOPLAYANIN o sırada ayakta olup
// olmadığı — ve bunu yalnız evaluator bilebilir. Bu dosya o bilgiyi
// kaydediyor: süpürme, ancak KENDİ gözlemlediği bir sessizliğe
// dayanarak karar verir.
//
// Zarar kozmetik değildi: kapanış problemin cooldown damgasını da
// siliyor (clearResolved), yani yeniden açılış TAZE bir problem olarak
// doğuyor — StartedAt sıfırlanıyor, yaş-bazlı eskalasyon baştan
// başlıyor ve /problems geçmişi her dağıtımda uydurma bir
// çözüldü→açıldı çifti kazanıyor.
package evaluator

import (
	"context"
	"encoding/json"
	"time"
)

// tickGapTolerance — iki tik arası bu kadarı "kesinti" sayılmaz.
//
// 2× aralık: tek bir kaçan tik (lider devri, ağ takılması) sürekliliği
// bozmamalı. Zaten süpürmenin kendi eşiği de 3× ile aynı toleransı
// gösteriyor.
func tickGapTolerance(interval time.Duration) time.Duration { return 2 * interval }

// noteTickContinuity — bu tikin bir öncekiyle kesintisiz olup
// olmadığını işler.
//
// runIfLeader'dan, DEĞERLENDİRME BAŞLAMADAN önce çağrılır. Tek
// goroutine'den (lider tiki seridir) çağrıldığı için kilit yok.
func (e *Evaluator) noteTickContinuity(now time.Time) {
	prev := e.lastTickAt
	inherited := time.Time{}

	// Süreç içinde önceki tik yoksa (yeni pod, ilk tik) süreklilik
	// kanıtı SÜREÇ DIŞINDAN gelmek zorunda. Tam da düzeltmeye
	// çalıştığımız hal bu: rollout'ta yeni pod'un belleği boştur ama
	// evaluator olarak fleet'in kesintisi yalnızca birkaç saniye
	// olabilir. Redis'teki kalp atışı o birkaç saniyeyi bilen tek yer.
	if prev.IsZero() {
		if hb, ok := e.readHeartbeat(); ok {
			if hb.StartedAt > 0 {
				prev = time.Unix(0, hb.StartedAt)
			}
			if hb.ContinuousSince > 0 {
				inherited = time.Unix(0, hb.ContinuousSince)
			}
		}
	}

	switch {
	case prev.IsZero() || now.Sub(prev) > tickGapTolerance(e.interval):
		// Kanıt yok ya da gerçek bir kesinti var → sayaç sıfırdan.
		// Kanıtsızlığı süreklilik saymak, düzeltmeye çalıştığımız
		// hatanın aynısı olurdu (kör noktayı veri sanmak).
		e.contSince = now
	case e.contSince.IsZero():
		// Devralma: önceki lider kesintisiz koşuyorduysa onun
		// başlangıcını sürdürüyoruz. Yoksa en azından önceki tik.
		if !inherited.IsZero() {
			e.contSince = inherited
		} else {
			e.contSince = prev
		}
	}
	e.lastTickAt = now
}

// sweepIsTrustworthy — sessizlik delili GERÇEKTEN bize mi ait?
//
// Süpürme eşiği 3× aralık; yani bir problemin bayat sayılabilmesi için
// evaluator'ın o pencere boyunca AYAKTA olup onu tazeleme fırsatı
// bulmuş olması gerekir. Kesintisiz koşma süresi bundan kısaysa
// bayatlığın kaynağı problem değil, BİZİZ.
func (e *Evaluator) sweepIsTrustworthy(now time.Time) bool {
	if e.contSince.IsZero() {
		return false
	}
	return now.Sub(e.contSince) >= 3*e.interval
}

// readHeartbeat — Redis'teki son kalp atışı.
//
// En iyi çaba: okunamıyorsa (cache yok, anahtar yok, bozuk JSON)
// "kanıt yok" döner ve çağıran süpürmeyi ATLAR. Kanıtsızlıkta
// süpürmemek doğru taraf: kaçırılan bir süpürme bir sonraki tikte
// telafi edilir, yanlış bir süpürme ise operatörün geçmişine sahte bir
// çözüldü→açıldı çifti yazar.
func (e *Evaluator) readHeartbeat() (Heartbeat, bool) {
	if e.stamps == nil {
		return Heartbeat{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b, ok, err := e.stamps.Get(ctx, HeartbeatKey)
	if err != nil || !ok {
		return Heartbeat{}, false
	}
	var hb Heartbeat
	if err := json.Unmarshal(b, &hb); err != nil {
		return Heartbeat{}, false
	}
	return hb, true
}
