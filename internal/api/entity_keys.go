package api

import (
	"fmt"
	"hash/fnv"
	"time"
)

// entity_keys.go — entity uçlarının cache anahtarları (v0.10.130). SAF;
// entity_key_test.go pinler. Serbest metinler (namespace, arama, id) FNV
// ile AYRI AYRI özetlenir — "a|b"+"c" ≠ "a"+"b|c" (endpointKeyDigest
// deseni); at/pencere cacheBucket grid'inde.

func fnvStr(parts ...string) string {
	h := fnv.New64a()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum64())
}

func atBucket(at time.Time) string {
	if at.IsZero() {
		return "now"
	}
	return fmt.Sprintf("%d", at.Unix()/30*30)
}

func entityListKey(cluster, typ, ns, search string, limit int, at time.Time) string {
	return fmt.Sprintf("entities:list:%s:lim=%d:at=%s", fnvStr(cluster, typ, ns, search), limit, atBucket(at))
}

func entityPivotKey(kind, id string, from, to time.Time) string {
	return fmt.Sprintf("entities:%s:%s:w=%s", kind, fnvStr(id), cacheBucket(from, to))
}

func servicePodsKey(service, cluster string, from, to time.Time) string {
	return fmt.Sprintf("entities:svcpods:%s:w=%s", fnvStr(service, cluster), cacheBucket(from, to))
}
