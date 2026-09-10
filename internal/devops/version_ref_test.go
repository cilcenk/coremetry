package devops

import (
	"strings"
	"testing"
)

func containsStr(s, sub string) bool { return strings.Contains(s, sub) }

// version_ref_test.go — v0.10.590: olay anındaki sürümü VCS ref'ine bağlama.
//
// K2 kuralı (operatör): link olay anındaki revizyon üzerinden; doğrulanamazsa
// görünür uyarı. Bugüne dek revizyon çözümleme YOKTU, uyarı her linkteydi.
// Sözleşme:
//   - desen `{version}` taşımalı ve tags/ ya da heads/ ile başlamalı; boş →
//     varsayılan tags/{version}
//   - yer tutucu sürüm (boş, SNAPSHOT, ${version}, latest…) ref ÜRETMEZ —
//     yanlış tag'e bağlanmak bağlanmamaktan kötü
//   - refs cevabında annotated tag için peeledObjectId (commit) objectId'nin
//     (tag nesnesi) ÖNÜNDE; ad tam eşleşir (önek değil)
func TestResolveVersionRef(t *testing.T) {
	cases := []struct {
		pattern, version, want string
		ok                     bool
	}{
		{"tags/{version}", "release.20260817.1", "tags/release.20260817.1", true},
		{"heads/release/{version}", "1.2.3", "heads/release/1.2.3", true},
		{"", "1.2.3", "tags/1.2.3", true}, // boş desen = varsayılan
		{"tags/{version}", "", "", false},
		{"tags/{version}", "latest", "", false},
		{"tags/{version}", "1.0.0-SNAPSHOT", "", false},
		{"tags/{version}", "${project.version}", "", false},
	}
	for _, c := range cases {
		got, ok := ResolveVersionRef(c.pattern, c.version)
		if ok != c.ok || got != c.want {
			t.Errorf("(%q,%q) → %q,%v; beklenen %q,%v", c.pattern, c.version, got, ok, c.want, c.ok)
		}
	}
}

func TestNormalizeVersionRef(t *testing.T) {
	if v, err := NormalizeVersionRef("  "); err != nil || v != DefaultVersionRef {
		t.Fatalf("boş → varsayılan: %q %v", v, err)
	}
	for _, bad := range []string{"tags/release", "refs/tags/{version}", "commits/{version}", "{version}"} {
		if _, err := NormalizeVersionRef(bad); err == nil {
			t.Errorf("%q reddedilmeliydi", bad)
		}
	}
	if v, err := NormalizeVersionRef(" heads/release/{version} "); err != nil || v != "heads/release/{version}" {
		t.Fatalf("kırpılmış geçerli desen: %q %v", v, err)
	}
}

func TestRefCommitFromBody(t *testing.T) {
	body := []byte(`{"value":[
	  {"name":"refs/tags/release.1","objectId":"tagobj111","peeledObjectId":"c0ffee111"},
	  {"name":"refs/tags/release.10","objectId":"c0ffee999"}
	]}`)
	if sha, ok := refCommitFromBody(body, "tags/release.1"); !ok || sha != "c0ffee111" {
		t.Fatalf("annotated tag: peeledObjectId kazanmalı: %q %v", sha, ok)
	}
	if sha, ok := refCommitFromBody(body, "tags/release.10"); !ok || sha != "c0ffee999" {
		t.Fatalf("lightweight tag: objectId: %q %v", sha, ok)
	}
	if _, ok := refCommitFromBody(body, "tags/release"); ok {
		t.Fatal("önek eşleşmesi KABUL EDİLMEZ — release ≠ release.1")
	}
	if _, ok := refCommitFromBody([]byte(`{"value":[]}`), "tags/x"); ok {
		t.Fatal("boş liste → yok")
	}
}

func TestFileURLAt(t *testing.T) {
	cfg := Settings{BaseURL: "https://tfs.example.com", Collection: "DefaultCollection", Project: "SHOP"}
	for _, c := range []struct{ kind, name, want string }{
		{"branch", "refs/heads/release", "version=GBrelease"},
		{"tag", "release.1", "version=GTrelease.1"},
		{"commit", "c0ffee", "version=GCc0ffee"},
	} {
		u := FileURLAt(cfg, "", "repo", RefSpec{Kind: c.kind, Name: c.name}, "src/A.java", 42)
		if !containsStr(u, c.want) || !containsStr(u, "line=42") {
			t.Errorf("%s: %q", c.kind, u)
		}
	}
}
