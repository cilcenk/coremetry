package influx

// secret.go — tokenRef çözümü (v0.10.222, audit K5).
//
// system_settings'e TOKEN DEĞİL REFERANS yazılır; referans her kullanımda
// (poll, test, snapshot rozeti) yeniden çözülür — rotasyon = Secret'ı
// değiştir, pod'u yeniden başlat (env) ya da dosyayı güncelle (file,
// anında). Prod besleme: Helm `extraEnv` + `existingSecret`
// (charts/coremetry/values.yaml). Düz token hiçbir yolda kabul edilmez
// (Normalize → ValidTokenRef).

import "github.com/cilcenk/coremetry/internal/secretref"

// v0.10.271 — çözücü internal/secretref'e taşındı; bu sarmalayıcılar
// influx'un mevcut çağrı yerleri ve testleri için aynen kalır.

// ValidTokenRef — `env:NAME` (POSIX env adı) ya da `file:/mutlak/yol`.
func ValidTokenRef(ref string) bool { return secretref.Valid(ref) }

// ResolveTokenRef — os üzerinden çözer (üretim yolu).
func ResolveTokenRef(ref string) (string, error) { return secretref.Resolve(ref) }

func resolveTokenRef(ref string, getenv func(string) string, readFile func(string) ([]byte, error)) (string, error) {
	return secretref.ResolveWith(ref, getenv, readFile)
}
