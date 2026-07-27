package worker

import "io"

// tailWriter keeps the last max bytes written to it and forwards every write to
// sink, if there is one.
//
// Writes never fail: os/exec reports a write error out of Cmd.Wait, where it
// would be indistinguishable from the worker itself failing, so neither the cap
// nor a log sink that rejects a write may fail a job.
type tailWriter struct {
	max  int
	buf  []byte
	sink io.Writer
}

func newTailWriter(max int, sink io.Writer) *tailWriter {
	return &tailWriter{max: max, sink: sink}
}

func (w *tailWriter) Write(p []byte) (int, error) {
	if w.sink != nil {
		_, _ = w.sink.Write(p)
	}
	kept := p
	if len(kept) > w.max {
		kept = kept[len(kept)-w.max:]
	}
	w.buf = append(w.buf, kept...)
	if len(w.buf) > w.max {
		// Overlapping copy slides the retained bytes to the front, so a chatty
		// worker cannot grow the buffer past the cap.
		w.buf = w.buf[:copy(w.buf, w.buf[len(w.buf)-w.max:])]
	}
	return len(p), nil
}

// tail returns the retained bytes. os/exec's copying goroutines have finished
// by the time Cmd.Wait returns, on the WaitDelay path too, so the caller reads
// this without further synchronization.
func (w *tailWriter) tail() []byte { return w.buf }
