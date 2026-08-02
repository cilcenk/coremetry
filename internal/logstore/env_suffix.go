package logstore

import "strings"

// stripLogEnvSuffix — servis adının SONUNDAKİ bilinen ortam ekini soyar.
//
// v0.9.545, operator-reported (prod, ES zemin gerçeğiyle doğrulandı):
// BFF servislerinin Logs sekmesi tamamen boştu. Sebep aranan ALAN değil,
// aranan DEĞER — servis adı env ekli ama log dokümanı eksiz taşıyor:
//
//	service (Coremetry)       : mobile-overview-bff-prod
//	kubernetes.container_name : mobile-overview-bff
//	kubernetes.labels.app     : mobile-overview-bff
//	kubernetes.namespace_name : mobile-bff-prod      ← ek BURADA
//
// Yani filo, ortam bilgisini NAMESPACE'e yazıyor; iş yükü adı eksiz.
// Aynı adlandırma boşluğunun pod-eşleştirme tarafındaki ikizi
// v0.9.535'te kapatılmıştı (frontend stripEnvSuffix) — bu onun log
// tarafı ve BİLEREK aynı ek listesini kullanıyor: iki yüzey aynı
// servisi farklı adlarla aramamalı.
//
// Yalnız KUYRUKTAKİ bilinen ekler soyulur. Ad ortasındaki "prod"
// (bsa-digital-limitcore-prod-oneagent) ve bilinmeyen varyantlar
// ("-production") dokunulmadan kalır — soyma ne kadar gevşek olursa
// yanlış servisin logunu getirme riski o kadar büyür.
//
// Yalnız ekten ibaret ad ("-prod") soyulmaz: boş bir arama terimi
// üretmek, filtreyi sessizce kaldırmak demek olurdu.
func stripLogEnvSuffix(service string) string {
	for _, suf := range logEnvSuffixes {
		if len(service) > len(suf) && strings.HasSuffix(service, suf) {
			return service[:len(service)-len(suf)]
		}
	}
	return service
}

// logEnvSuffixes — frontend podWorkload.ts'teki ENV_SUFFIXES ile AYNI
// küme. Ayrışırlarsa aynı servis pod yüzeyinde bulunup log yüzeyinde
// bulunamaz (ya da tersi) — sessiz ve teşhisi zor bir tutarsızlık.
var logEnvSuffixes = []string{"-prod", "-int", "-uat", "-prep"}
