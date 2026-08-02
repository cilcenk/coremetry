// v0.9.550 — evaluator kalp atışı.
//
// Operatör raporu: "worker modda çalışan evaluator gerçekten problem
// bulsun ve Problems sekmesinde göreyim, bazen sanki takıldığını
// hissediyorum."
//
// "Hissediyorum" cümlesi teşhisin kendisiydi: evaluator'ın koştuğuna
// dair HİÇBİR kayıt yoktu. Son tik ne zaman atıldı, ne kadar sürdü,
// kaç kural işlendi, kaç problem açıldı — hiçbiri hiçbir yerde
// tutulmuyordu. Dolayısıyla "takıldı mı?" sorusu ne kanıtlanabilir ne
// çürütülebilirdi; operatörün elinde yalnızca Problems sayfasının
// sessizliği vardı, o da "sorun yok" ile "evaluator ölü" arasında
// ayrım yapmıyor. İkisi ekranda BİREBİR aynı görünüyor. En kötü
// gözlemlenebilirlik hatası budur: sistem sana yanlış bir şey
// söylemiyor, hiçbir şey söylemiyor ve sen bunu iyi haber sanıyorsun.
//
// Neden Redis: evaluator YALNIZCA worker pod'unda koşar
// (main.go, mode.worker), /api/problems'i ise API pod'u sunar.
// Bellekteki bir sayaç API tarafından görülemez — dağıtık kurulumda
// asla, monolitikte tesadüfen. Yazma-okuma paylaşımlı bir yerden
// geçmek ZORUNDA. Redis zaten lider kilidi ve v0.8.354 stamp
// aynası için bağlı; bu yüzden yeni bağımlılık yok, aynı cache
// tutamacı (SetStampCache) kullanılıyor.
//
// Neden ClickHouse değil: kalp atışı tik başına yazılır (60 sn) ve
// yalnız EN SON değeri anlamlıdır — geçmişi yok. Bu, TTL'li bir
// anahtarın tam tanımı; ReplacingMergeTree'ye tik başına satır yazıp
// FINAL ile okumak aynı bilgiyi çok daha pahalıya verirdi.
//
// TTL bilerek kısa DEĞİL: bayat bir kalp atışı silinirse "hiç
// çalışmadı" ile "14 dakikadır takıldı" ayırt edilemez hale gelir —
// ki operatörün sorduğu tam olarak bu ayrım. Anahtar bu yüzden uzun
// yaşar ve TAZELİĞİ okuyan taraf yorumlar.
package evaluator

import (
	"context"
	"encoding/json"
	"log"
	"time"
)

// HeartbeatKey — tek anahtar; kalp atışının geçmişi yok, son değeri
// var. API pod'u bunu okur (internal/api/evaluator_health.go).
// Dışa açık: evaluator yazar, API okur, ikisi AYNI anahtarı bilmek
// zorunda ve string'i iki yerde ayrı yazmak sessizce ayrışabilirdi.
const HeartbeatKey = "coremetry:eval:heartbeat"

// heartbeatTTL — straggler GC'si. Tik aralığının çok üstünde tutulur:
// amaç bayat kaydı SİLMEK değil, tamamen ölü bir kurulumun anahtarını
// sonsuza dek bırakmamak. Okuyan taraf tazeliği kendi yorumlar.
const heartbeatTTL = 24 * time.Hour

// Heartbeat — bir evaluator tikinin sonucu.
//
// Alan seçimi "operatör neye bakıp karar verir" sorusundan türedi:
// tik atıldı mı (FinishedAt), sağlıklı mı (DurationMS, Err), iş yaptı
// mı (Rules, Opened/Resolved), ve bu pod gerçekten lider mi (Leader).
type Heartbeat struct {
	// StartedAt / FinishedAt — unix ns. FinishedAt=0 iken tik HÂLÂ
	// koşuyor demektir; bu ikisinin ayrı tutulmasının tek sebebi
	// "uzun sürüyor" ile "bitti" arasındaki farkı okuyan tarafa
	// gösterebilmek. Tek zaman damgası bu ayrımı yapamazdı.
	StartedAt  int64 `json:"startedAt"`
	FinishedAt int64 `json:"finishedAt"`
	DurationMS int64 `json:"durationMs"`

	// Rules — bu tikte DEĞERLENDİRİLEN kural sayısı (devre dışı olanlar
	// hariç). Sıfır ve hata yoksa: kural yok demektir, takılma değil.
	Rules int `json:"rules"`
	// Opened / Resolved — bu tikte açılan/kapanan problem sayısı.
	// Sürekli 0 olması takılma DEĞİLDİR (sağlıklı sistemin normali);
	// bu yüzden hiçbir yerde alarm koşulu olarak kullanılmaz, yalnız
	// "evaluator iş yapıyor mu" sorusuna renk katar.
	Opened   int `json:"opened"`
	Resolved int `json:"resolved"`

	// Err — tikin BAŞARISIZ bittiği durumun mesajı (deadline dahil).
	// Boş = tik temiz bitti.
	Err string `json:"err,omitempty"`

	// Leader — bu pod tiki gerçekten çalıştırdı mı. Lider olmayan pod
	// kalp atışı YAZMAZ (aşağıya bkz.), ama alan yine de taşınır ki
	// okuyan taraf kaydın anlamını tahmin etmek zorunda kalmasın.
	Leader bool `json:"leader"`

	// Version — kalp atışını yazan binary. Bir dağıtım sonrası eski
	// pod'un kalp atışı hâlâ Redis'te durabilir; sürüm olmadan bunu
	// "çalışıyor" sanmak mümkün.
	Version string `json:"version,omitempty"`

	// IntervalMS — YAZANIN tik aralığı. Okuyan taraf "bu kalp atışı
	// bayat mı" sorusunu buna göre cevaplar; aralığı okuyan tarafta
	// sabit yazmak, evaluator'ın aralığı değiştiğinde sessizce yanlış
	// eşiğe düşmek demekti. Tempoyu yazan bildirir.
	IntervalMS int64 `json:"intervalMs"`
}

// writeHeartbeat — tik sonucunu Redis'e yazar.
//
// En iyi çaba: kalp atışı yazımı ASLA bir tiki bloke etmez veya
// başarısız etmez. Gözlemlenebilirlik katmanının gözlemlediği şeyi
// bozması, çözdüğü sorundan kötüdür.
//
// Kendi ctx'i var: tikin context'i deadline'a takılmış olabilir
// (asıl kaydetmek İSTEDİĞİMİZ durum tam da budur) — o ctx ile
// yazmaya kalksak takılan tikin kaydı hiç düşmezdi.
func (e *Evaluator) writeHeartbeat(hb Heartbeat) {
	if e.stamps == nil {
		return
	}
	hb.Version = e.version
	hb.IntervalMS = e.interval.Milliseconds()
	b, err := json.Marshal(hb)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := e.stamps.Set(ctx, HeartbeatKey, b, heartbeatTTL); err != nil {
		log.Printf("[evaluator] kalp atışı yazılamadı: %v", err)
	}
}

// tickCounters — bir tik boyunca açılan/kapanan problemleri sayar.
//
// Evaluator tek liderde tek tik çalıştırır, ama reconcile yolları
// (runtime pods, db capacity) aynı tik içinde çağrılır ve testler
// eşzamanlı dokunabilir — sayaçlar bu yüzden atomik.
func (e *Evaluator) countOpened()   { e.opened.Add(1) }
func (e *Evaluator) countResolved() { e.resolved.Add(1) }

// SetVersion — kalp atışına gömülecek binary kimliği. main()'den bir
// kez çağrılır (SetLogs / SetStampCache ile aynı desen); evaluator
// main'i import edemeyeceği için setter şart.
func (e *Evaluator) SetVersion(v string) { e.version = v }

// tickDeadline — bir tikin harcayabileceği azami süre.
//
// Bu koruma v0.9.550'nin ASIL düzeltmesi. Öncesinde evaluateAll kök
// context ile ticker döngüsünün İÇİNDE senkron çağrılıyordu: tek bir
// asılı CH/ES sorgusu tüm alarm üretimini SÜRESİZ durduruyordu ve
// dışarıdan görünen tek belirti Problems sayfasının sessizliğiydi.
//
// Bu varsayımsal bir risk değil: v0.8.342 yorumu (evaluator.go)
// aynı sınıftan bir olayı anlatıyor — "during an ES brownout each
// burned its timeout SEQUENTIALLY, stalling ALL alerting". O olayın
// TEK örneği (log-query fan-out) düzeltilmişti; tik seviyesinde
// koruma yoktu, yani aynı sınıftan yeni bir yavaş bağımlılık aynı
// sonucu yeniden üretebilirdi.
//
// 5× aralık seçimi: normal tik saniyeler sürer, dolayısıyla 5 dakika
// sağlıklı hiçbir tiki kesmez. Deadline bir performans hedefi değil,
// SONSUZ takılmaya karşı tavan — kesilen tik hata olarak kaydedilir
// ve bir sonraki tik temiz başlar. Kesmemek "belki birazdan biter"
// demektir ve o bahis kaybedildiğinde alarm üretimi kalıcı durur.
func tickDeadline(interval time.Duration) time.Duration {
	d := 5 * interval
	if d < time.Minute {
		d = time.Minute
	}
	return d
}
