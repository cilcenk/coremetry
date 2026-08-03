// v0.9.609 — altyapı-ölümcül exception'lar.
//
// Operatör (prod trace'i, 2026-08-03): bir servis bağımlılığına
// ulaşamıyor ve `java.net.UnknownHostException` alıyor — hedef bir
// Kubernetes servis adı (`*.svc.cluster.local`). İstek: "varsa çok
// kritik, hemen P1 üret".
//
// NEDEN AYRI BİR SINIF: bu, "hedef ayakta değil" demek DEĞİL.
// ConnectException hedefin var olduğunu ama cevap vermediğini söyler —
// çoğu zaman geçicidir, yeniden deneme düzeltir, pod yeniden başlarken
// normaldir. UnknownHostException ise İSMİN HİÇ ÇÖZÜLMEDİĞİNİ söyler:
// yanlış yapılandırma, silinmiş servis, yanlış namespace, bozuk DNS.
//
// Üç özelliği onu P1 yapıyor:
//   - yeniden deneme DÜZELTMEZ (isim yok, bekleyerek var olmaz)
//   - kendiliğinden GEÇMEZ (biri bir şey değiştirene kadar sürer)
//   - genelde TÜM çağrıları etkiler, bir kısmını değil
//
// Bu yüzden eşik 1: patlama beklemek yok. Tek oluşum yeter — çünkü tek
// oluşum zaten "bu bağımlılık hiç çözülemiyor" demek.
package chstore

import (
	"context"
	"strings"
	"time"
)

// FatalExceptionType — altyapı-ölümcül exception sınıfları.
//
// Eşleşme SONEK üzerinden: `java.net.UnknownHostException` da
// `UnknownHostException` da yakalanır, paket adı ne olursa olsun.
// Dil-bağımsız kalması da bunu gerektiriyor — .NET, Go ve Python
// karşılıkları farklı isimlerle gelir.
//
// Liste KASTEN kısa. Her ekleme "yeniden deneme düzeltmez VE
// kendiliğinden geçmez" testini geçmeli; geçmeyen bir tip buraya
// girerse P1 kavramı değersizleşir — operatörün P1'e verdiği tepki
// onun ne kadar seyrek olduğuna bağlı.
var fatalExceptionSuffixes = []string{
	"UnknownHostException", // DNS / servis keşfi: isim hiç çözülmüyor
}

// IsFatalExceptionType — bu exception tipi altyapı-ölümcül mü? SAF.
func IsFatalExceptionType(exType string) bool {
	t := strings.TrimSpace(exType)
	if t == "" {
		return false
	}
	for _, suf := range fatalExceptionSuffixes {
		if strings.HasSuffix(t, suf) {
			return true
		}
	}
	return false
}

// FatalException — bir (tip, servis) çifti için ölümcül exception hâli.
//
// Bu bir OLAY değil DURUM: patlama gibi başlayıp biten bir şey değil,
// biri düzeltene kadar süren bir koşul. Bu yüzden kimlik zaman kovası
// TAŞIMAZ (paylaşılan-bağımlılık dedektörünün aksine) — aynı servis
// aynı hostu çözemediği sürece AYNI problemdir.
type FatalException struct {
	Type      string `json:"type"`
	Service   string `json:"service"`
	Message   string `json:"message"`
	FirstSeen int64  `json:"firstSeen"`
	LastSeen  int64  `json:"lastSeen"`
	Count     uint64 `json:"occurrences"`
}

// FindFatalExceptions — penceredeki altyapı-ölümcül exception'lar.
//
// Tazelik kapısı LAST_SEEN'de (v0.9.576 dersi): first_seen kullansaydık
// (a) uzun süren bir arıza pencereden düşüp hiç görünmez, (b) daha
// kötüsü, KAPANIŞ görünürlüğe bağlı kalırdı — dedektör "artık aktif
// değil" kararını ancak koşulu GÖREREK verebilir.
//
// Bounded: state tablosu, zaman pencereli WHERE, LIMIT,
// max_execution_time. FINAL şart (ReplacingMergeTree).
func (s *Store) FindFatalExceptions(ctx context.Context, lookback time.Duration) ([]FatalException, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT ex_type, service, any(ex_message) AS msg,
		       toUnixTimestamp64Nano(min(first_seen)) AS first_ns,
		       toUnixTimestamp64Nano(max(last_seen))  AS last_ns,
		       sum(occurrences)                       AS occ
		FROM exception_groups FINAL
		WHERE last_seen >= ? AND ex_type != ''
		  -- 'ignored' operatörün açık kararı: bu tipi görmek istemiyor.
		  -- P1 bile olsa o kararı ezmiyoruz.
		  AND state != 'ignored'
		GROUP BY ex_type, service
		ORDER BY last_ns DESC
		LIMIT 500
		SETTINGS max_execution_time = 10`,
		chDateTime64Arg(time.Now().Add(-lookback)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FatalException
	for rows.Next() {
		var f FatalException
		if err := rows.Scan(&f.Type, &f.Service, &f.Message,
			&f.FirstSeen, &f.LastSeen, &f.Count); err != nil {
			return nil, err
		}
		// Süzme GO tarafında: sonek eşleşmesi SQL'de LIKE zinciri
		// olurdu ve her yeni tip sorguyu uzatırdı. Aday kümesi zaten
		// LIMIT'li ve küçük.
		if IsFatalExceptionType(f.Type) {
			out = append(out, f)
		}
	}
	return out, rows.Err()
}
