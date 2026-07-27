package input

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// localFixture lays out a root the resolver allows next to a directory it must
// never reach, plus the symlinks that try to bridge them.
type localFixture struct {
	root    string
	outside string
}

func newLocalFixture(t *testing.T) localFixture {
	t.Helper()
	// The resolver stores roots symlink-free, so the fixture compares against
	// a symlink-free temp dir too (/tmp is a symlink on some systems).
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	f := localFixture{
		root:    filepath.Join(base, "root"),
		outside: filepath.Join(base, "outside"),
	}
	for _, dir := range []string{f.root, filepath.Join(f.root, "sub"), f.outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}
	writeFile(t, filepath.Join(f.root, "doc.pdf"), "allowed")
	writeFile(t, filepath.Join(f.root, "sub", "nested.pdf"), "nested")
	writeFile(t, filepath.Join(f.outside, "secret.txt"), "secret")

	symlink(t, filepath.Join(f.outside, "secret.txt"), filepath.Join(f.root, "escape.pdf"))
	symlink(t, f.outside, filepath.Join(f.root, "escapedir"))
	symlink(t, filepath.Join("sub", "nested.pdf"), filepath.Join(f.root, "inside.pdf"))
	symlink(t, filepath.Join(f.root, "doc.pdf"), filepath.Join(f.root, "absolute.pdf"))
	return f
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
}

func TestResolveLocalPathDeniedByDefault(t *testing.T) {
	f := newLocalFixture(t)
	r := newResolver(t, Config{})

	_, err := r.ResolveLocalPath(t.Context(), t.TempDir(), filepath.Join(f.root, "doc.pdf"))
	if !errors.Is(err, ErrLocalInputDisabled) {
		t.Fatalf("err = %v, want ErrLocalInputDisabled", err)
	}
}

func TestResolveLocalPathAllowed(t *testing.T) {
	f := newLocalFixture(t)
	r := newResolver(t, Config{LocalRoots: []string{f.root}})

	for _, tt := range []struct {
		name string
		path string
		want string
	}{
		{name: "file in the root", path: filepath.Join(f.root, "doc.pdf"), want: "allowed"},
		{
			name: "file in a subdirectory",
			path: filepath.Join(f.root, "sub", "nested.pdf"),
			want: "nested",
		},
		{
			name: "relative symlink staying inside the root",
			path: filepath.Join(f.root, "inside.pdf"),
			want: "nested",
		},
		{
			name: "traversal that lands back inside the root",
			path: filepath.Join(f.root, "sub", "..", "doc.pdf"),
			want: "allowed",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			in, err := r.ResolveLocalPath(t.Context(), dir, tt.path)
			if err != nil {
				t.Fatalf("ResolveLocalPath: %v", err)
			}
			if in.Path != filepath.Join(dir, inputFileName) {
				t.Errorf("Path = %q, want it inside the job dir %q", in.Path, dir)
			}
			if in.Size != int64(len(tt.want)) {
				t.Errorf("Size = %d, want %d", in.Size, len(tt.want))
			}
			got, err := os.ReadFile(in.Path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("copied content = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveLocalPathNotAllowed(t *testing.T) {
	f := newLocalFixture(t)
	r := newResolver(t, Config{LocalRoots: []string{f.root}})

	for _, tt := range []struct {
		name string
		path string
	}{
		{name: "outside every root", path: filepath.Join(f.outside, "secret.txt")},
		{
			name: "traversal out of the root",
			path: filepath.Join(f.root, "..", "outside", "secret.txt"),
		},
		{
			name: "traversal through a subdirectory",
			path: filepath.Join(f.root, "sub", "..", "..", "outside", "secret.txt"),
		},
		{name: "symlink escaping the root", path: filepath.Join(f.root, "escape.pdf")},
		{
			// os.Root refuses absolute symlink targets outright, so one is
			// rejected even while it points back inside the root.
			name: "absolute symlink inside the root",
			path: filepath.Join(f.root, "absolute.pdf"),
		},
		{
			name: "directory symlink escaping the root",
			path: filepath.Join(f.root, "escapedir", "secret.txt"),
		},
		{name: "the root itself", path: f.root},
		{name: "a directory inside the root", path: filepath.Join(f.root, "sub")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := r.ResolveLocalPath(t.Context(), t.TempDir(), tt.path)
			if _, ok := errors.AsType[*PathNotAllowedError](err); !ok {
				t.Fatalf("err = %v, want *PathNotAllowedError", err)
			}
		})
	}
}

func TestResolveLocalPathMissingFile(t *testing.T) {
	f := newLocalFixture(t)
	r := newResolver(t, Config{LocalRoots: []string{f.root}})

	_, err := r.ResolveLocalPath(t.Context(), t.TempDir(), filepath.Join(f.root, "gone.pdf"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
	if _, ok := errors.AsType[*PathNotAllowedError](err); ok {
		t.Error("a missing file inside a root must not report as not allowed")
	}
}

func TestNewRejectsUnusableRoot(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "file"), "not a directory")

	for _, tt := range []struct {
		name string
		root string
	}{
		{name: "missing directory", root: filepath.Join(base, "missing")},
		{name: "not a directory", root: filepath.Join(base, "file")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(Config{LocalRoots: []string{tt.root}}); err == nil {
				t.Fatal("New succeeded, want an error")
			}
		})
	}
}
