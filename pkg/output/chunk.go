package output

import (
	"context"
	"fmt"
	"io"
)

// DefaultChunkSize is the payload size of one response frame.
const DefaultChunkSize = 64 << 10

// SendFunc hands one frame to the caller's response stream. Keeping the sink
// on a function instead of the generated stream type keeps this package free
// of protobuf.
type SendFunc func(PageChunk) error

// ChunkSink frames rendered pages into response messages.
type ChunkSink struct {
	send      SendFunc
	chunkSize int
}

// NewChunkSink returns a sink that frames pages through send. A chunkSize of
// 0 or less selects DefaultChunkSize.
func NewChunkSink(send SendFunc, chunkSize int) *ChunkSink {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	return &ChunkSink{send: send, chunkSize: chunkSize}
}

// WritePage sends page as one or more frames carrying pageIndex, the last of
// them marked Last. An empty page still sends one empty frame, so a client
// sees every page boundary.
//
// The Chunk slice handed to the send func is reused and stays valid only until
// that call returns.
func (s *ChunkSink) WritePage(ctx context.Context, pageIndex int, page io.ReadSeeker) error {
	size, err := pageSize(page)
	if err != nil {
		return fmt.Errorf("output: measuring page %d: %w", pageIndex, err)
	}

	buf := make([]byte, s.chunkSize)
	var sent int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.ReadFull(page, buf[:min(int64(s.chunkSize), size-sent)])
		if err != nil {
			return fmt.Errorf("output: reading page %d: %w", pageIndex, err)
		}
		sent += int64(n)
		frame := PageChunk{PageIndex: pageIndex, Chunk: buf[:n], Last: sent == size}
		if err := s.send(frame); err != nil {
			return fmt.Errorf("output: sending page %d: %w", pageIndex, err)
		}
		if sent == size {
			return nil
		}
	}
}

// Close reports nothing: the response stream needs no finalization. It exists
// so ChunkSink and PutSink present the same shape to their caller.
func (s *ChunkSink) Close() error { return nil }
