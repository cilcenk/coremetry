// v0.9.629 — demo filosu KARIŞIK semconv konuşur.
//
// v0.9.628 ingest'in yalnız 2023 adlarını okuduğunu düzeltti
// (http.method → http.request.method, http.status_code →
// http.response.status_code, db.system → db.system.name, db.statement →
// db.query.text). O hata da lokalde YAPISAL OLARAK GÖRÜNEMİYORDU: demo
// üreteçlerinin HEPSİ eski adları basıyordu, dolayısıyla modern bir
// SDK'nın kolonları boş bırakması hiçbir yerel testte ortaya çıkmıyordu.
//
// Gerçek bir filo tek bir semconv sürümünde değildir: bazı servisler
// Java agent 2.x / .NET / Node 0.5x ile modern adları, kalanlar eski
// ajanlarla eski adları basar. Demo bunu taklit etmezse "her iki adı da
// oku" kuralı bir daha asla sınanmaz.
//
// SEÇİM DETERMİNİSTİK: servis adının hash'i. Rastgele olsaydı aynı
// servis bir trace'te modern, ötekinde eski konuşurdu — gerçek bir
// dağıtımda olmayan bir şey, ve hata ayıklarken yanıltıcı.
package main

import (
	"hash/fnv"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// modernSemconvKeys — eski ad → v0.9.628'in tanıdığı yeni ad.
// Anahtarlar internal/otlp/semconv.go'daki spanAttrAliases'in ESKİ
// yarısıyla birebir; ikisi ayrışırsa demo, düzeltmenin sınamadığı bir
// şeyi üretir.
var modernSemconvKeys = map[string]string{
	"http.method":      "http.request.method",
	"http.status_code": "http.response.status_code",
	"db.system":        "db.system.name",
	"db.statement":     "db.query.text",
}

// serviceSpeaksModernSemconv — bu servis yeni adları mı basıyor?
//
// Filonun kabaca üçte biri modern. Oran bilinçli: hepsi modern olsaydı
// eski yol sınanmazdı, hiçbiri olmasaydı yeni yol sınanmazdı — düzeltme
// ikisini de tanımak zorunda.
func serviceSpeaksModernSemconv(service string) bool {
	h := fnv.New32a()
	_, _ = h.Write([]byte(service))
	return h.Sum32()%3 == 0
}

// applySemconvDialect — modern konuşan servislerin attribute adlarını
// çevirir. Değerler DEĞİŞMEZ, yalnız anahtar adı.
//
// Yeni bir liste döndürür; çağıranın map'i / dilimi mutasyona uğramaz
// (kv(...) haritaları çağrı yerlerinde paylaşılabiliyor).
func applySemconvDialect(service string, kvs []*commonpb.KeyValue) []*commonpb.KeyValue {
	if !serviceSpeaksModernSemconv(service) {
		return kvs
	}
	out := make([]*commonpb.KeyValue, len(kvs))
	for i, kv := range kvs {
		if newKey, ok := modernSemconvKeys[kv.Key]; ok {
			clone := *kv
			clone.Key = newKey
			out[i] = &clone
			continue
		}
		out[i] = kv
	}
	return out
}
