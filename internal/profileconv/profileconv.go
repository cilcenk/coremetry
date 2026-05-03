// Package profileconv parses pprof profiles into a hierarchical flame tree
// suitable for visualisation in the UI.
package profileconv

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"

	"github.com/google/pprof/profile"

	"github.com/cenk/qmetry/internal/chstore"
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
