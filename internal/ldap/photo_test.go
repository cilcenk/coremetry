package ldap

import (
	"bytes"
	"testing"

	goldap "github.com/go-ldap/ldap/v3"
)

// v0.8.238 — LDAP profile photo extraction: thumbnailPhoto (AD) wins
// over jpegPhoto; an oversized value is DROPPED (a truncated JPEG
// renders as a broken image — no photo renders as the initials
// fallback); absent attributes → nil.
func TestPhotoFromEntry(t *testing.T) {
	entry := func(attrs map[string][][]byte) *goldap.Entry {
		e := &goldap.Entry{DN: "cn=test"}
		for name, vals := range attrs {
			e.Attributes = append(e.Attributes, &goldap.EntryAttribute{Name: name, ByteValues: vals})
		}
		return e
	}
	small := []byte{0xFF, 0xD8, 0xFF, 0xE0} // JPEG magic
	huge := make([]byte, maxPhotoBytes+1)

	if got := photoFromEntry(entry(nil)); got != nil {
		t.Fatalf("no attrs → nil, got %d bytes", len(got))
	}
	if got := photoFromEntry(entry(map[string][][]byte{"jpegPhoto": {small}})); !bytes.Equal(got, small) {
		t.Fatal("jpegPhoto alone must be used")
	}
	if got := photoFromEntry(entry(map[string][][]byte{
		"thumbnailPhoto": {small}, "jpegPhoto": {[]byte("other")},
	})); !bytes.Equal(got, small) {
		t.Fatal("thumbnailPhoto (AD, small by convention) must win over jpegPhoto")
	}
	// Oversized thumbnail dropped → falls THROUGH to jpegPhoto.
	if got := photoFromEntry(entry(map[string][][]byte{
		"thumbnailPhoto": {huge}, "jpegPhoto": {small},
	})); !bytes.Equal(got, small) {
		t.Fatal("oversized thumbnailPhoto must be skipped, jpegPhoto used")
	}
	// Both oversized → nil (drop, never truncate).
	if got := photoFromEntry(entry(map[string][][]byte{
		"thumbnailPhoto": {huge}, "jpegPhoto": {huge},
	})); got != nil {
		t.Fatal("oversized photos must be dropped, not truncated")
	}
}
