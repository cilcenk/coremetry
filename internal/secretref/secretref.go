// Package secretref — v0.10.271 (kuyruk 7 dilim 1): system_settings'e secret
// DEĞİL referans yazma sözleşmesi. Influx'ta v0.10.222 ile doğdu
// (internal/influx/secret.go, audit K5); Tempo / Remote Clusters /
// VictoriaMetrics'e yayılırken TEK kopya buraya taşındı — influx sarmalayıcı
// olarak kaldı (davranış bayt-bayt aynı, influx testleri değişmedi).
//
//	env:NAME     → ortam değişkeni (Helm extraEnv + existingSecret)
//	file:/path   → dosya içeriği (mount edilmiş Secret); sondaki boşluk/newline
//	               kırpılır (kubectl create secret --from-file newline bırakır)
//
// Düz token hiçbir yolda kabul edilmez (Valid). Çözüm kullanım anında ya da
// entegrasyonun Configure/yenileme döngüsünde (sıcak yolda IO yok) yapılır;
// rotasyon = Secret'ı değiştir (env: pod restart, file: dosyayı güncelle).
package secretref

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var (
	envRefRe  = regexp.MustCompile(`^env:[A-Za-z_][A-Za-z0-9_]*$`)
	fileRefRe = regexp.MustCompile(`^file:/[^\s]+$`)
)

// Valid — `env:NAME` (POSIX env adı) ya da `file:/mutlak/yol`.
func Valid(ref string) bool {
	return envRefRe.MatchString(ref) || fileRefRe.MatchString(ref)
}

// Resolve — os üzerinden çözer (üretim yolu).
func Resolve(ref string) (string, error) {
	return ResolveWith(ref, os.Getenv, os.ReadFile)
}

// ResolveWith — enjekte edilebilir getenv/readFile ile çözer (testler).
func ResolveWith(ref string, getenv func(string) string, readFile func(string) ([]byte, error)) (string, error) {
	switch {
	case envRefRe.MatchString(ref):
		v := strings.TrimSpace(getenv(strings.TrimPrefix(ref, "env:")))
		if v == "" {
			return "", fmt.Errorf("tokenRef %s: ortam değişkeni boş ya da tanımsız", ref)
		}
		return v, nil
	case fileRefRe.MatchString(ref):
		b, err := readFile(strings.TrimPrefix(ref, "file:"))
		if err != nil {
			return "", fmt.Errorf("tokenRef %s: %w", ref, err)
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return "", fmt.Errorf("tokenRef %s: dosya boş", ref)
		}
		return v, nil
	default:
		return "", fmt.Errorf("tokenRef %q: şema `env:NAME` ya da `file:/path` olmalı", ref)
	}
}

// InvalidMessage — API 400 metni (influx_routes.go sözleşmesi).
const InvalidMessage = "tokenRef `env:NAME` ya da `file:/path` biçiminde olmalı (düz token saklanmaz)"
