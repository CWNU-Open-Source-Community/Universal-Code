package waitgraph

import (
	"errors"
	"fmt"
	"strings"
)

// MaxDurableDepth bounds the longest durable wait chain. A deeper chain is
// rejected before the edge is persisted.
const MaxDurableDepth = 64

// ErrDurableCycle reports that persisting the edge would close a wait cycle
// across already persisted edges.
var ErrDurableCycle = errors.New("durable wait dependency would create a cycle")

var (
	ErrDurableSelfLoop   = errors.New("durable wait dependency cannot target its own source")
	ErrDurableDepth      = errors.New("durable wait chain exceeds the maximum depth")
	ErrDurableCapacity   = errors.New("durable wait graph capacity exceeded")
	ErrDurableReverseWait = errors.New("lower runtime layer cannot durably wait on an Agent")
)

// DurableEdge is one persisted wait relationship between two nodes.
type DurableEdge struct {
	Source Node
	Target Node
}

// DAG mirrors the persisted dependency edges of one scope (for example one
// Run) so cycle, depth, and reverse-layer rules can be checked before a new
// edge is written. It is not goroutine-safe; construct it under the store's
// transaction serialization.
type DAG struct {
	edges     []DurableEdge
	neighbors map[string]map[string]struct{}
	count     int
}

func NewDAG() *DAG {
	return &DAG{neighbors: make(map[string]map[string]struct{})}
}

// Add records an already persisted edge.
func (d *DAG) Add(edge DurableEdge) {
	if d == nil {
		return
	}
	from, to := edge.Source.key(), edge.Target.key()
	if from == to {
		return
	}
	neighbors := d.neighbors[from]
	if neighbors == nil {
		neighbors = make(map[string]struct{})
		d.neighbors[from] = neighbors
	}
	if _, exists := neighbors[to]; !exists {
		neighbors[to] = struct{}{}
		d.edges = append(d.edges, edge)
		d.count++
	}
}

// Remove drops one persisted edge (used when an edge reaches a terminal
// state and stops participating in cycle checks).
func (d *DAG) Remove(edge DurableEdge) {
	if d == nil {
		return
	}
	from, to := edge.Source.key(), edge.Target.key()
	neighbors := d.neighbors[from]
	if neighbors == nil {
		return
	}
	if _, exists := neighbors[to]; !exists {
		return
	}
	delete(neighbors, to)
	if len(neighbors) == 0 {
		delete(d.neighbors, from)
	}
	for index, existing := range d.edges {
		if existing.Source.key() == from && existing.Target.key() == to {
			d.edges = append(d.edges[:index], d.edges[index+1:]...)
			break
		}
	}
	d.count--
}

// ValidateInsert rejects self-loops, ancestor and multi-node cycles, reverse
// runtime→Agent waits, and chains beyond MaxDurableDepth. It never mutates
// the DAG.
func (d *DAG) ValidateInsert(edge DurableEdge) error {
	if d == nil {
		return errors.New("durable wait graph is required")
	}
	edge.Source.ID = trimID(edge.Source.ID)
	edge.Target.ID = trimID(edge.Target.ID)
	if err := edge.Source.Validate(); err != nil {
		return fmt.Errorf("invalid durable wait source: %w", err)
	}
	if err := edge.Target.Validate(); err != nil {
		return fmt.Errorf("invalid durable wait target: %w", err)
	}
	from, to := edge.Source.key(), edge.Target.key()
	if from == to {
		return fmt.Errorf("%w: %s/%s", ErrDurableSelfLoop, edge.Source.Kind, edge.Source.ID)
	}
	if edge.Target.Kind == KindAgent && lowerRuntimeKind(edge.Source.Kind) {
		return fmt.Errorf("%w: %s cannot wait on %s", ErrDurableReverseWait,
			edge.Source.Kind, edge.Target.Kind)
	}
	if d.reachable(to, from) {
		return fmt.Errorf("%w: %s/%s -> %s/%s", ErrDurableCycle, edge.Source.Kind,
			edge.Source.ID, edge.Target.Kind, edge.Target.ID)
	}
	// The new chain depth is the longest incoming wait chain to the source
	// plus the longest outgoing chain from the target plus this edge.
	incoming := d.longestIncoming(from, make(map[string]struct{}), 0)
	outgoing := d.longestOutgoing(to, make(map[string]struct{}), 0)
	if incoming+outgoing+1 > MaxDurableDepth {
		return fmt.Errorf("%w: chain depth %d exceeds %d", ErrDurableDepth,
			incoming+outgoing+1, MaxDurableDepth)
	}
	if d.count+1 > MaxActiveEdges {
		return ErrDurableCapacity
	}
	return nil
}

// Reachable reports whether a wait path exists from one node to another.
func (d *DAG) Reachable(from, to Node) bool {
	if d == nil {
		return false
	}
	return d.reachable(from.key(), to.key())
}

func (d *DAG) reachable(start, target string) bool {
	if start == target {
		return true
	}
	seen := make(map[string]struct{}, d.count)
	stack := []string{start}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current == target {
			return true
		}
		if _, ok := seen[current]; ok {
			continue
		}
		seen[current] = struct{}{}
		for next := range d.neighbors[current] {
			stack = append(stack, next)
		}
	}
	return false
}

func (d *DAG) longestIncoming(node string, seen map[string]struct{}, depth int) int {
	if depth > MaxDurableDepth {
		return MaxDurableDepth
	}
	longest := 0
	for source, neighbors := range d.neighbors {
		if _, exists := neighbors[node]; !exists {
			continue
		}
		if _, ok := seen[source]; ok {
			continue
		}
		seen[source] = struct{}{}
		length := 1 + d.longestIncoming(source, seen, depth+1)
		delete(seen, source)
		if length > longest {
			longest = length
		}
	}
	return longest
}

func (d *DAG) longestOutgoing(node string, seen map[string]struct{}, depth int) int {
	if depth > MaxDurableDepth {
		return MaxDurableDepth
	}
	longest := 0
	for next := range d.neighbors[node] {
		if _, ok := seen[next]; ok {
			continue
		}
		seen[next] = struct{}{}
		length := 1 + d.longestOutgoing(next, seen, depth+1)
		delete(seen, next)
		if length > longest {
			longest = length
		}
	}
	return longest
}

func trimID(value string) string {
	return strings.TrimSpace(value)
}

