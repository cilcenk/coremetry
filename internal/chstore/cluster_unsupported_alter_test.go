package chstore

// v0.10.381 — dış skill denetimi C2: boot ALTER'ları MODIFY SETTING
// ttl_only_drop_parts taşıyor. cluster_name ayarsızken dış Distributed
// tablo bunu "is not supported" değil "Unknown setting … for storage
// Distributed" ile reddeder; eşleşmezse store.go alters döngüsü hatayı
// döndürüp boot'u DURDURUR. Matcher üç metni de tanımalı, yerel tablo
// hatalarını yutmamalı.

import (
	"errors"
	"testing"
)

func TestIsClusterUnsupportedAlterRecognisesSettingRejection(t *testing.T) {
	for _, msg := range []string{
		"code: 48, message: Alter of type 'ADD INDEX' is not supported by storage Distributed",
		"code: 36, message: Distributed doesn't support TTL",
		"code: 115, message: Unknown setting 'ttl_only_drop_parts' for storage Distributed",
	} {
		if !isClusterUnsupportedAlter(errors.New(msg)) {
			t.Fatalf("must be treated as wrapper-unsupported: %s", msg)
		}
	}
	for _, msg := range []string{
		"code: 115, message: Unknown setting 'ttl_only_drop_parts'",
		"code: 60, message: Table coremetry.spans_local does not exist",
	} {
		if isClusterUnsupportedAlter(errors.New(msg)) {
			t.Fatalf("must NOT be swallowed (local-table error): %s", msg)
		}
	}
}
