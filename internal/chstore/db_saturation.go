package chstore

// db_saturation.go — /databases havuz doygunluğu (v0.9.822).
//
// Bu ölçüler v0.7 döneminden beri VARDI ama yalnız alert evaluator'ı
// okuyordu (internal/evaluator/db_capacity.go): operatör "oturum havuzu
// dolmak üzere" bilgisini ancak bir Problem AÇILDIKTAN sonra görüyordu.
// /databases sayfası, yani o soruyu sormak için gidilen yer, hiç
// bilmiyordu. Aynı gauge'lar, aynı okuma katmanı — sadece ikinci bir
// tüketici.
//
// EŞİK YOK, KARAR YOK: bu dosya sadece OKUR. Problem açma eşikleri
// evaluator'da kalıyor (capacityDecision), yoksa aynı eşiğin iki nüshası
// olur ve biri değişince sayfa ile alarm birbirini yalanlar.
//
// KOŞULLU: gauge'u OLMAYAN motor için satır dönmez. Bu bir hata durumu
// değil, normal durum — kurulumların çoğunda tek bir DB receiver'ı var.
// Hiç satır yoksa frontend karoyu HİÇ KURMAZ (messaging'in consumer-lag
// karosunu hiç kurmama kararıyla aynı disiplin): uydurulmuş bir "%0
// doygunluk" karosu, ölçülmemiş bir şeyi ölçülmüş gibi gösterirdi.
//
// MALİYET KAPISI: her motor ailesi ÖNCE metric_catalog'dan geçiyor
// (receiverPrefixActive, v0.9.693). Kapı olmadan bu uç, hiçbir postgres
// receiver'ı olmayan bir kurulumda bile her sayfa yüklemesinde iki boş
// metric_points taraması ödetirdi. CANLI DOĞRULANDI (chc-0):
// oracledb.* var (sessions/processes/tablespace), postgresql.* ve
// mysql.* HİÇ YOK — yani bu kurulumda kapı iki aileyi tamamen kapatıyor.

import "context"

// DBSaturationRow — bir (motor, instance, kontrol) doygunluk okuması.
// Subkey yalnız boyutlu kontrollerde dolu (Oracle tablespace adı).
type DBSaturationRow struct {
	System   string  `json:"system"`   // "oracle" / "postgresql" / "mysql" — DBInstance.System ile AYNI kelime
	Instance string  `json:"instance"` // receiver instance kimliği (instanceExpr)
	Check    string  `json:"check"`    // "sessions" / "processes" / "connections" / "tablespace"
	Subkey   string  `json:"subkey,omitempty"`
	Usage    float64 `json:"usage"`
	Limit    float64 `json:"limit"`
	Pct      float64 `json:"pct"` // usage/limit*100
}

// DBSaturation — /api/databases/saturation zarfı.
type DBSaturation struct {
	Rows []DBSaturationRow `json:"rows"`
	// LookbackSeconds — okumanın GERÇEK penceresi. Sayfa penceresi
	// DEĞİL ve olmamalı: bu bir GAUGE, "şu anki doluluk". 24 saatlik bir
	// pencereye ortalanmış doygunluk, tam da görülmesi gereken zirveyi
	// saklardı. Frontend bu sayıyı karoda yazıyor ki "son 10 dakika" ile
	// sayfanın range'i karıştırılmasın.
	LookbackSeconds int `json:"lookbackSeconds"`
}

// dbSaturationCheck — bir kontrolün tanımı. read fonksiyonu evaluator'ın
// capacityChecks tablosuyla AYNI chstore çağrılarını yapıyor; metrik
// adları tek yerde değil ama İKİ yerde olması bilinçli değil, sadece
// evaluator'ın tablosu private ve orası karar katmanı. Adlar burada
// değişirse orada da değişmeli — test ikisini karşılaştırıyor.
type dbSaturationCheck struct {
	prefix string // metric_catalog kapısı ("oracledb." / "postgresql." / "mysql.")
	system string // DBInstance.System ile aynı kelime (join için ŞART)
	check  string
	read   func(ctx context.Context, s *Store) ([]CapacitySample, error)
}

// dbSaturationChecks — açılan kontroller. Redis eviction BİLEREK YOK:
// o bir RATE, tavanı olmayan bir sayaç (maxmemory-policy siliyor) ve
// yüzde olarak ifade edilemez. Bir "% doygunluk" karosunda yer alsaydı
// ya sahte bir tavan uydurmamız ya da karonun anlamını bulanıklaştırmamız
// gerekirdi. Redis baskısı kendi paneline ve alarmına ait.
var dbSaturationChecks = []dbSaturationCheck{
	{prefix: "oracledb.", system: "oracle", check: "sessions",
		read: func(ctx context.Context, s *Store) ([]CapacitySample, error) {
			return s.UsageLimit(ctx, "oracledb.sessions.usage", "oracledb.sessions.limit")
		}},
	{prefix: "oracledb.", system: "oracle", check: "processes",
		read: func(ctx context.Context, s *Store) ([]CapacitySample, error) {
			return s.UsageLimit(ctx, "oracledb.processes.usage", "oracledb.processes.limit")
		}},
	// Tablespace BOYUTLU (tablespace_name) — DimensionedUsageLimit'in
	// tek çağrı yeri ve Oracle'ın bir numaralı düşme sebebi.
	{prefix: "oracledb.", system: "oracle", check: "tablespace",
		read: func(ctx context.Context, s *Store) ([]CapacitySample, error) {
			return s.DimensionedUsageLimit(ctx,
				"oracledb.tablespace_size.usage", "oracledb.tablespace_size.limit", "tablespace_name")
		}},
	{prefix: "postgresql.", system: "postgresql", check: "connections",
		read: func(ctx context.Context, s *Store) ([]CapacitySample, error) {
			return s.UsageLimit(ctx, "postgresql.backends", "postgresql.connection.max")
		}},
	{prefix: "mysql.", system: "mysql", check: "connections",
		read: func(ctx context.Context, s *Store) ([]CapacitySample, error) {
			return s.UsageLimit(ctx, "mysql.connection.count", "mysql.max_used_connections")
		}},
}

// saturationPct — yüzde hesabı. SAF.
//
// limit <= 0 → (0, false): tavansız bir kullanım yüzdeye çevrilemez.
// Okuma katmanı bunları zaten eliyor (UsageLimit `c.Limit <= 0` atıyor);
// bu ikinci kapı, o davranış bir gün değişirse sayfada Inf görünmesin
// diye. NaN/Inf JSON'a çıkarsa frontend'i patlatır (v0.5.301 sınıfı).
func saturationPct(usage, limit float64) (float64, bool) {
	if !(limit > 0) {
		return 0, false
	}
	pct := usage / limit * 100
	if pct < 0 {
		return 0, false // negatif kullanım bir ölçüm değil
	}
	return pct, true
}

// GetDBSaturation — havuz doygunluğu okuması.
//
// PENCERE ALMAZ. Bilinçli: gauge'lar "şu anki doluluk" anlatıyor ve
// okuma katmanı zaten son capacityWindow (10 dk) içindeki EN SON değeri
// alıyor. Sayfa penceresine bağlansaydı 24 saatlik bir seçimde karo,
// görülmesi gereken zirveyi ortalayarak saklardı.
func (s *Store) GetDBSaturation(ctx context.Context) (*DBSaturation, error) {
	out := &DBSaturation{
		Rows:            []DBSaturationRow{},
		LookbackSeconds: int(capacityWindow.Seconds()),
	}
	// Motor ailesi başına TEK katalog kapısı — aynı önekin üç kontrolü
	// için üç kez sormaya gerek yok.
	gate := map[string]bool{}
	for _, c := range dbSaturationChecks {
		active, known := gate[c.prefix]
		if !known {
			active = s.receiverPrefixActive(ctx, c.prefix)
			gate[c.prefix] = active
		}
		if !active {
			continue
		}
		samples, err := c.read(ctx, s)
		if err != nil {
			// Bir kontrolün patlaması diğerlerini düşürmez: eksik bir
			// satır, hiç karo olmamasından iyidir ve karo zaten yalnız
			// dönen satırları anlatıyor.
			continue
		}
		for _, sm := range samples {
			pct, ok := saturationPct(sm.Usage, sm.Limit)
			if !ok {
				continue
			}
			out.Rows = append(out.Rows, DBSaturationRow{
				System:   c.system,
				Instance: sm.Instance,
				Check:    c.check,
				Subkey:   sm.Subkey,
				Usage:    sm.Usage,
				Limit:    sm.Limit,
				Pct:      pct,
			})
		}
	}
	return out, nil
}
