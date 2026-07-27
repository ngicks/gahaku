package worker

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// frameMagic opens the result frame on the worker's stdout.
//
// A worker shares its stdout with whatever a library or a converter it invoked
// decides to print there, so the frame is located by this marker instead of
// being assumed to start at byte 0. The bytes are non-textual on purpose: they
// cannot show up in a log line.
var frameMagic = []byte("\x00\x00gahaku-result\x00")

// frameHeaderSize is the magic plus the big-endian uint32 payload length.
const frameHeaderSize = len("\x00\x00gahaku-result\x00") + 4

// maxFrameBytes bounds a payload the decoder is willing to allocate for.
const maxFrameBytes = 1 << 20

// errNoFrame reports stdout that carries no result frame at all — the usual
// shape of a worker that died before it could report anything.
var errNoFrame = errors.New("no result frame on stdout")

// frame is the worker's single stdout message: the job's result, or the error
// that stopped it, with the class the worker gave it when it had one.
type frame struct {
	Result *Result   `json:"result,omitzero"`
	Error  *JobError `json:"error,omitzero"`
}

func writeFrame(w io.Writer, f frame) error {
	payload, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("worker: encoding result frame: %w", err)
	}
	buf := make([]byte, 0, frameHeaderSize+len(payload))
	buf = append(buf, frameMagic...)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(payload)))
	buf = append(buf, payload...)
	if _, err := w.Write(buf); err != nil {
		return fmt.Errorf("worker: writing result frame: %w", err)
	}
	return nil
}

// decodeFrame picks the result frame out of a worker's stdout.
//
// The last marker wins: the frame is the last thing a worker writes, so an
// earlier one can only come from stray output that happens to contain the
// marker, or from a frame the stdout cap already truncated.
func decodeFrame(stdout []byte) (frame, error) {
	start := bytes.LastIndex(stdout, frameMagic)
	if start < 0 {
		return frame{}, errNoFrame
	}
	rest := stdout[start+len(frameMagic):]
	if len(rest) < 4 {
		return frame{}, errors.New("truncated result frame header")
	}
	size := binary.BigEndian.Uint32(rest[:4])
	if size > maxFrameBytes {
		return frame{}, fmt.Errorf("result frame of %d bytes is too large", size)
	}
	rest = rest[4:]
	if uint32(len(rest)) < size {
		return frame{}, fmt.Errorf(
			"truncated result frame: %d of %d bytes", len(rest), size,
		)
	}
	var f frame
	if err := json.Unmarshal(rest[:size], &f); err != nil {
		return frame{}, fmt.Errorf("decoding result frame: %w", err)
	}
	return f, nil
}
