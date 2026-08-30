package api

import (
	"os"
	"strings"
	"testing"
)

// v0.10.197 kaynak-pini (inceleme S3/S4): apply ucu ön kontrolü HER istekte
// koşar ve Supported/probe hatası/0011 kapısını sunucuda doğrular — arayüz
// kapıları doğrudan POST'la atlanamaz.
func TestRolloutLayerApplyGatesServerSide(t *testing.T) {
	b, err := os.ReadFile("admin_rollout_layer.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func (s *Server) postRolloutLayerApply(")
	if i < 0 {
		t.Fatal("postRolloutLayerApply bulunamadı")
	}
	body := src[i:]
	for _, need := range []string{"!pre.Supported", "len(pre.ProbeErrors) > 0", "pre.MVGate && pre.Layer0011", "http.StatusConflict"} {
		if !strings.Contains(body, need) {
			t.Errorf("apply kapısı %q taşımıyor", need)
		}
	}
	if strings.Index(body, "s.store.RolloutLayerPreflight(") > strings.Index(body, "s.store.RolloutLayerApply(") {
		t.Fatal("ön kontrol DDL'den ÖNCE koşmalı")
	}
}
