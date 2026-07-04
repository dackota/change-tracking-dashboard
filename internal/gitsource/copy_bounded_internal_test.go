package gitsource

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestCopyBounded_ReaderExceedsMaxBytes_StopsAtCeiling proves the SHOULD-FIX
// self-enforced materialize byte ceiling: copyBounded ties the actual number
// of bytes written to maxBytes, regardless of how much data the source
// reader actually has to offer. materializeBlob checks a blob's *declared*
// Size against the remaining budget before ever calling this, but that
// declared Size is git-object metadata, not a guarantee about the reader's
// real behavior — copyBounded is the chokepoint that holds the ceiling even
// if that declared size were ever inaccurate (defense-in-depth, not reliant
// on the accuracy of an upstream go-git property). This is a plain io.Reader
// with more content than maxBytes, so no real (accurate-by-construction) git
// blob needs to be fabricated to exercise the bound.
func TestCopyBounded_ReaderExceedsMaxBytes_StopsAtCeiling(t *testing.T) {
	t.Parallel()

	const maxBytes = 100
	oversized := bytes.NewReader(bytes.Repeat([]byte{0xAB}, maxBytes*10))

	dest := filepath.Join(t.TempDir(), "out.bin")
	written, err := copyBounded(oversized, dest, maxBytes)

	if !errors.Is(err, ErrMaterializeBoundsExceeded) {
		t.Fatalf("copyBounded error = %v, want errors.Is(err, ErrMaterializeBoundsExceeded) == true", err)
	}
	if written > maxBytes {
		t.Errorf("copyBounded reported written = %d, want <= %d (the configured ceiling)", written, maxBytes)
	}

	onDisk, statErr := os.Stat(dest)
	if statErr != nil {
		t.Fatalf("stat %q: %v", dest, statErr)
	}
	if onDisk.Size() > maxBytes {
		t.Errorf("file on disk is %d bytes, want <= %d (the configured ceiling) — the copy must never write past the budget even when the source has more data", onDisk.Size(), maxBytes)
	}
}

// TestCopyBounded_ReaderWithinMaxBytes_CopiesEverything is the converse
// check: a reader with less content than maxBytes copies in full, with no
// spurious bounds error — the ceiling only trips when the source actually
// exceeds it.
func TestCopyBounded_ReaderWithinMaxBytes_CopiesEverything(t *testing.T) {
	t.Parallel()

	content := []byte("well within the budget")
	r := bytes.NewReader(content)

	dest := filepath.Join(t.TempDir(), "out.bin")
	written, err := copyBounded(r, dest, int64(len(content)+50))
	if err != nil {
		t.Fatalf("copyBounded: %v", err)
	}
	if written != int64(len(content)) {
		t.Errorf("written = %d, want %d", written, len(content))
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read %q: %v", dest, err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("on-disk content = %q, want %q", got, content)
	}
}
