package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestHandleRoundTripsSpecAndResult(t *testing.T) {
	spec := Spec{
		InputPath: "/jobs/1/input.bin",
		JobDir:    "/jobs/1",
		Options: Options{
			Format: FormatWebp,
			Resize: Resize{MaxWidth: 1024, MaxHeight: 768},
			Dpi:    300,
			Pages:  PageRange{FirstPage: 1, LastPage: 2},
		},
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := Result{
		Pages:         []Page{{PageIndex: 0, Path: "/jobs/1/page-0.webp"}},
		DocumentPages: 7,
	}

	var got Spec
	var stdout bytes.Buffer
	handler := func(_ context.Context, spec Spec) (Result, error) {
		got = spec
		return want, nil
	}
	if err := Handle(t.Context(), bytes.NewReader(encoded), &stdout, handler, nil); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got != spec {
		t.Fatalf("handler received %+v, want %+v", got, spec)
	}

	f, err := decodeFrame(stdout.Bytes())
	if err != nil {
		t.Fatalf("decodeFrame: %v", err)
	}
	if f.Result == nil {
		t.Fatalf("frame = %+v, want a result", f)
	}
	if f.Result.DocumentPages != want.DocumentPages || len(f.Result.Pages) != 1 {
		t.Fatalf("frame result = %+v, want %+v", *f.Result, want)
	}
	if f.Result.Pages[0] != want.Pages[0] {
		t.Fatalf("frame page = %+v, want %+v", f.Result.Pages[0], want.Pages[0])
	}
}

func TestHandleFramesTheHandlerError(t *testing.T) {
	failure := errors.New("no pages rendered")
	var stdout bytes.Buffer
	handler := func(_ context.Context, _ Spec) (Result, error) { return Result{}, failure }

	err := Handle(t.Context(), strings.NewReader(`{"jobDir":"/jobs/1"}`), &stdout, handler, nil)
	if !errors.Is(err, failure) {
		t.Fatalf("err = %v, want the handler's error", err)
	}
	f, err := decodeFrame(stdout.Bytes())
	if err != nil {
		t.Fatalf("decodeFrame: %v", err)
	}
	if f.Error == nil || f.Error.Message != failure.Error() {
		t.Fatalf("frame error = %+v, want %q", f.Error, failure.Error())
	}
	if f.Error.Code != "" {
		t.Fatalf("frame error class = %q, want an unclassified failure", f.Error.Code)
	}
	if f.Result != nil {
		t.Fatalf("frame = %+v, want no result beside the error", f)
	}
}

func TestHandleFramesTheClassOfTheHandlerError(t *testing.T) {
	failure := errors.New("page range starts past the end")
	classify := func(err error) *JobError {
		return &JobError{
			Code:    "test.page_range",
			Message: err.Error(),
			Details: json.RawMessage(`{"firstPage":5,"documentPages":2}`),
		}
	}
	var stdout bytes.Buffer
	handler := func(_ context.Context, _ Spec) (Result, error) { return Result{}, failure }

	err := Handle(
		t.Context(),
		strings.NewReader(`{"jobDir":"/jobs/1"}`),
		&stdout,
		handler,
		classify,
	)
	if !errors.Is(err, failure) {
		t.Fatalf("err = %v, want the handler's error", err)
	}
	f, decodeErr := decodeFrame(stdout.Bytes())
	if decodeErr != nil {
		t.Fatalf("decodeFrame: %v", decodeErr)
	}
	if f.Error == nil {
		t.Fatalf("frame = %+v, want the classified failure", f)
	}
	if f.Error.Code != "test.page_range" || f.Error.Message != failure.Error() {
		t.Fatalf("frame error = %+v, want the class the classifier named", f.Error)
	}
	var got struct {
		FirstPage     int `json:"firstPage"`
		DocumentPages int `json:"documentPages"`
	}
	if err := json.Unmarshal(f.Error.Details, &got); err != nil {
		t.Fatalf("frame error details: %v", err)
	}
	if got.FirstPage != 5 || got.DocumentPages != 2 {
		t.Fatalf("frame error details = %+v, want page 5 of a 2 page document", got)
	}
}

func TestHandleFramesAnUnrecognizedErrorWithoutAClass(t *testing.T) {
	failure := errors.New("the renderer gave way")
	classify := func(error) *JobError { return nil }
	var stdout bytes.Buffer
	handler := func(_ context.Context, _ Spec) (Result, error) { return Result{}, failure }

	err := Handle(
		t.Context(),
		strings.NewReader(`{"jobDir":"/jobs/1"}`),
		&stdout,
		handler,
		classify,
	)
	if !errors.Is(err, failure) {
		t.Fatalf("err = %v, want the handler's error", err)
	}
	f, decodeErr := decodeFrame(stdout.Bytes())
	if decodeErr != nil {
		t.Fatalf("decodeFrame: %v", decodeErr)
	}
	if f.Error == nil || f.Error.Code != "" || f.Error.Message != failure.Error() {
		t.Fatalf("frame error = %+v, want the bare message of %q", f.Error, failure)
	}
}

func TestHandleFramesADecodeFailure(t *testing.T) {
	var stdout bytes.Buffer
	called := false
	handler := func(_ context.Context, _ Spec) (Result, error) {
		called = true
		return Result{}, nil
	}

	err := Handle(t.Context(), strings.NewReader("not json"), &stdout, handler, nil)
	if err == nil {
		t.Fatal("Handle: err = nil, want a spec decode error")
	}
	if called {
		t.Fatal("the handler ran on a spec that did not decode")
	}
	f, decodeErr := decodeFrame(stdout.Bytes())
	if decodeErr != nil {
		t.Fatalf("decodeFrame: %v", decodeErr)
	}
	if f.Error == nil || !strings.Contains(f.Error.Message, "decoding job spec") {
		t.Fatalf("frame error = %+v, want the decode failure", f.Error)
	}
}

func TestDecodeFrameFindsTheLastFrameAmongNoise(t *testing.T) {
	var stdout bytes.Buffer
	stdout.WriteString("a converter printed this\n")
	if err := writeFrame(
		&stdout,
		frame{Error: &JobError{Message: "an earlier frame"}},
	); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}
	stdout.WriteString("and this\n")
	if err := writeFrame(&stdout, frame{Result: &Result{DocumentPages: 2}}); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}
	stdout.WriteString("and a trailing line\n")

	f, err := decodeFrame(stdout.Bytes())
	if err != nil {
		t.Fatalf("decodeFrame: %v", err)
	}
	if f.Result == nil || f.Result.DocumentPages != 2 {
		t.Fatalf("frame = %+v, want the last frame written", f)
	}
}

func TestDecodeFrameRejectsBrokenStdout(t *testing.T) {
	full := func() []byte {
		var buf bytes.Buffer
		if err := writeFrame(&buf, frame{Result: &Result{DocumentPages: 1}}); err != nil {
			t.Fatalf("writeFrame: %v", err)
		}
		return buf.Bytes()
	}()

	for _, tt := range []struct {
		name   string
		stdout []byte
		want   error
	}{
		{
			name:   "no frame at all",
			stdout: []byte("just log lines\n"),
			want:   errNoFrame,
		},
		{
			name:   "header cut short",
			stdout: full[:len(frameMagic)+2],
		},
		{
			name:   "payload cut short",
			stdout: full[:len(full)-3],
		},
		{
			name:   "payload is not json",
			stdout: append(append([]byte{}, full[:len(full)-1]...), '{'),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeFrame(tt.stdout)
			if err == nil {
				t.Fatal("decodeFrame: err = nil, want a framing error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestTailWriterKeepsTheLastBytes(t *testing.T) {
	for _, tt := range []struct {
		name   string
		max    int
		writes []string
		want   string
	}{
		{
			name:   "under the cap",
			max:    8,
			writes: []string{"abc", "de"},
			want:   "abcde",
		},
		{
			name:   "over the cap across writes",
			max:    4,
			writes: []string{"abc", "de", "fg"},
			want:   "defg",
		},
		{
			name:   "one write larger than the cap",
			max:    3,
			writes: []string{"abcdefgh"},
			want:   "fgh",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var sink bytes.Buffer
			w := newTailWriter(tt.max, &sink)
			var total int
			for _, s := range tt.writes {
				n, err := w.Write([]byte(s))
				if err != nil {
					t.Fatalf("Write: %v", err)
				}
				total += n
			}
			if got := string(w.tail()); got != tt.want {
				t.Fatalf("tail = %q, want %q", got, tt.want)
			}
			if got := sink.String(); got != strings.Join(tt.writes, "") {
				t.Fatalf("sink = %q, want every byte forwarded", got)
			}
			if total != len(strings.Join(tt.writes, "")) {
				t.Fatalf("Write reported %d bytes, want %d", total, len(sink.String()))
			}
		})
	}
}
