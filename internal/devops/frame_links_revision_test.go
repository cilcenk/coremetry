package devops

// frame_links_revision_test.go — v0.10.590: olay anındaki sürüm → tag → commit.
//
//   - tag VARSA: Revision.Verified, SHA; linkler GC<sha>; AĞAÇ DA o commit'ten
//     okunur (link ile yol aynı ref — aksi hâlde satır yine kayardı)
//   - tag YOKSA: Verified=false, Note "bulunamadı"; linkler branş ucu (GB);
//     ağaç branştan — bugünkü davranış aynen, hiçbir şey gerilemez
//   - yer tutucu sürüm: Revision nil, tek ekstra istek bile yok

import (
	"context"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/stackparse"
)

func revisionFixture(t *testing.T) (*fakeTFS, *Service, []stackparse.Frame) {
	t.Helper()
	f := newFakeTFS(t)
	// Branş ucunda dosya TAŞINMIŞ (moved/), olay anındaki commit'te eski yerinde.
	// Yol commit ağacından gelmezse link branşın yoluna gider — satır kayması
	// sınıfının ta kendisi.
	f.tree = []string{"/src/main/java/com/example/moved/CardService.java"}
	f.treeAt = map[string][]string{
		"c0ffee0000000000000000000000000000000001": {"/src/main/java/com/example/card/CardService.java"},
	}
	f.tags = map[string]string{"refs/tags/release.20260817.1": "tagobj0001"}
	f.peeled = map[string]string{"refs/tags/release.20260817.1": "c0ffee0000000000000000000000000000000001"}
	svc := New()
	svc.Configure(f.settings())
	frames := []stackparse.Frame{frame("com.example.card.CardService", "charge", "CardService.java", 246)}
	return f, svc, frames
}

func TestResolveFrameLinks_VersionTagVerified(t *testing.T) {
	f, svc, frames := revisionFixture(t)
	got := svc.ResolveFrameLinks(context.Background(), "shop-core-service-prod", PinRead{}, frames, "release.20260817.1")
	if got.Revision == nil || !got.Revision.Verified {
		t.Fatalf("tag varken sürüm doğrulanmalı: %+v", got.Revision)
	}
	if got.Revision.SHA != "c0ffee0000000000000000000000000000000001" || got.Revision.Ref != "tags/release.20260817.1" {
		t.Fatalf("peeled commit + ref: %+v", *got.Revision)
	}
	u := got.Links[0].URL
	if !strings.Contains(u, "version=GCc0ffee") || strings.Contains(u, "version=GB") {
		t.Fatalf("link commit'e gitmeli (GC), branşa değil: %s", u)
	}
	// YOL commit ağacından: card/, moved/ değil.
	if !strings.Contains(u, "com%2Fexample%2Fcard%2FCardService.java") || strings.Contains(u, "moved") {
		t.Fatalf("dosya yolu commit ağacından gelmeli (card/), branştan (moved/) değil: %s", u)
	}
	// Ağaç o commit'ten okundu — link ile yol aynı ref.
	sawCommit := false
	for _, v := range f.treeVersions {
		if v == "commit:c0ffee0000000000000000000000000000000001" {
			sawCommit = true
		}
	}
	if !sawCommit {
		t.Fatalf("dosya ağacı commit'ten okunmalı; görülen ref'ler: %v", f.treeVersions)
	}
}

func TestResolveFrameLinks_VersionTagMissingFallsBackWithNote(t *testing.T) {
	_, svc, frames := revisionFixture(t)
	got := svc.ResolveFrameLinks(context.Background(), "shop-core-service-prod", PinRead{}, frames, "release.19990101.9")
	if got.Revision == nil || got.Revision.Verified {
		t.Fatalf("tag yokken doğrulanmamalı: %+v", got.Revision)
	}
	if !strings.Contains(got.Revision.Note, "bulunamadı") {
		t.Fatalf("not nedeni söylemeli: %q", got.Revision.Note)
	}
	if u := got.Links[0].URL; !strings.Contains(u, "version=GBrelease") {
		t.Fatalf("branş ucuna düşmeli (bugünkü davranış): %s", u)
	}
}

func TestResolveFrameLinks_PlaceholderVersionSkipsResolution(t *testing.T) {
	f, svc, frames := revisionFixture(t)
	got := svc.ResolveFrameLinks(context.Background(), "shop-core-service-prod", PinRead{}, frames, "latest")
	if got.Revision != nil {
		t.Fatalf("yer tutucu sürüm ref üretmez: %+v", got.Revision)
	}
	for _, v := range f.treeVersions {
		if strings.HasPrefix(v, "commit:") {
			t.Fatalf("yer tutucu için commit ağacı okunmamalı: %v", f.treeVersions)
		}
	}
}
