package chstore

import (
	"strings"
	"testing"
)

// v0.9.1053 (Faz 0.2) regresyon pini — P1 derin kanıtının exception
// ailesi INCIDENT PENCERESİNE bakar. Eski hâl ömür-boyu last_seen
// sırasıyla ilk 5 grubu alıyordu: 40 dk önceki incident'ın kanıtına
// bugünün grupları yazılıyor, denetim izi Found:true diyordu. Bu tablo
// pencere-örtüşme filtresinin SQL şeklini ve sıfır=kapalı sözleşmesini
// mühürler.
func TestExceptionGroupWhereActiveWindow(t *testing.T) {
	t.Run("pencere verilince örtüşme filtresi SQL'de", func(t *testing.T) {
		wc := buildExceptionGroupWhere(ExceptionGroupFilter{
			Service: "svc", ActiveFromNs: 100, ActiveToNs: 200,
		})
		sql := wc.sql()
		if !strings.Contains(sql, "last_seen >= fromUnixTimestamp64Nano(?)") {
			t.Fatalf("last_seen alt sınırı yok:\n%s", sql)
		}
		if !strings.Contains(sql, "first_seen <= fromUnixTimestamp64Nano(?)") {
			t.Fatalf("first_seen üst sınırı yok:\n%s", sql)
		}
	})

	t.Run("sıfır = filtre yok (mevcut çağıranlar bayt-bayt eski)", func(t *testing.T) {
		wc := buildExceptionGroupWhere(ExceptionGroupFilter{Service: "svc"})
		sql := wc.sql()
		if strings.Contains(sql, "last_seen >=") || strings.Contains(sql, "first_seen <=") {
			t.Fatalf("pencere istenmeden pencere filtresi girdi:\n%s", sql)
		}
	})
}
