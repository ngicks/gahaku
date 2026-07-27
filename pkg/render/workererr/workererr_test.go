package workererr_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/ngicks/gahaku/pkg/render"
	"github.com/ngicks/gahaku/pkg/render/officejob"
	"github.com/ngicks/gahaku/pkg/render/workererr"
	"github.com/ngicks/gahaku/pkg/worker"
)

// crossing is the trip a failure makes: classified in the worker, marshaled
// into the result frame, read back by the orchestrator and restored there.
func crossing(t *testing.T, err error) error {
	t.Helper()
	classified := workererr.Classify(err)
	if classified == nil {
		t.Fatalf("Classify(%v) = nil, want a class", err)
	}
	encoded, marshalErr := json.Marshal(classified)
	if marshalErr != nil {
		t.Fatalf("Marshal: %v", marshalErr)
	}
	var decoded worker.JobError
	if unmarshalErr := json.Unmarshal(encoded, &decoded); unmarshalErr != nil {
		t.Fatalf("Unmarshal: %v", unmarshalErr)
	}
	return workererr.Restore(&decoded)
}

// TestAClassifiedFailureComesBackTyped states what survives the crossing: the
// type the status mapping branches on and the numbers it reads, with the whole
// reported message as the restored error's cause. What a class does not carry —
// the office converter's binary, its exit status — is gone by design.
func TestAClassifiedFailureComesBackTyped(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want error
	}{
		{
			name: "a document the pipeline could not decode",
			err:  &render.DecodeError{Err: errors.New("unexpected EOF")},
			want: &render.DecodeError{
				Err: errors.New("render: decoding input document: unexpected EOF"),
			},
		},
		{
			name: "an output format nothing encodes",
			err:  &render.UnsupportedFormatError{Format: worker.Format("xyz")},
			want: &render.UnsupportedFormatError{Format: worker.Format("xyz")},
		},
		{
			name: "a page range that ends before it starts",
			err:  &render.InvalidPageRangeError{FirstPage: 5, LastPage: 2},
			want: &render.InvalidPageRangeError{FirstPage: 5, LastPage: 2},
		},
		{
			name: "a page range starting past the end",
			err:  &render.PageRangeOutOfRangeError{FirstPage: 5, DocumentPages: 2},
			want: &render.PageRangeOutOfRangeError{FirstPage: 5, DocumentPages: 2},
		},
		{
			name: "a deployment without LibreOffice",
			err: &officejob.ConverterMissingError{
				Binary: "soffice",
				Err:    errors.New("not found"),
			},
			want: &officejob.ConverterMissingError{
				Err: errors.New(`officejob: no LibreOffice to run as "soffice": not found`),
			},
		},
		{
			name: "a document soffice could not convert",
			err:  &officejob.ConversionError{ExitCode: 1, Err: errors.New("exit status 1")},
			want: &officejob.ConversionError{
				Err: errors.New("officejob: soffice failed: exit status 1"),
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := crossing(t, tt.err)

			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("restored %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestTheReportedMessageCrossesWhole(t *testing.T) {
	err := &render.PageRangeOutOfRangeError{FirstPage: 5, DocumentPages: 2}

	classified := workererr.Classify(err)

	if classified.Message != err.Error() {
		t.Fatalf("Message = %q, want %q", classified.Message, err.Error())
	}
}

func TestAFailureNoClassCoversIsLeftAlone(t *testing.T) {
	failure := errors.New("the renderer gave way")

	if classified := workererr.Classify(failure); classified != nil {
		t.Fatalf("Classify = %+v, want no class", classified)
	}
	if got := workererr.Restore(failure); got != failure {
		t.Fatalf("Restore = %v, want the error unchanged", got)
	}
}

// TestAnUnknownClassIsLeftAlone covers the worker error a build does not
// understand, which pkg/server maps like any other worker failure.
func TestAnUnknownClassIsLeftAlone(t *testing.T) {
	jobErr := &worker.JobError{Code: "render.from_another_build", Message: "who knows"}

	if got := workererr.Restore(jobErr); got != error(jobErr) {
		t.Fatalf("Restore = %v (%T), want the *worker.JobError unchanged", got, got)
	}
}
