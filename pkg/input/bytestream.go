package input

import (
	"errors"
	"fmt"
	"os"
)

// ByteStreamWriter accumulates the chunks of one client-streamed input.
//
// pkg/server pumps request chunks through Write as they arrive, so an
// oversized upload trips the limit while it is still being received — before
// a worker is spawned for the job.
type ByteStreamWriter struct {
	file     *os.File
	limit    int64
	received int64
	done     bool
}

// OpenByteStream creates the job's input file in dir and returns a writer that
// enforces the configured cumulative limit.
func (r *Resolver) OpenByteStream(dir string) (*ByteStreamWriter, error) {
	f, err := createInputFile(dir)
	if err != nil {
		return nil, err
	}
	return &ByteStreamWriter{file: f, limit: r.maxStreamBytes}, nil
}

// Write appends one chunk. A chunk that would push the cumulative size past
// the limit is rejected whole with *LimitExceededError and nothing is written.
func (w *ByteStreamWriter) Write(p []byte) (int, error) {
	if w.done {
		return 0, errors.New("input: write after the byte stream was finished")
	}
	if w.limit > 0 && w.received+int64(len(p)) > w.limit {
		return 0, &LimitExceededError{Limit: w.limit, Received: w.received + int64(len(p))}
	}
	n, err := w.file.Write(p)
	w.received += int64(n)
	if err != nil {
		return n, fmt.Errorf("input: writing stream chunk: %w", err)
	}
	return n, nil
}

// Received reports how many bytes have been accepted so far.
func (w *ByteStreamWriter) Received() int64 { return w.received }

// Finish closes the file and returns the materialized input. Later writes
// fail.
func (w *ByteStreamWriter) Finish() (Input, error) {
	path := w.file.Name()
	if err := w.Close(); err != nil {
		return Input{}, err
	}
	return Input{Path: path, Size: w.received}, nil
}

// Close releases the file. It is idempotent so callers can defer it next to
// Finish.
func (w *ByteStreamWriter) Close() error {
	if w.done {
		return nil
	}
	w.done = true
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("input: closing stream file: %w", err)
	}
	return nil
}
