// Package profileconv parses pprof profiles into a hierarchical flame tree
// suitable for visualisation in the UI.
package profileconv

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"

	"github.com/google/pprof/profile"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// IsPprof returns true if the byte slice looks like a pprof payload
// (gzipped protobuf — magic bytes 0x1f 0x8b — or raw protobuf wire format).
// Anything else is treated as collapsed-stack text format.
func IsPprof(data []byte) bool {
	if len(data) < 2 {
		return false
	}
	// gzip magic
	if data[0] == 0x1f && data[1] == 0x8b {
		return true
	}
	// Raw protobuf usually starts with field tag bytes; collapsed stacks are
	// printable ASCII or contain ';'. If the first byte is non-printable AND
	// not whitespace, assume binary.
	c := data[0]
	if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
		return true
	}
	return false
}

// BuildFlameAuto picks the right parser based on the payload format.
func BuildFlameAuto(data []byte) (*chstore.FlameNode, error) {
	if IsPprof(data) {
		return BuildFlame(data, 0)
	}
	return BuildFlameFromCollapsed(data)
}

// BuildFlameFromCollapsed parses async-profiler "collapsed" output:
//
//	frame_root;frame_b;frame_c 142
//	frame_root;frame_b;frame_d 88
//
// Each line is one stack (root → leaf, semi-colon separated) followed by a
// count. The same stack may repeat across lines; counts accumulate.
func BuildFlameFromCollapsed(data []byte) (*chstore.FlameNode, error) {
	root := &chstore.FlameNode{Name: "root"}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// last whitespace-separated token = count
		idx := strings.LastIndexAny(line, " \t")
		if idx < 0 {
			continue
		}
		v, err := strconv.ParseInt(strings.TrimSpace(line[idx+1:]), 10, 64)
		if err != nil || v <= 0 {
			continue
		}
		stack := line[:idx]
		root.Value += v

		cur := root
		for _, frame := range strings.Split(stack, ";") {
			frame = strings.TrimSpace(frame)
			if frame == "" {
				continue
			}
			child := findChildByName(cur, frame)
			if child == nil {
				child = &chstore.FlameNode{Name: frame}
				cur.Children = append(cur.Children, child)
			}
			child.Value += v
			cur = child
		}
		cur.Self += v
	}
	return root, scanner.Err()
}

func findChildByName(n *chstore.FlameNode, name string) *chstore.FlameNode {
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// BuildFlame parses raw pprof bytes (gzip or plain pb) and aggregates samples
// into a flame graph tree. `valueIndex` selects which sample value to use:
// 0 is the default (first value, e.g. CPU samples or alloc count).
func BuildFlame(data []byte, valueIndex int) (*chstore.FlameNode, error) {
	p, err := profile.ParseData(data)
	if err != nil {
		return nil, err
	}
	if valueIndex >= len(p.SampleType) {
		valueIndex = 0
	}

	root := &chstore.FlameNode{Name: "root"}
	for _, s := range p.Sample {
		if len(s.Value) <= valueIndex {
			continue
		}
		v := s.Value[valueIndex]
		if v <= 0 {
			continue
		}
		root.Value += v

		// Walk locations from root → leaf (reverse iterate).
		cur := root
		for i := len(s.Location) - 1; i >= 0; i-- {
			loc := s.Location[i]
			for j := len(loc.Line) - 1; j >= 0; j-- {
				line := loc.Line[j]
				name := "<unknown>"
				file := ""
				lineno := int64(0)
				if line.Function != nil {
					name = line.Function.Name
					if name == "" && line.Function.SystemName != "" {
						name = line.Function.SystemName
					}
					file = line.Function.Filename
				}
				lineno = line.Line

				child := findChild(cur, name, file, lineno)
				if child == nil {
					child = &chstore.FlameNode{Name: name, File: file, Line: lineno}
					cur.Children = append(cur.Children, child)
				}
				child.Value += v
				cur = child
			}
		}
		// Self-time on the leaf
		cur.Self += v
	}
	return root, nil
}

func findChild(n *chstore.FlameNode, name, file string, line int64) *chstore.FlameNode {
	for _, c := range n.Children {
		if c.Name == name && c.File == file && c.Line == line {
			return c
		}
	}
	return nil
}

// MergeFlame folds src into dst in place — sums Value+Self on
// every (name, file, line)-matched node and recursively merges
// children. dst's Name is unchanged (callers seed a synthetic
// "root" node). Used by the service-level hotspot aggregator
// where N pprof samples in a time window need to look like one
// flame tree.
func MergeFlame(dst, src *chstore.FlameNode) {
	if src == nil {
		return
	}
	dst.Value += src.Value
	dst.Self += src.Self
	if dst.File == "" && src.File != "" {
		dst.File = src.File
		dst.Line = src.Line
	}
	for _, sc := range src.Children {
		child := findChild(dst, sc.Name, sc.File, sc.Line)
		if child == nil {
			child = &chstore.FlameNode{Name: sc.Name, File: sc.File, Line: sc.Line}
			dst.Children = append(dst.Children, child)
		}
		MergeFlame(child, sc)
	}
}

// FrameKind classifies a stack frame by what the runtime was
// doing while sampled there. Dynatrace's "Suspension" panel
// reads off exactly this distinction — a 10s self-time on a
// CPU frame is a hot path worth optimising, the same 10s on a
// LockSupport.park is contention and the operator looks
// elsewhere. Default for any frame we don't recognise is
// FrameCPU.
type FrameKind string

const (
	FrameCPU   FrameKind = "cpu"
	FrameLock  FrameKind = "lock"  // park, wait, mutex acquire
	FrameIO    FrameKind = "io"    // network / disk syscall
	FrameSleep FrameKind = "sleep" // explicit Thread.sleep / time.Sleep
	FrameGC    FrameKind = "gc"    // runtime GC overhead
)

// ClassifyFrame returns the FrameKind for a fully qualified
// method/function name. Rules are conservative — false positives
// on the kind badge confuse operators, so we only match well-
// known framework / runtime call sites. Anything not on the
// list stays FrameCPU (the safe default).
func ClassifyFrame(name string) FrameKind {
	n := strings.ToLower(name)
	// Lock / wait / park
	if containsAny(n, []string{
		"locksupport.park", "object.wait", "unsafe.park",
		"reentrantlock", "semaphore.acquire", "lock.acquire",
		"monitor.enter", "lock.lock(",
		"sync.(*mutex).lock", "sync.(*rwmutex).rlock", "sync.(*rwmutex).lock",
		"runtime.semacquire", "runtime.notesleep", "runtime.chansend",
		"runtime.chanrecv", "runtime.park_m",
		"futex_wait", "pthread_cond_wait", "pthread_mutex_lock",
	}) || strings.HasPrefix(n, "runtime.gopark") {
		return FrameLock
	}
	// Explicit sleep
	if containsAny(n, []string{
		"thread.sleep", "time.sleep", "time_sleep",
		"runtime.usleep", "sleep(",
	}) {
		return FrameSleep
	}
	// IO — kept narrow on purpose. "read" / "write" substrings
	// hit CPU paths like ByteBuffer.write and would over-count.
	if containsAny(n, []string{
		"runtime.netpoll", "runtime.netpollblock",
		"socketread", "socketwrite",
		"filechannel.read", "filechannel.write",
		"socket._socket.recv", "socket._socket.send",
		"sun.nio.ch", "epoll_wait", "kevent",
	}) {
		return FrameIO
	}
	// GC
	if containsAny(n, []string{
		"runtime.gcbgmarkworker", "runtime.gcassist",
		"g1collectedheap", "gcstart", "gc_main", "youngcollect",
	}) || strings.HasPrefix(n, "runtime.gc") {
		return FrameGC
	}
	return FrameCPU
}

func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

// CategoryBreakdown is the total self-time per FrameKind across
// the whole flame. Sums equal the flame's root value
// (each sample lands in exactly one bucket via its leaf frame).
type CategoryBreakdown struct {
	CPU   int64 `json:"cpu"`
	Lock  int64 `json:"lock"`
	IO    int64 `json:"io"`
	Sleep int64 `json:"sleep"`
	GC    int64 `json:"gc"`
}

func (b CategoryBreakdown) Total() int64 {
	return b.CPU + b.Lock + b.IO + b.Sleep + b.GC
}

// FlameCategoryBreakdown attributes each frame's Self to its
// FrameKind. Walks the whole tree — internal frames with Self=0
// contribute nothing, so the sum tracks the leaf-time
// distribution exactly.
func FlameCategoryBreakdown(root *chstore.FlameNode) CategoryBreakdown {
	var b CategoryBreakdown
	walkBreakdown(root, &b)
	return b
}

func walkBreakdown(n *chstore.FlameNode, b *CategoryBreakdown) {
	if n.Self > 0 {
		switch ClassifyFrame(n.Name) {
		case FrameLock:
			b.Lock += n.Self
		case FrameSleep:
			b.Sleep += n.Self
		case FrameIO:
			b.IO += n.Self
		case FrameGC:
			b.GC += n.Self
		default:
			b.CPU += n.Self
		}
	}
	for _, c := range n.Children {
		walkBreakdown(c, b)
	}
}

// MethodHotspot is the per-function rollup the API returns to
// the UI. Self / Total / Paths semantics mirror the frontend
// helper (flameHotspots.ts) so the table looks identical
// whether it's fed by a single profile or a merged window.
// Kind annotates the row (cpu/lock/io/sleep/gc) so the UI can
// render a coloured badge — operators chasing a regression can
// scan-skip lock/sleep rows when looking for CPU hot paths.
type MethodHotspot struct {
	Name  string    `json:"name"`
	File  string    `json:"file,omitempty"`
	Line  int64     `json:"line,omitempty"`
	Self  int64     `json:"self"`
	Total int64     `json:"total"`
	Paths int       `json:"paths"`
	Kind  FrameKind `json:"kind"`
}

// FlameToHotspots walks the tree once, accumulating per-name
// stats. Recursion-safe: a name's Total is credited only once
// per stack so self-recursive functions don't double-count.
func FlameToHotspots(root *chstore.FlameNode) []MethodHotspot {
	acc := make(map[string]*MethodHotspot)
	walkHS(root, map[string]bool{}, acc)
	out := make([]MethodHotspot, 0, len(acc))
	for _, h := range acc {
		if h.Name == "root" {
			continue
		}
		out = append(out, *h)
	}
	return out
}

func walkHS(n *chstore.FlameNode, ancestors map[string]bool, acc map[string]*MethodHotspot) {
	entry, ok := acc[n.Name]
	if !ok {
		entry = &MethodHotspot{Name: n.Name, File: n.File, Line: n.Line, Kind: ClassifyFrame(n.Name)}
		acc[n.Name] = entry
	}
	entry.Self += n.Self
	seen := ancestors[n.Name]
	if !seen {
		entry.Total += n.Value
		entry.Paths++
	}
	if len(n.Children) == 0 {
		return
	}
	var nextAncestors map[string]bool
	if seen {
		nextAncestors = ancestors
	} else {
		nextAncestors = make(map[string]bool, len(ancestors)+1)
		for k, v := range ancestors {
			nextAncestors[k] = v
		}
		nextAncestors[n.Name] = true
	}
	for _, c := range n.Children {
		walkHS(c, nextAncestors, acc)
	}
}

// SampleCount estimates how many samples a profile contains (for display).
func SampleCount(data []byte) (int, error) {
	if IsPprof(data) {
		p, err := profile.ParseData(data)
		if err != nil {
			return 0, err
		}
		return len(p.Sample), nil
	}
	// Collapsed: line count is a fair proxy for distinct stacks.
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	n := 0
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			n++
		}
	}
	return n, nil
}
