package workspace

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestPortOffset(t *testing.T) {
	if got := PortOffset("0123456789abcdef", DefaultName); got != 0 {
		t.Fatalf("default offset = %d", got)
	}
	first := PortOffset("0123456789abcdef", "agent-1")
	if first < 1 || first > PortOffsetMax {
		t.Fatalf("offset out of range: %d", first)
	}
	if again := PortOffset("0123456789abcdef", "agent-1"); again != first {
		t.Fatalf("offset is not deterministic: %d != %d", first, again)
	}
	if other := PortOffset("0123456789abcdef", "agent-2"); other == first {
		t.Fatalf("distinct names collided at %d", first)
	}
	if other := PortOffset("fedcba9876543210", "agent-1"); other == first {
		t.Fatalf("different projects collided at %d", first)
	}
}

func TestCheckPortOffsetCollision(t *testing.T) {
	const projectID = "0123456789abcdef"
	want := PortOffset(projectID, "agent-1")
	var colliding string
	for i := 0; i < 2000; i++ {
		name := fmt.Sprintf("other-%d", i)
		if PortOffset(projectID, name) == want {
			colliding = name
			break
		}
	}
	if colliding == "" {
		t.Fatal("could not find a colliding name for the test")
	}
	offset, err := CheckPortOffset(projectID, "agent-1", []string{"default", colliding})
	var collision *ErrPortOffsetCollision
	if !errors.As(err, &collision) {
		t.Fatalf("expected collision, got %d, %v", offset, err)
	}
	if collision.Offset != want || collision.Other != colliding {
		t.Fatalf("collision = %#v", collision)
	}
	if !strings.Contains(collision.Hint(), "Rename") {
		t.Fatalf("hint = %q", collision.Hint())
	}
	if offset, err := CheckPortOffset(projectID, "agent-1", []string{"default", "agent-2"}); err != nil || offset != want {
		t.Fatalf("unexpected collision: %d, %v", offset, err)
	}
}
