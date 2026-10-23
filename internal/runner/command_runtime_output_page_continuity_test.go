package runner

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestCommandRuntimeRingPageStaysContiguous proves a bounded output page may never
// report a NextCursor past bytes it did not deliver. The page bound backs off
// inside a multi-byte rune to keep returned text valid UTF-8; after that backoff
// the rest of the same frame is still retained, and the following frame starts
// past the residue, so the page must stop instead of continuing with it.
func TestCommandRuntimeRingPageStaysContiguous(t *testing.T) {
	ring := commandRuntimeRing{capacity: MinCommandRuntimeInlineBytes}
	first := "😀😀😀😀😀\n"
	second := "x\n"
	ring.append(CommandRuntimeStdout, first, time.Unix(0, 1).UTC())
	ring.append(CommandRuntimeStdout, second, time.Unix(0, 2).UTC())
	full := first + second

	cursor := uint64(0)
	var assembled strings.Builder
	for page := 0; page < 20; page++ {
		result := ring.read(cursor, 6)
		if len(result.Frames) == 0 {
			break
		}
		var delivered strings.Builder
		used := 0
		for _, frame := range result.Frames {
			delivered.WriteString(frame.Text)
			used += len([]byte(frame.Text))
		}
		if delivered.String() != full[cursor:result.NextCursor] {
			t.Fatalf("page %d from cursor %d to %d delivered %q, which is not the retained range %q",
				page, cursor, result.NextCursor, delivered.String(), full[cursor:result.NextCursor])
		}
		if used > 6 {
			t.Fatalf("page %d returned %d bytes for a 6-byte bound", page, used)
		}
		if result.NextCursor <= cursor {
			t.Fatalf("page %d did not advance the cursor", page)
		}
		assembled.WriteString(delivered.String())
		cursor = result.NextCursor
	}
	if cursor != ring.next {
		t.Fatalf("drain stopped at %d while the window ends at %d", cursor, ring.next)
	}
	if assembled.String() != full {
		t.Fatalf("resumed pages lost retained output: got %q want %q", assembled.String(), full)
	}
}

// TestCommandRuntimeManagerPagedWaitKeepsMultiByteOutput drives the same invariant
// through the public Wait API, which is how the operator drain and the thread
// activity reader resume retained output.
func TestCommandRuntimeManagerPagedWaitKeepsMultiByteOutput(t *testing.T) {
	store := newCommandRuntimeMemoryStore()
	starter := &commandRuntimeFakeStarter{}
	manager, err := NewCommandRuntimeManager(store, starter, "multibyte-page-owner")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := manager.Start(context.Background(), commandRuntimeTestRequest(manager, 20000))
	if err != nil {
		t.Fatal(err)
	}
	process := starter.last()
	first := "😀😀😀😀😀\n"
	second := "x\n"
	if _, err := process.stdoutWriter.Write([]byte(first)); err != nil {
		t.Fatal(err)
	}
	if _, err := process.stdoutWriter.Write([]byte(second)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		entry := manager.entry(snapshot.ID)
		if entry != nil && entry.snapshot().OutputCursor == uint64(len(first)+len(second)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("output was not collected: %#v", entry.snapshot())
		}
		time.Sleep(5 * time.Millisecond)
	}
	var cursor uint64
	var assembled strings.Builder
	var endCursor uint64
	for page := 0; page < 20; page++ {
		_, result, err := manager.Wait(context.Background(), snapshot.ID, 0, cursor, 6)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Frames) == 0 {
			break
		}
		var delivered strings.Builder
		for _, frame := range result.Frames {
			delivered.WriteString(frame.Text)
		}
		if delivered.String() != (first + second)[cursor:result.NextCursor] {
			t.Fatalf("page %d from %d to %d delivered %q instead of the retained range %q",
				page, cursor, result.NextCursor, delivered.String(),
				(first + second)[cursor:result.NextCursor])
		}
		assembled.WriteString(delivered.String())
		cursor = result.NextCursor
		endCursor = result.EndCursor
		if cursor >= endCursor {
			break
		}
	}
	if cursor != endCursor {
		t.Fatalf("resume never reached the window end: cursor=%d end=%d", cursor, endCursor)
	}
	if assembled.String() != first+second {
		t.Fatalf("paged Wait lost output: got %q want %q", assembled.String(), first+second)
	}
}
