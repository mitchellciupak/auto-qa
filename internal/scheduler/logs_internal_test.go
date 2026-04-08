package scheduler

import (
	"strings"
	"testing"
)

func TestReadLogStreamCapped_NoTruncation(t *testing.T) {
	input := "short log"
	got, err := readLogStreamCapped(strings.NewReader(input), 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != input {
		t.Fatalf("expected %q, got %q", input, got)
	}
}

func TestReadLogStreamCapped_ExactLimit_NoTruncationMarker(t *testing.T) {
	input := strings.Repeat("x", 8)
	got, err := readLogStreamCapped(strings.NewReader(input), 8)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != input {
		t.Fatalf("expected %q, got %q", input, got)
	}
}

func TestReadLogStreamCapped_TruncatesAndAppendsMarker(t *testing.T) {
	input := strings.Repeat("a", 12)
	got, err := readLogStreamCapped(strings.NewReader(input), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := strings.Repeat("a", 10) + logTruncatedMarker
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}
