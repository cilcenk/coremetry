package main

// k8s_workload_test.go — v0.10.192 (rollouts audit «ön koşul»): demo üreteci
// K8s işyükü kimliği sözleşmesi. Rollout dedektörünün girdileri lokalde
// GERÇEKTEN icra edilsin (feedback-local-data-is-a-fixture): replicaset adı
// pod-template-hash biçiminde ve nesille değişir; STS iş yükü RS yaymaz;
// namespace iki değerli; imaj adı+tag her sinyalde aynı yardımcıdan.

import (
	"regexp"
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

func resAttrMap(kv []*commonpb.KeyValue) map[string]string {
	out := map[string]string{}
	for _, a := range kv {
		out[a.Key] = a.Value.GetStringValue()
	}
	return out
}

func TestK8sWorkloadAttrs(t *testing.T) {
	t.Cleanup(func() { podGeneration, imageTag = "", "" })
	podGeneration, imageTag = "r4242", "release.20260830.r4242"
	dep := k8sWorkloadAttrs(Service{Name: "api-gateway"})
	m := resAttrMap(dep)
	rs := m["k8s.replicaset.name"]
	hashRe := regexp.MustCompile(`^api-gateway-[bcdfghjklmnpqrstvwxz2456789]{10}$`)
	if !hashRe.MatchString(rs) {
		t.Fatalf("replicaset adı <deployment>-<10 k8s-alfabesi> olmalı: %q", rs)
	}
	if m["k8s.deployment.name"] != "api-gateway" || m["k8s.namespace.name"] != "channels" || m["k8s.container.name"] != "api-gateway" {
		t.Fatalf("deployment/namespace/container: %v", m)
	}
	if m["container.image.name"] != "registry.demo.local/coremetry-demo/api-gateway" {
		t.Fatalf("image name: %q", m["container.image.name"])
	}
	if m["container.image.tag"] != "release.20260830.r4242" {
		t.Fatalf("image tag nesille sabit (release.<gün>.<nesil>) olmalı: %q", m["container.image.tag"])
	}
	// nesil değişince RS adı değişir (her yeniden başlatma = rollout)
	podGeneration = "r7777"
	m2 := resAttrMap(k8sWorkloadAttrs(Service{Name: "api-gateway"}))
	if m2["k8s.replicaset.name"] == rs {
		t.Fatalf("nesil değişti, RS adı değişmedi: %q", rs)
	}
	// StatefulSet: statefulset adı VAR, deployment/replicaset YOK
	sts := resAttrMap(k8sWorkloadAttrs(Service{Name: "feature-store"}))
	if sts["k8s.statefulset.name"] != "feature-store" || sts["k8s.deployment.name"] != "" || sts["k8s.replicaset.name"] != "" {
		t.Fatalf("STS iş yükü: %v", sts)
	}
	if sts["k8s.namespace.name"] != "demo" {
		t.Fatalf("kanal dışı servis namespace 'demo' olmalı: %q", sts["k8s.namespace.name"])
	}
}
