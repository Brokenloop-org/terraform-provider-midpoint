package convert

import (
	"fmt"
	"slices"
	"strings"
)

// breakCycles leaves references literal until the Refs between converted
// objects form no cycle, so that Terraform can order the resources. While
// the graph of Refs (object to referenced object) has a cycle, the edge of
// that cycle whose "source → target" text is lexically largest is broken:
// every Ref from the source to the target becomes the target's literal oid,
// and a note on the source names both ends. Cycles are found in sorted
// order, so the result is deterministic. rs is modified in place (Rewrite
// owns it); the notes are appended.
func breakCycles(rs []Resource, notes []Note) []Note {
	oids := map[string]string{}
	for _, r := range rs {
		oids[r.Kind+"."+r.Label] = r.OID
	}
	graph := map[string][]string{}
	for _, r := range rs {
		src := r.Kind + "." + r.Label
		walkRefs(r, func(ref Ref) any {
			if dst, ok := refNode(ref, oids); ok && dst != src && !slices.Contains(graph[src], dst) {
				graph[src] = append(graph[src], dst)
			}
			return ref
		})
	}
	broken := map[string]map[string]bool{} // source -> targets left literal
	for {
		cycle := findCycle(graph)
		if cycle == nil {
			break
		}
		src, dst := largestEdge(cycle)
		graph[src] = slices.DeleteFunc(graph[src], func(n string) bool { return n == dst })
		if broken[src] == nil {
			broken[src] = map[string]bool{}
		}
		broken[src][dst] = true
		notes = append(notes, Note{Path: src, Message: fmt.Sprintf(
			"the reference from %s to %s is left as the literal oid %s to break the reference cycle %s "+
				"(Terraform cannot order objects that reference each other)",
			src, dst, oids[dst], strings.Join(append(slices.Clone(cycle), cycle[0]), " → "))})
	}
	for i := range rs {
		targets := broken[rs[i].Kind+"."+rs[i].Label]
		if len(targets) == 0 {
			continue
		}
		rs[i] = walkRefs(rs[i], func(ref Ref) any {
			if dst, ok := refNode(ref, oids); ok && targets[dst] {
				return oids[dst]
			}
			return ref
		})
	}
	return notes
}

// refNode is the converted object ref points to ("midpoint_role.x"); ok is
// false for a data lookup or an address that names no converted object
// with an oid.
func refNode(ref Ref, oids map[string]string) (string, bool) {
	node, ok := strings.CutSuffix(ref.Address, ".oid")
	if !ok || strings.HasPrefix(node, "data.") || oids[node] == "" {
		return "", false
	}
	return node, true
}

// findCycle returns one cycle of graph as its nodes in order (the edge from
// the last back to the first closes it), or nil. Nodes and edges are
// visited in sorted order.
func findCycle(graph map[string][]string) []string {
	const (
		unvisited = iota
		onStack
		done
	)
	state := map[string]int{}
	var stack []string
	var visit func(n string) []string
	visit = func(n string) []string {
		state[n] = onStack
		stack = append(stack, n)
		for _, m := range slices.Sorted(slices.Values(graph[n])) {
			switch state[m] {
			case onStack:
				i := slices.Index(stack, m)
				return slices.Clone(stack[i:])
			case unvisited:
				if c := visit(m); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[n] = done
		return nil
	}
	for _, n := range sortedKeys(graph) {
		if state[n] == unvisited {
			if c := visit(n); c != nil {
				return c
			}
		}
	}
	return nil
}

// largestEdge is the edge of cycle whose "source → target" text is
// lexically largest.
func largestEdge(cycle []string) (src, dst string) {
	best := ""
	for i, s := range cycle {
		d := cycle[(i+1)%len(cycle)]
		if e := s + " → " + d; e > best {
			best, src, dst = e, s, d
		}
	}
	return src, dst
}

// walkRefs returns r with every Ref in its attributes, blocks and extra
// replaced by f(ref). It rebuilds what it walks, never sharing a modified
// container with r.
func walkRefs(r Resource, f func(Ref) any) Resource {
	out := r
	if r.Attrs != nil {
		out.Attrs = walkRefValue(r.Attrs, f).(map[string]any)
	}
	out.Blocks = walkRefBlocks(r.Blocks, f)
	if r.Extra != nil {
		out.Extra = walkRefValue(r.Extra, f).(map[string]any)
	}
	return out
}

func walkRefBlocks(bs []Block, f func(Ref) any) []Block {
	if bs == nil {
		return nil
	}
	out := make([]Block, len(bs))
	for i, b := range bs {
		out[i] = b
		if b.Attrs != nil {
			out[i].Attrs = walkRefValue(b.Attrs, f).(map[string]any)
		}
		out[i].Blocks = walkRefBlocks(b.Blocks, f)
	}
	return out
}

func walkRefValue(v any, f func(Ref) any) any {
	switch t := v.(type) {
	case Ref:
		return f(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = walkRefValue(e, f)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = walkRefValue(e, f)
		}
		return out
	default:
		return v
	}
}
