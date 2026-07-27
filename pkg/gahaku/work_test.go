package gahaku_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ngicks/gahaku/pkg/gahaku"
	"github.com/ngicks/gahaku/pkg/render/workererr"
	"github.com/ngicks/gahaku/pkg/worker"
)

// TestWorkFramesTheClassOfARenderFailure pins the wiring rather than the
// translation both sides of it are tested for: whether a failure keeps its
// class all the way to the server comes down to what a worker subcommand hands
// worker.Handle, and a nil classifier there would go unnoticed everywhere else.
func TestWorkFramesTheClassOfARenderFailure(t *testing.T) {
	jobDir := t.TempDir()
	inputPath := filepath.Join(jobDir, "input.bin")
	if err := os.WriteFile(inputPath, []byte("not an image"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	spec, err := json.Marshal(worker.Spec{InputPath: inputPath, JobDir: jobDir})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var stdout bytes.Buffer
	err = gahaku.Work(t.Context(), worker.KindImage, gahaku.WorkOption{
		Stdin:  bytes.NewReader(spec),
		Stdout: &stdout,
	})

	if err == nil {
		t.Fatal("Work: err = nil, want the pipeline's failure")
	}
	// Reading the frame back is pkg/worker's own business; what this looks for
	// is that the class reached it at all.
	want := `"code":"` + workererr.CodeDecode + `"`
	if !bytes.Contains(stdout.Bytes(), []byte(want)) {
		t.Fatalf("frame = %q, want it to carry %s", stdout.String(), want)
	}
}
