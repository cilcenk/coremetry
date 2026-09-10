package devops

// version_ref.go — v0.10.590. Olay anındaki sürümü (image tag / service.version)
// VCS'te bir ref'e, oradan bir COMMIT'e bağlar. Bugüne dek kod branşın
// UCUndan geliyordu ve K2 uyarısı her linkteydi; çözülebilen sürümde link
// ve dosya ağacı AYNI commit'ten gider, uyarı "doğrulandı"ya döner.
// Çözülemeyen sürümde bugünkü davranış aynen kalır — hiçbir şey gerilemez.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// RefSpec — Azure DevOps versionDescriptor: branch | tag | commit.
type RefSpec struct {
	Kind string
	Name string
}

// DefaultVersionRef — ayar boşsa. En yaygın kural: image tag'i ile aynı adlı
// git tag'i. Bulunamazsa branş ucuna düşülür (uyarıyla); yani yanlış bir
// varsayılanın bedeli bir ekstra refs isteğidir, yanlış link değil.
const DefaultVersionRef = "tags/{version}"

// versionPlaceholders — chstore/deploys.go placeholderVersionList'in Go
// ikizi (devops chstore'a bağımlı değil). Yer tutucu bir sürümü ref'e
// bağlamak yanlış tag'e bağlanmaktır; bağlanmamaktan kötü.
var versionPlaceholders = map[string]bool{
	"": true, "0.0.1": true, "0.0.1-SNAPSHOT": true, "0.1.0-SNAPSHOT": true,
	"1.0-SNAPSHOT": true, "1.0.0-SNAPSHOT": true, "${project.version}": true,
	"${version}": true, "unknown": true, "latest": true, "dev": true, "none": true,
}

// IsPlaceholderVersion — yer tutucu / SNAPSHOT / boş.
func IsPlaceholderVersion(v string) bool {
	v = strings.TrimSpace(v)
	return versionPlaceholders[v] || strings.Contains(strings.ToUpper(v), "SNAPSHOT")
}

// NormalizeVersionRef — ayar deseni: boş → varsayılan; `{version}` taşımalı;
// `tags/` ya da `heads/` ile başlamalı (refs/ öneki YAZILMAZ, API filter
// biçimi). Geçersiz desen 400 — sessizce varsayılana düşmek operatörün
// yazdığını yok saymak olurdu.
func NormalizeVersionRef(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return DefaultVersionRef, nil
	}
	if !strings.Contains(p, "{version}") {
		return "", errors.New("desen {version} yer tutucusunu taşımalı")
	}
	if !strings.HasPrefix(p, "tags/") && !strings.HasPrefix(p, "heads/") {
		return "", errors.New("desen tags/ ya da heads/ ile başlamalı (refs/ öneksiz)")
	}
	return p, nil
}

// ResolveVersionRef — desen + sürüm → ref adı ("tags/release.1"). Yer tutucu
// sürüm ref ÜRETMEZ.
func ResolveVersionRef(pattern, version string) (string, bool) {
	p, err := NormalizeVersionRef(pattern)
	if err != nil {
		return "", false
	}
	version = strings.TrimSpace(version)
	if IsPlaceholderVersion(version) {
		return "", false
	}
	return strings.ReplaceAll(p, "{version}", version), true
}

// refCommitFromBody — SAF: refs cevabından TAM ad eşleşmesiyle commit SHA.
// Annotated tag'de objectId TAG NESNESİDİR, commit peeledObjectId'dedir —
// o yüzden peeled önce. Önek eşleşmesi kabul edilmez: release ≠ release.1.
func refCommitFromBody(body []byte, want string) (string, bool) {
	var rr struct {
		Value []struct {
			Name     string `json:"name"`
			ObjectID string `json:"objectId"`
			Peeled   string `json:"peeledObjectId"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &rr); err != nil {
		return "", false
	}
	full := "refs/" + strings.TrimPrefix(want, "refs/")
	for _, r := range rr.Value {
		if r.Name != full {
			continue
		}
		if r.Peeled != "" {
			return r.Peeled, true
		}
		if r.ObjectID != "" {
			return r.ObjectID, true
		}
	}
	return "", false
}

// refCommit — `refs?filter=<ref>` ile tek ref'in commit'i. Bulunamazsa ("",
// nil): yokluk hata değil, "bu sürümün tag'i yok" bilgisidir.
func (s *Service) refCommit(ctx context.Context, cli *http.Client, cfg Settings, ver, repo, ref string) (string, error) {
	prefix, name := ref, ""
	if i := strings.Index(ref, "/"); i > 0 {
		prefix, name = ref[:i+1], ref[i+1:]
	}
	u := repoURL(cfg, repo) + "/refs?filter=" + prefix + url.PathEscape(name) + "&api-version=" + ver
	body, err := doGet(ctx, cli, u, cfg)
	if err != nil {
		return "", err
	}
	sha, ok := refCommitFromBody(body, ref)
	if !ok {
		return "", nil
	}
	return sha, nil
}

// refCacheName — ağaç cache anahtarı için ref adı. Branş bugünkü anahtarı
// AYNEN korur (mevcut cache/pin testleri bozulmasın); diğer türler ayrık.
func refCacheName(ref RefSpec) string {
	if ref.Kind == "" || ref.Kind == "branch" {
		return ref.Name
	}
	return "\x02" + ref.Kind + ":" + ref.Name
}
