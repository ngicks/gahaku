package input

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newResolver(t *testing.T, cfg Config) *Resolver {
	t.Helper()
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func TestOpenByteStreamUsesDefaultLimit(t *testing.T) {
	w, err := newResolver(t, Config{}).OpenByteStream(t.TempDir())
	if err != nil {
		t.Fatalf("OpenByteStream: %v", err)
	}
	defer w.Close()

	if w.limit != DefaultMaxStreamBytes {
		t.Errorf("limit = %d, want %d", w.limit, DefaultMaxStreamBytes)
	}
}

func TestByteStreamWriterLimit(t *testing.T) {
	const limit = 8

	for _, tt := range []struct {
		name    string
		chunks  []string
		wantErr bool
		// wantReceived is the Received reported on the limit error.
		wantReceived int64
	}{
		{name: "below the limit", chunks: []string{"abc", "de"}},
		{name: "exactly at the limit", chunks: []string{"abcd", "efgh"}},
		{
			name:         "one byte over the limit",
			chunks:       []string{"abcd", "efghi"},
			wantErr:      true,
			wantReceived: limit + 1,
		},
		{
			name:         "single oversized chunk",
			chunks:       []string{"abcdefghijkl"},
			wantErr:      true,
			wantReceived: 12,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			w, err := newResolver(t, Config{MaxStreamBytes: limit}).OpenByteStream(dir)
			if err != nil {
				t.Fatalf("OpenByteStream: %v", err)
			}
			defer w.Close()

			var written []byte
			var writeErr error
			for _, chunk := range tt.chunks {
				if _, writeErr = w.Write([]byte(chunk)); writeErr != nil {
					break
				}
				written = append(written, chunk...)
			}

			limitErr, ok := errors.AsType[*LimitExceededError](writeErr)
			switch {
			case tt.wantErr && !ok:
				t.Fatalf("write error = %v, want *LimitExceededError", writeErr)
			case !tt.wantErr && writeErr != nil:
				t.Fatalf("write error = %v, want nil", writeErr)
			case !tt.wantErr:
				return
			}
			if limitErr.Limit != limit || limitErr.Received != tt.wantReceived {
				t.Errorf(
					"error = %+v, want limit %d received %d",
					limitErr, limit, tt.wantReceived,
				)
			}
			// The rejected chunk must not have landed in the file.
			if got := w.Received(); got != int64(len(written)) {
				t.Errorf("Received() = %d, want %d", got, len(written))
			}
		})
	}
}

func TestByteStreamWriterFinish(t *testing.T) {
	dir := t.TempDir()
	w, err := newResolver(t, Config{}).OpenByteStream(dir)
	if err != nil {
		t.Fatalf("OpenByteStream: %v", err)
	}
	defer w.Close()

	want := []byte("rendered input document")
	if _, err := w.Write(want[:5]); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := w.Write(want[5:]); err != nil {
		t.Fatalf("Write: %v", err)
	}

	in, err := w.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if in.Path != filepath.Join(dir, inputFileName) {
		t.Errorf("Path = %q, want it inside the job dir %q", in.Path, dir)
	}
	if in.Size != int64(len(want)) {
		t.Errorf("Size = %d, want %d", in.Size, len(want))
	}
	got, err := os.ReadFile(in.Path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("file = %q, want %q", got, want)
	}
	if _, err := w.Write([]byte("late")); err == nil {
		t.Error("Write after Finish succeeded, want an error")
	}
}
