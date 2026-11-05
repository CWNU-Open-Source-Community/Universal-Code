package waitgraph

import (
	"errors"
	"strconv"
	"testing"
)

func TestDAGRejectsSelfLoopAndCycles(t *testing.T) {
	dag := NewDAG()
	if err := dag.ValidateInsert(DurableEdge{Source: Agent("a"), Target: Agent("a")});
		!errors.Is(err, ErrDurableSelfLoop) {
		t.Fatalf("self-loop was not rejected: %v", err)
	}
	dag.Add(DurableEdge{Source: Agent("a"), Target: Agent("b")})
	if err := dag.ValidateInsert(DurableEdge{Source: Agent("b"), Target: Agent("a")});
		!errors.Is(err, ErrDurableCycle) {
		t.Fatalf("two-node cycle was not rejected: %v", err)
	}
	dag.Add(DurableEdge{Source: Agent("b"), Target: Agent("c")})
	dag.Add(DurableEdge{Source: Agent("c"), Target: Agent("d")})
	if err := dag.ValidateInsert(DurableEdge{Source: Agent("d"), Target: Agent("a")});
		!errors.Is(err, ErrDurableCycle) {
		t.Fatalf("multi-node cycle was not rejected: %v", err)
	}
	if !dag.Reachable(Agent("a"), Agent("d")) {
		t.Fatal("expected a wait path from a to d")
	}
	if dag.Reachable(Agent("d"), Agent("a")) {
		t.Fatal("unexpected reverse wait path")
	}
}

func TestDAGRejectsReverseRuntimeAgentWait(t *testing.T) {
	dag := NewDAG()
	if err := dag.ValidateInsert(DurableEdge{Source: Tool("shell"), Target: Agent("a")});
		!errors.Is(err, ErrDurableReverseWait) {
		t.Fatalf("tool→agent wait was not rejected: %v", err)
	}
	if err := dag.ValidateInsert(DurableEdge{Source: Retriever("rag"), Target: Agent("a")});
		!errors.Is(err, ErrDurableReverseWait) {
		t.Fatalf("retriever→agent wait was not rejected: %v", err)
	}
	// Runtime nodes may wait on other runtime nodes and Agents on Agents.
	if err := dag.ValidateInsert(DurableEdge{Source: Tool("shell"), Target: Retriever("rag")});
		err != nil {
		t.Fatalf("tool→retriever wait was rejected: %v", err)
	}
	if err := dag.ValidateInsert(DurableEdge{Source: Agent("a"), Target: Agent("b")});
		err != nil {
		t.Fatalf("agent→agent wait was rejected: %v", err)
	}
}

func TestDAGRejectsExcessiveDepth(t *testing.T) {
	dag := NewDAG()
	// A chain of exactly MaxDurableDepth edges is the last accepted shape.
	for index := 0; index < MaxDurableDepth; index++ {
		edge := DurableEdge{Source: Agent(nodeID(index)), Target: Agent(nodeID(index + 1))}
		if err := dag.ValidateInsert(edge); err != nil {
			t.Fatalf("chain edge %d rejected: %v", index, err)
		}
		dag.Add(edge)
	}
	// One more edge on top exceeds the bound and must be rejected before it
	// is persisted.
	if err := dag.ValidateInsert(DurableEdge{Source: Agent("head"),
		Target: Agent(nodeID(0))}); !errors.Is(err, ErrDurableDepth) {
		t.Fatalf("excessive chain depth was not rejected: %v", err)
	}
}

func TestDAGRemoveRestoresInsert(t *testing.T) {
	dag := NewDAG()
	edge := DurableEdge{Source: Agent("a"), Target: Agent("b")}
	dag.Add(edge)
	if err := dag.ValidateInsert(DurableEdge{Source: Agent("b"), Target: Agent("a")});
		!errors.Is(err, ErrDurableCycle) {
		t.Fatalf("cycle with persisted edge was not rejected: %v", err)
	}
	dag.Remove(edge)
	if err := dag.ValidateInsert(DurableEdge{Source: Agent("b"), Target: Agent("a")});
		err != nil {
		t.Fatalf("removed edge still blocks the reverse wait: %v", err)
	}
}

func nodeID(value int) string {
	return "node-" + strconv.Itoa(value)
}

