// v0.9.628 — semconv sözlüğü 2023'te donmuştu.
//
// convertSpan tipli kolonları YALNIZ eski OTel adlarından okuyordu:
// http.method, http.status_code, db.system, db.statement. OTel bu
// dördünü ≥1.23'te yeniden adlandırdı ve yeni SDK'lar (Java agent 2.x,
// .NET, Node 0.5x) YALNIZ yeni adı basıyor. Sonuç: modern
// enstrümantasyonlu bir servisin http_method / http_status / db_system /
// db_statement kolonları BOŞ kalıyor — ve onların üzerine kurulu her şey
// (endpoint listeleri, DB özetleri, HTTP hata sınıflandırması, topoloji
// db kenarları) o servisi hiç görmüyor.
//
// Bu tam olarak v0.9.71'in yaşandığı sınıf: operatör "url.path'ler
// /endpoints'te görünmüyor" dedi, sebep http.target'ın url.path'e
// bölünmesiydi ve /endpoints o servisleri hiç listelemiyordu. Aynı
// hata, dört alan daha.
//
// ÇÖZÜM ŞEKLİ, EMSALLERİN AYNISI (deployEnv v0.8.379, httpRoute v0.9.71):
// çok-adlı geri düşme zinciri. YENİ ad önce denenir, yoksa ESKİ ad.
//
//   - ALTER yok, yeni kolon yok → dağıtık-güvenli gün bir
//     (feedback-distributed-column-safety).
//   - Yalnız YENİ span'leri etkiler; eski veri olduğu gibi kalır.
//   - İki ad birden gelirse yeni kazanır — çift yazan SDK'larda
//     ikisi de aynı değeri taşır.
//
// KAPSAM DIŞI BIRAKILANLAR ve nedenleri:
//
//   - peer.service → server.address: SEMANTİK FARKLI. peer.service
//     mantıksal servis ADI, server.address bir host. İkincisini
//     peer_service'e yazmak topoloji grafiğinin düğüm adlarını servis
//     adından hostname'e çevirirdi — görünür bir davranış değişikliği,
//     ayrı bir karar hak ediyor.
//   - url.full / client.address / db.operation.name / db.namespace:
//     karşılık gelen TİPLİ KOLON YOK. Attribute dizilerinde zaten
//     duruyorlar; kolon olmadan eşleyecek bir yer de yok.
package otlp

import (
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// spanAttrAliases — tipli kolon başına kabul edilen attribute adları,
// ÖNCELİK SIRASIYLA (yeni semconv → eski).
//
// Okuma tarafındaki eşleşiği internal/chstore: wellKnown (filtre) ve
// WellKnownTraceCol (gösterim). ÜÇÜ BİRLİKTE güncellenmeli — bu
// oturumun tekrar eden hatası "bir kural iki yere bölünüp ayrışıyor";
// semconv_alias_test.go üçünün ayrışmadığını çiviliyor.
var spanAttrAliases = map[string][]string{
	// OTel ≥1.23: http.method → http.request.method
	"http_method": {"http.request.method", "http.method"},
	// OTel ≥1.23: http.status_code → http.response.status_code
	"http_status": {"http.response.status_code", "http.status_code"},
	// OTel ≥1.30: db.system → db.system.name
	"db_system": {"db.system.name", "db.system"},
	// OTel ≥1.25: db.statement → db.query.text
	"db_statement": {"db.query.text", "db.statement"},
}

// attrFirst — listedeki İLK boş-olmayan değeri döndürür. SAF (tablo
// testli).
//
// Boş-olmayan şartı önemli: bir SDK yeni anahtarı BOŞ değerle basarsa
// (bazı otomatik enstrümantasyonlar yapıyor), eski anahtarın dolu
// değerine düşülmeli. "Anahtar var" ile "değer var" farklı şeyler —
// bu oturumda tam bu ayrımı bir kez kaçırdım (kolonun VAR olduğunu
// DOLU olduğu sandım).
func attrFirst(attrs []*commonpb.KeyValue, keys ...string) string {
	for _, k := range keys {
		if v := attrStr(attrs, k, ""); v != "" {
			return v
		}
	}
	return ""
}

// attrIntFirst — attrFirst'in tamsayı ikizi. 0 "yok" sayılır: HTTP
// durum kodu 0 geçerli bir değer değil.
func attrIntFirst(attrs []*commonpb.KeyValue, keys ...string) int64 {
	for _, k := range keys {
		if v := attrInt(attrs, k, 0); v != 0 {
			return v
		}
	}
	return 0
}
