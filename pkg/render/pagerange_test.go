package render

import (
	"errors"
	"testing"

	"github.com/ngicks/gahaku/pkg/worker"
)

func TestNormalizePageRange(t *testing.T) {
	for _, tt := range []struct {
		name          string
		pages         worker.PageRange
		documentPages int
		wantFirst     int
		wantLast      int
	}{
		{
			name:          "the zero range covers the whole document",
			documentPages: 3,
			wantFirst:     1, wantLast: 3,
		},
		{
			name:          "the zero range on a one page document",
			documentPages: 1,
			wantFirst:     1, wantLast: 1,
		},
		{
			name:          "page 1 stated explicitly",
			pages:         worker.PageRange{FirstPage: 1, LastPage: 1},
			documentPages: 1,
			wantFirst:     1, wantLast: 1,
		},
		{
			name:          "open ended from page 1",
			pages:         worker.PageRange{FirstPage: 1},
			documentPages: 1,
			wantFirst:     1, wantLast: 1,
		},
		{
			name:          "open ended up to page 1",
			pages:         worker.PageRange{LastPage: 1},
			documentPages: 1,
			wantFirst:     1, wantLast: 1,
		},
		{
			name:          "a range running past the end is clamped",
			pages:         worker.PageRange{FirstPage: 1, LastPage: 5},
			documentPages: 1,
			wantFirst:     1, wantLast: 1,
		},
		{
			name:          "the middle of a document",
			pages:         worker.PageRange{FirstPage: 2, LastPage: 3},
			documentPages: 5,
			wantFirst:     2, wantLast: 3,
		},
		{
			name:          "the tail of a document",
			pages:         worker.PageRange{FirstPage: 3},
			documentPages: 5,
			wantFirst:     3, wantLast: 5,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			first, last, err := NormalizePageRange(tt.pages, tt.documentPages)
			if err != nil {
				t.Fatalf("NormalizePageRange: %v", err)
			}
			if first != tt.wantFirst || last != tt.wantLast {
				t.Errorf(
					"NormalizePageRange = %d-%d, want %d-%d",
					first, last, tt.wantFirst, tt.wantLast,
				)
			}
		})
	}
}

func TestNormalizePageRangeRejects(t *testing.T) {
	for _, tt := range []struct {
		name          string
		pages         worker.PageRange
		documentPages int
		check         func(t *testing.T, err error)
	}{
		{
			name:          "starting past the end of a one page document",
			pages:         worker.PageRange{FirstPage: 2, LastPage: 2},
			documentPages: 1,
			check: func(t *testing.T, err error) {
				if _, ok := errors.AsType[*PageRangeOutOfRangeError](err); !ok {
					t.Fatalf("err = %v, want *PageRangeOutOfRangeError", err)
				}
			},
		},
		{
			name:          "open ended, starting past the end",
			pages:         worker.PageRange{FirstPage: 2},
			documentPages: 1,
			check: func(t *testing.T, err error) {
				if _, ok := errors.AsType[*PageRangeOutOfRangeError](err); !ok {
					t.Fatalf("err = %v, want *PageRangeOutOfRangeError", err)
				}
			},
		},
		{
			name:          "starting one page past a long document",
			pages:         worker.PageRange{FirstPage: 6, LastPage: 9},
			documentPages: 5,
			check: func(t *testing.T, err error) {
				if _, ok := errors.AsType[*PageRangeOutOfRangeError](err); !ok {
					t.Fatalf("err = %v, want *PageRangeOutOfRangeError", err)
				}
			},
		},
		{
			name:          "ending before it starts",
			pages:         worker.PageRange{FirstPage: 5, LastPage: 2},
			documentPages: 9,
			check: func(t *testing.T, err error) {
				if _, ok := errors.AsType[*InvalidPageRangeError](err); !ok {
					t.Fatalf("err = %v, want *InvalidPageRangeError", err)
				}
			},
		},
		{
			// A reversed range is the caller's mistake whatever the document
			// turned out to be, so it must not surface as out of range.
			name:          "ending before it starts on a one page document",
			pages:         worker.PageRange{FirstPage: 5, LastPage: 2},
			documentPages: 1,
			check: func(t *testing.T, err error) {
				if _, ok := errors.AsType[*InvalidPageRangeError](err); !ok {
					t.Fatalf("err = %v, want *InvalidPageRangeError", err)
				}
			},
		},
		{
			name:          "a document with no pages",
			documentPages: 0,
			check: func(t *testing.T, err error) {
				if err == nil {
					t.Fatal("NormalizePageRange succeeded, want an error")
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := NormalizePageRange(tt.pages, tt.documentPages)
			tt.check(t, err)
		})
	}
}
