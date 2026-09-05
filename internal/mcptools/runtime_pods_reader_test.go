package mcptools

// v0.10.374 — VM dilim 3c: the pod-health tool reads JVM heap through
// Deps.RuntimePods (VictoriaMetrics when configured), Store otherwise.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

type fakePods struct{}

func (fakePods) JVMHeapPodUsage(context.Context, time.Time, time.Time) ([]chstore.CapacitySample, error) {
	return []chstore.CapacitySample{{Instance: "vm"}}, nil
}
func (fakePods) JVMGCPodPause(context.Context, time.Time, time.Time) ([]chstore.CapacitySample, error) {
	return nil, nil
}

func TestDepsRuntimePodsPrefersInjectedReader(t *testing.T) {
	d := Deps{RuntimePods: fakePods{}}
	got, _ := d.runtimePods().JVMHeapPodUsage(context.Background(), time.Now(), time.Now())
	if len(got) != 1 || got[0].Instance != "vm" {
		t.Fatal("injected reader must answer")
	}
	var st *chstore.Store
	if (Deps{Store: st}).runtimePods() != chstore.RuntimePodReader(st) {
		t.Fatal("no reader → Store")
	}
	b, err := os.ReadFile("guided_parity.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "d.Store.JVMHeapPodUsage(") || !strings.Contains(string(b), "d.runtimePods().JVMHeapPodUsage(") {
		t.Fatal("ReadPodHealth must read heap through Deps.runtimePods()")
	}
}
