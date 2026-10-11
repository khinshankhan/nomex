package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "available.txt")
	err := writeOutput(path, func(w io.Writer) {
		fmt.Fprintln(w, "a.dev")
	})
	if err != nil {
		t.Fatalf("writeOutput: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "a.dev\n" {
		t.Errorf("wrote %q, want %q", got, "a.dev\n")
	}
}

// a failed write must not pass as a complete list
func TestWriteOutputReportsWriteFailure(t *testing.T) {
	// every write to /dev/full fails with ENOSPC
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("no /dev/full here")
	}

	err := writeOutput("/dev/full", func(w io.Writer) {
		fmt.Fprintln(w, "a.dev")
	})
	if err == nil {
		t.Error("writeOutput reported success writing to a full device")
	}
}
