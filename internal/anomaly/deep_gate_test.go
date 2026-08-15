package anomaly

import "testing"

// v0.9.1060 (Faz 1.5 / K10) regresyon pini — derin soruşturma kapısı
// "P1 VEYA deploy-korelasyonlu". Eski hâl yalnız P1'di: deploy-
// korelasyonlu bir critical, bağımsız olarak 2× aşmadıkça derin kanıt
// alamıyordu — fuser'ın en güçlü şüpheli sınıfında (deploy, 0.80 taban)
// soruşturma hiç koşmuyordu. Bu tablo dört hücreyi de mühürler; kapının
// sessizce yeniden daralması (yalnız-P1) ya da koşulsuz açılması
// (maliyet kapısının ölmesi) burada patlar.
func TestShouldDeepInvestigate(t *testing.T) {
	cases := []struct {
		name           string
		isP1, hasDep   bool
		want           bool
	}{
		{"P1 + deploy", true, true, true},
		{"P1, deploysuz", true, false, true},
		{"P1 değil ama deploy-korelasyonlu", false, true, true},
		{"ne P1 ne deploy → koşmaz (maliyet kapısı yaşıyor)", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldDeepInvestigate(tc.isP1, tc.hasDep); got != tc.want {
				t.Fatalf("shouldDeepInvestigate(%v, %v) = %v, want %v",
					tc.isP1, tc.hasDep, got, tc.want)
			}
		})
	}
}
