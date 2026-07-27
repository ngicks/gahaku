package input

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ResolveLocalPath copies the file at path into dir.
//
// path must resolve inside one of the configured roots. Containment is
// enforced by reading through os.Root, which refuses ".." segments and
// symlinks leading out of the root, so a symlink planted inside a root cannot
// exfiltrate an outside file. os.Root also refuses absolute symlink targets,
// so a symlinked file inside a root must point at a relative target to be
// readable. The copy — rather than handing the worker the original path —
// removes the window where the verified file is swapped before the worker
// opens it.
func (r *Resolver) ResolveLocalPath(ctx context.Context, dir, path string) (Input, error) {
	if len(r.localRoots) == 0 {
		return Input{}, ErrLocalInputDisabled
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Input{}, fmt.Errorf("input: local path %q: %w", path, err)
	}
	for _, root := range r.localRoots {
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return r.copyFromRoot(ctx, dir, root, rel, path)
	}
	return Input{}, &PathNotAllowedError{Path: path}
}

func (r *Resolver) copyFromRoot(ctx context.Context, dir, root, rel, path string) (Input, error) {
	rootFs, err := os.OpenRoot(root)
	if err != nil {
		return Input{}, fmt.Errorf("input: opening local root %q: %w", root, err)
	}
	defer rootFs.Close()

	src, err := rootFs.Open(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Input{}, fmt.Errorf("input: local path %q: %w", path, err)
		}
		// Everything else os.Root rejects — traversal ("path escapes from
		// parent") and permission failures alike — is a path this job may not
		// read, so it is reported the same way.
		return Input{}, &PathNotAllowedError{Path: path, Err: err}
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return Input{}, fmt.Errorf("input: local path %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return Input{}, &PathNotAllowedError{
			Path: path,
			Err:  errors.New("not a regular file"),
		}
	}

	dst, err := createInputFile(dir)
	if err != nil {
		return Input{}, err
	}
	defer dst.Close()

	n, err := copyLimited(ctx, dst, src, 0)
	if err != nil {
		return Input{}, fmt.Errorf("input: copying local path %q: %w", path, err)
	}
	if err := dst.Close(); err != nil {
		return Input{}, fmt.Errorf("input: closing input file: %w", err)
	}
	return Input{Path: dst.Name(), Size: n}, nil
}
