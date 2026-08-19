package api

import "time"

// sparkBinPlan — /api/spanmetrics/services sparkline'ının bin ızgarası.
// SAF, tablo-testli (sparkbin_test.go): aritmetiği handler gövdesinden
// çıkarmanın tek sebebi bu — yüklem ile binlemenin AYNI anı referans
// aldığını gerçekten koşarak doğrulayabilmek, kaynağa bakarak değil.
//
// Sözleşme (v0.9.1176): sorgunun ALDIĞI her MV kovası geçerli bir bine
// düşer. WHERE `time_bucket >= bucketStart AND time_bucket < to` diyor,
// dolayısıyla binler de tam olarak [bucketStart, to) aralığını örter:
// origin = bucketStart, genişlik = (to - bucketStart)/bins.
//
// Öncesi origin olarak ham `from`u kullanıyordu ve bu iki uçtan da
// yanlıştı:
//   - from hizasızken bucketStart'taki kova b = -1 üretip
//     `range(0, bins)` dışında kalıyor, sparkline'dan düşüyor, ama
//     Stage-1'in `calls` toplamına giriyordu (satır ile seri ayrışması —
//     v0.9.1169/1170'te üst uçta düzeltilen sınıfın alt uç ikizi),
//   - genişlik (to - from)/bins olduğu için binler [bucketStart, to)
//     aralığından DAR kalıyordu; origin'i düzeltip genişliği bırakmak bu
//     sefer son kovayı taşırırdı. İkisi birlikte değişmeli.
//
// Dejenere pencereler: bins < 1 ise 1'e çekilir; genişlik 0 ya da negatif
// çıkarsa 1 ns'e sabitlenir. Her iki durumda da alınabilecek TEK kova
// bucketStart'tır ve b = 0 çıkar, yani sözleşme bozulmaz.
func sparkBinPlan(bucketStart, to time.Time, bins int) (originNs, widthNs int64) {
	if bins < 1 {
		bins = 1
	}
	originNs = bucketStart.UnixNano()
	widthNs = (to.UnixNano() - originNs) / int64(bins)
	if widthNs <= 0 {
		widthNs = 1
	}
	return originNs, widthNs
}

// sparkBinIndex — SQL'deki `intDiv(toUnixTimestamp(time_bucket) *
// 1000000000 - ?, ?)` ifadesinin Go ikizi. Yalnız test bunu çağırır;
// üretim yolu ifadeyi ClickHouse'ta hesaplar, dolayısıyla bu fonksiyon
// SQL'in ne yaptığının yazılı beyanıdır ve testin ölçtüğü şey odur.
func sparkBinIndex(bucket time.Time, originNs, widthNs int64) int64 {
	return (bucket.UnixNano() - originNs) / widthNs
}
