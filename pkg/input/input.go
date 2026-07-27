// Package input materializes a render job's input document into a file inside
// the job's temp directory, where the worker subprocess reads it from.
//
// Each delivery kind carries its own guardrail: client-streamed chunks are
// capped at a cumulative byte limit enforced while the chunks arrive, local
// paths are accepted only under allowlisted roots, and presigned URLs are
// fetched over HTTP(S) under scheme and address policy.
//
// Nothing here knows about protobuf or gRPC; failures are typed errors that
// pkg/server maps to status codes.
package input

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// DefaultMaxStreamBytes is the cumulative cap on client-streamed input (D-02).
const DefaultMaxStreamBytes int64 = 32 << 20

// inputFileName is the name every source materializes to inside the job
// directory; one job has exactly one input document.
const inputFileName = "input.bin"

// Input is a materialized input document.
type Input struct {
	// Path is the file inside the job directory handed to the worker.
	Path string
	// Size is the materialized byte count.
	Size int64
}

// Config configures a Resolver. The zero value is usable: it denies local
// paths, requires https, and caps streamed input at DefaultMaxStreamBytes.
type Config struct {
	// MaxStreamBytes caps the cumulative size of client-streamed input.
	// 0 selects DefaultMaxStreamBytes, a negative value disables the cap.
	MaxStreamBytes int64
	// MaxFetchBytes caps the body of a presigned GET. 0 disables the cap.
	MaxFetchBytes int64
	// LocalRoots are the directories a local path may resolve under. Empty
	// disables local-path input entirely (D-04).
	LocalRoots []string
	// AllowHttp accepts plain http:// presigned URLs; https-only otherwise.
	AllowHttp bool
	// BlockPrivateAddresses refuses connections to loopback, private,
	// link-local, multicast and unspecified addresses. It also disables proxy
	// use, which would hide the target address from the check.
	BlockPrivateAddresses bool
	// HttpClient fetches presigned URLs. A nil client gets one built from this
	// config; a supplied client's transport then owns address policy, but the
	// resolver still installs its own redirect policy on a copy of it so the
	// scheme check cannot be dropped.
	HttpClient *http.Client
}

// Resolver materializes render input. It is safe for concurrent use as long
// as each job passes its own directory.
type Resolver struct {
	maxStreamBytes int64
	maxFetchBytes  int64
	localRoots     []string
	allowHttp      bool
	client         *http.Client
}

// New validates cfg and returns a Resolver. Local roots are resolved to
// absolute, symlink-free directories once here so every later containment
// check compares against a stable path.
func New(cfg Config) (*Resolver, error) {
	roots := make([]string, 0, len(cfg.LocalRoots))
	for _, root := range cfg.LocalRoots {
		resolved, err := resolveRoot(root)
		if err != nil {
			return nil, err
		}
		roots = append(roots, resolved)
	}

	maxStream := cfg.MaxStreamBytes
	if maxStream == 0 {
		maxStream = DefaultMaxStreamBytes
	}

	client := newHttpClient(cfg)

	return &Resolver{
		maxStreamBytes: maxStream,
		maxFetchBytes:  cfg.MaxFetchBytes,
		localRoots:     roots,
		allowHttp:      cfg.AllowHttp,
		client:         client,
	}, nil
}

// HttpClient returns the client the resolver fetches presigned URLs with, so a
// caller writing back to presigned URLs (pkg/output) can reach the destinations
// under the same scheme and address policy the sources are read under.
func (r *Resolver) HttpClient() *http.Client { return r.client }

func resolveRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("input: local root %q: %w", root, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("input: local root %q: %w", root, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("input: local root %q: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("input: local root %q is not a directory", root)
	}
	return resolved, nil
}

// newHttpClient builds the fetch client, or adopts the caller's one, and in
// both cases installs the redirect policy: the scheme of the caller's url says
// nothing about where a redirect leads, and Go's default policy would happily
// follow an https url down to plain http on another host.
func newHttpClient(cfg Config) *http.Client {
	client := &http.Client{Transport: newTransport(cfg.BlockPrivateAddresses)}
	if cfg.HttpClient != nil {
		copied := *cfg.HttpClient
		client = &copied
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("input: stopped after %d redirects", maxRedirects)
		}
		return checkScheme(req.URL.Scheme, cfg.AllowHttp)
	}
	return client
}

// maxRedirects mirrors the hop limit of net/http's default policy, which an
// explicit CheckRedirect replaces.
const maxRedirects = 10

// newTransport clones the default transport so its timeout defaults survive,
// replacing only the dial step where the address policy applies.
func newTransport(blockPrivate bool) http.RoundTripper {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if blockPrivate {
		dialer.Control = blockPrivateAddress
		// A proxy would make every dial go to the proxy's address instead of
		// the target's, leaving the check above inspecting the wrong host, so
		// blocking and proxying are mutually exclusive here.
		transport.Proxy = nil
	}
	transport.DialContext = dialer.DialContext
	return transport
}

// blockPrivateAddress runs after name resolution and before the connection is
// made, so a hostname that resolves to an internal address is caught even when
// the name itself looked public.
func blockPrivateAddress(_, address string, _ syscall.RawConn) error {
	addrPort, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("input: parsing dial address %q: %w", address, err)
	}
	addr := addrPort.Addr().Unmap()
	if isBlockedAddress(addr) {
		return &AddressBlockedError{Address: addr}
	}
	return nil
}

func isBlockedAddress(addr netip.Addr) bool {
	return !addr.IsValid() ||
		addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() ||
		addr.IsMulticast()
}

// copyLimited copies src into dst, checking ctx between reads and failing with
// *LimitExceededError once more than limit bytes arrive. limit <= 0 copies
// without a cap.
func copyLimited(ctx context.Context, dst io.Writer, src io.Reader, limit int64) (int64, error) {
	reader := io.Reader(&contextReader{ctx: ctx, r: src})
	if limit > 0 {
		// One byte past the limit is enough to tell "at the limit" from "over".
		reader = io.LimitReader(reader, limit+1)
	}
	n, err := io.Copy(dst, reader)
	if err != nil {
		return n, err
	}
	if limit > 0 && n > limit {
		return n, &LimitExceededError{Limit: limit, Received: n}
	}
	return n, nil
}

// contextReader makes an otherwise uninterruptible copy honor cancellation.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func createInputFile(dir string) (*os.File, error) {
	f, err := os.Create(filepath.Join(dir, inputFileName))
	if err != nil {
		return nil, fmt.Errorf("input: creating input file: %w", err)
	}
	return f, nil
}
