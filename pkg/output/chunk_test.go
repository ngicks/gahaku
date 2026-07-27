package output

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// frame is a PageChunk captured with its payload copied, since the sink reuses
// its buffer between sends.
type frame struct {
	pageIndex int
	chunk     string
	last      bool
}

func collector(frames *[]frame) SendFunc {
	return func(c PageChunk) error {
		*frames = append(*frames, frame{
			pageIndex: c.PageIndex,
			chunk:     string(c.Chunk),
			last:      c.Last,
		})
		return nil
	}
}

func TestChunkSinkFraming(t *testing.T) {
	for _, tt := range []struct {
		name      string
		page      string
		chunkSize int
		want      []frame
	}{
		{
			name:      "page shorter than one chunk",
			page:      "abc",
			chunkSize: 4,
			want:      []frame{{chunk: "abc", last: true}},
		},
		{
			name:      "page split across chunks",
			page:      "abcde",
			chunkSize: 2,
			want: []frame{
				{chunk: "ab"},
				{chunk: "cd"},
				{chunk: "e", last: true},
			},
		},
		{
			name:      "page that is an exact multiple of the chunk size",
			page:      "abcd",
			chunkSize: 2,
			want: []frame{
				{chunk: "ab"},
				{chunk: "cd", last: true},
			},
		},
		{
			name:      "empty page still marks its boundary",
			page:      "",
			chunkSize: 4,
			want:      []frame{{chunk: "", last: true}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var frames []frame
			sink := NewChunkSink(collector(&frames), tt.chunkSize)

			if err := sink.WritePage(t.Context(), 0, strings.NewReader(tt.page)); err != nil {
				t.Fatalf("WritePage: %v", err)
			}
			if err := sink.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if len(frames) != len(tt.want) {
				t.Fatalf("frames = %v, want %v", frames, tt.want)
			}
			for i, want := range tt.want {
				if frames[i] != want {
					t.Errorf("frame %d = %v, want %v", i, frames[i], want)
				}
			}
		})
	}
}

func TestChunkSinkKeepsPageIndex(t *testing.T) {
	var frames []frame
	sink := NewChunkSink(collector(&frames), 3)

	pages := []string{"first page", "second"}
	for i, page := range pages {
		if err := sink.WritePage(t.Context(), i, strings.NewReader(page)); err != nil {
			t.Fatalf("WritePage(%d): %v", i, err)
		}
	}

	var rebuilt [2]bytes.Buffer
	lasts := 0
	for _, f := range frames {
		rebuilt[f.pageIndex].WriteString(f.chunk)
		if f.last {
			lasts++
		}
	}
	for i, page := range pages {
		if rebuilt[i].String() != page {
			t.Errorf("page %d reassembled to %q, want %q", i, rebuilt[i].String(), page)
		}
	}
	if lasts != len(pages) {
		t.Errorf("last flags = %d, want one per page (%d)", lasts, len(pages))
	}
}

func TestChunkSinkSendError(t *testing.T) {
	sendErr := errors.New("stream closed")
	sent := 0
	sink := NewChunkSink(func(PageChunk) error {
		sent++
		return sendErr
	}, 2)

	err := sink.WritePage(t.Context(), 0, strings.NewReader("abcdef"))
	if !errors.Is(err, sendErr) {
		t.Fatalf("err = %v, want %v", err, sendErr)
	}
	if sent != 1 {
		t.Errorf("send calls = %d, want the sink to stop at the first failure", sent)
	}
}

func TestChunkSinkHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	sink := NewChunkSink(func(PageChunk) error {
		t.Error("send called on a cancelled context")
		return nil
	}, 2)

	err := sink.WritePage(ctx, 0, strings.NewReader("abcdef"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestChunkSinkDefaultChunkSize(t *testing.T) {
	var frames []frame
	sink := NewChunkSink(collector(&frames), 0)

	page := strings.Repeat("x", DefaultChunkSize+1)
	if err := sink.WritePage(t.Context(), 0, strings.NewReader(page)); err != nil {
		t.Fatalf("WritePage: %v", err)
	}
	if len(frames) != 2 || len(frames[0].chunk) != DefaultChunkSize {
		t.Fatalf("frame sizes = %d chunks, first %d bytes", len(frames), len(frames[0].chunk))
	}
}

// The sinks are interchangeable for pkg/server, which holds whichever one the
// request's output spec selected behind an interface of its own.
func TestSinksShareOneShape(t *testing.T) {
	type pageSink interface {
		WritePage(ctx context.Context, pageIndex int, page io.ReadSeeker) error
		Close() error
	}

	putSink, err := NewPutSink(PutSinkConfig{Urls: []string{"https://example.test/0"}})
	if err != nil {
		t.Fatalf("NewPutSink: %v", err)
	}
	var _ pageSink = NewChunkSink(nil, 0)
	var _ pageSink = putSink
}
