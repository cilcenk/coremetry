package influx

// secret.go — tokenRef çözümü (v0.10.222, audit K5).
//
// system_settings'e TOKEN DEĞİL REFERANS yazılır; referans her kullanımda
// (poll, test, snapshot rozeti) yeniden çözülür — rotasyon = Secret'ı
// değiştir, pod'u yeniden başlat (env) ya da dosyayı güncelle (file,
// anında). Prod besleme: Helm `extraEnv` + `existingSecret`
// (charts/coremetry/values.yaml). Düz token hiçbir yolda kabul edilmez
// (Normalize → ValidTokenRef).

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	envRefRe  = regexp.MustCompile(`^env:[A-Za-z_][A-Za-z0-9_]*$`)
	fileRefRe = regexp.MustCompile(`^file:/[^\s]+$`)
)

// ValidTokenRef — `env:NAME` (POSIX env adı) ya da `file:/mutlak/yol`.
func ValidTokenRef(ref string) bool {
	return envRefRe.MatchString(ref) || fileRefRe.MatchString(ref)
}

// ResolveTokenRef — os üzerinden çözer (üretim yolu).
func ResolveTokenRef(ref string) (string, error) {
	return resolveTokenRef(ref, osGetenv, osReadFile)
}

func resolveTokenRef(ref string, getenv func(string) string, readFile func(string) ([]byte, error)) (string, error) {
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
		// `kubectl create secret --from-file` sonda newline bırakır.
		v := strings.TrimSpace(string(b))
		if v == "" {
			return "", fmt.Errorf("tokenRef %s: dosya boş", ref)
		}
		return v, nil
	default:
		return "", fmt.Errorf("tokenRef %q: şema `env:NAME` ya da `file:/path` olmalı", ref)
	}
}
