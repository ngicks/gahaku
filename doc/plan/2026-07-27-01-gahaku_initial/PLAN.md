# gahaku — image rendering gRPC service (initial plan)

gahaku is a gRPC service that renders input documents (images, PDF, MS Office
files) into raster images, running each rendering job in a subprocess worker so
the OS OOM killer / orchestrator timeouts can reap runaway jobs.

## Goal / success criteria

- A gRPC server exposing:
  - Typed APIs — caller asserts the input kind: `RenderImage`, `RenderPdf`,
    `RenderOffice`.
  - Untyped API — `Render` sniffs the content type and routes to the matching
    typed implementation.
- Input delivery, both supported by every API:
  - Direct byte stream (size-limited; oversized input rejected with
    `INVALID_ARGUMENT` / `RESOURCE_EXHAUSTED`).
  - URI: a presigned HTTP(S) URL for object storage (GET for input, PUT for
    output), or a local file path under an allowlisted root.
- Deployment: quadlet units under `deployment/quadlet/` run versitygw (S3-
  compatible gateway) as the object-storage backend; units are verified by a
  podman-in-podman test harness (this dev environment has no systemd, so
  quadlet cannot run directly here).
- Rendering backends:
  - Images — image-to-image conversion: resize and/or format change.
  - PDF — rasterized via `go-pdfium` in WebAssembly mode.
  - MS Office — converted by invoking `soffice` (LibreOffice) directly.
- Isolation: rendering work runs in subprocesses. The orchestrator kills a
  worker on deadline; the kernel OOM killer can kill it on memory blowup;
  either results in a clean gRPC error, never a crashed server.
- `go build ./...`, `go vet ./...`, `golangci-lint run` clean; unit tests for
  routing/limits/orchestration; e2e test rendering at least one file per kind.

## Scope / non-goals

In scope:

- The gRPC API surface, proto definitions, generated code.
- Input resolution (bytes / local file / presigned URL) with size limits.
- Subprocess worker orchestration (spawn, IPC, timeout kill, concurrency cap).
- The three rendering backends and the sniffing router.
- A `serve` CLI entrypoint plus hidden worker subcommands.
- Quadlet deployment units (versitygw object-storage backend) and a
  podman-in-podman harness that tests them without host systemd.

Non-goals (for this plan):

- AuthN/AuthZ, TLS termination (assumed to be handled by the deployment
  environment; hooks can be added later).
- Video, SVG-to-raster, or other document kinds beyond image/PDF/Office.
- Horizontal scaling / job queueing across machines.

## Context

- Repo: `github.com/ngicks/gahaku`, currently empty of Go code (only
  `.golangci.yaml`, APM config, `AGENTS.md`). Module must be initialized.
- Conventions from `AGENTS.md`:
  - `./cmd` is thin — flags only, hands off to services; use `go-edit-cobra`
    skill when touching it.
  - errgroup/semaphore over hand-rolled sync; context-first APIs; DI over
    globals; small consumer-side interfaces.
- `soffice` and a WASI-capable environment are runtime dependencies; the
  Docker image must bundle LibreOffice.
- This dev environment has no systemd, so quadlet units cannot be exercised
  directly; podman-in-podman (a privileged podman container running systemd
  as PID 1 with podman inside) is available and is the test vehicle for
  deployment units.

## Approach

### Repository layout (proposed)

```
api/
  buf.yaml                        buf module config (buf.gen.yaml beside it)
  schema/proto/ngicks/gahaku/v1/  .proto sources (package ngicks.gahaku.v1)
  gen/proto/go/ngicks/gahaku/v1/  generated Go (committed)
cmd/gahaku/                       cobra CLI: serve + hidden worker subcommands
deployment/
  quadlet/                        units: versitygw/gahaku .container, network, volume
  test/                           podman-in-podman harness verifying the quadlet units
pkg/
  input/                          input resolution: bytes | file | presigned GET; size limits
  output/                         page sinks: response-stream chunker | presigned PUT uploader
  sniff/                          content-type detection + kind mapping
  worker/                         subprocess orchestrator: spawn, IPC, deadline kill, semaphore
  render/                         shared render option types (size, format, pages, dpi)
  render/imagejob/                image-to-image conversion
  render/pdfjob/                  go-pdfium (wasm) rasterization
  render/officejob/               soffice invocation
  server/                         gRPC service implementation, typed + untyped wiring
```

### API shape (decided — D-01, D-02)

All four RPCs are bidirectional streams with a header-then-chunks protocol:

- `Render(stream RenderRequest) → stream RenderResponse` (untyped, sniffing)
- `RenderImage`, `RenderPdf`, `RenderOffice` — same message shapes, but the
  input kind is asserted rather than sniffed.

Request stream: first message is a header (`RenderOptions` + input source +
output spec), followed by `bytes chunk` messages when the input is delivered
as a direct byte stream (D-02: client-streamed chunks, default cumulative
limit 32 MiB — configurable; exceeding it aborts with `RESOURCE_EXHAUSTED`
before a worker is spawned). URI-sourced inputs send the header only.

Input source (`oneof`): direct byte stream, or URI — a presigned HTTP(S) GET
URL for object storage (D-09, supersedes D-03), or a local file under an
allowlisted root (D-04).

Output spec (D-01) — the request chooses one of two styles:

- **Bytes**: rendered pages stream back as `(page_index, chunk)` response
  messages, one framed sequence per page in the selected page range.
- **URIs**: the request carries a list of destination presigned HTTP(S) PUT
  URLs; the server uploads page N of the page range to the Nth URL and
  streams back one per-page completion message. The URL count MUST equal the
  page-range length — validated up front (`INVALID_ARGUMENT` on mismatch;
  `OUT_OF_RANGE` if the document turns out to have fewer pages than the
  range).

Presigned URLs mean the service holds no storage credentials: the caller
presigns against its own store (versitygw, S3, MinIO, …) and gahaku performs
plain HTTP GET/PUT.

`RenderOptions` carries target format, max dimensions/resize spec, DPI, and
the page range (single-image inputs treat the range as page 1 only).

### Worker model

- Single binary: the server re-executes itself (`/proc/self/exe`) with a
  hidden subcommand (`gahaku worker pdf` etc.) per job or per worker pool
  slot. One binary keeps deploys simple and versions in lockstep.
- IPC: job spec on stdin, result on stdout (framed), logs on stderr. Input
  files are handed to workers as paths in a per-job temp dir (large payloads
  never traverse pipes twice).
- Orchestrator (`pkg/worker`): weighted semaphore caps concurrent
  workers; per-job `context.Context` deadline; on expiry the process group is
  SIGKILLed; non-zero exit / signal death maps to a typed gRPC error
  (`DEADLINE_EXCEEDED`, `RESOURCE_EXHAUSTED` for OOM-kill, `INTERNAL`
  otherwise).
- `soffice` note: concurrent invocations need distinct
  `-env:UserInstallation=file:///tmp/...` profile dirs — one per job temp dir.

### Rendering pipelines

- Image: decode → optional resize → encode to target format. Codec set
  (D-05): stdlib + `golang.org/x/image` (png, jpeg, gif, tiff, bmp, webp
  decode) plus `gen2brain/webp` / `gen2brain/avif` wazero-based encoders —
  CGo-free throughout.
- PDF: `go-pdfium` wasm (wazero) instantiated inside the worker subprocess;
  render selected pages at requested DPI.
- Office: `soffice --headless --convert-to pdf` into the job temp dir, then
  reuse the PDF pipeline on the result (keeps one raster path; see
  DECISION.md stub D-06).

### Sniffing router

- Magic-byte detection via `github.com/gabriel-vasile/mimetype` (routine
  choice — broad Office/CFB coverage, prefix-only reads) maps content to
  `image/*`, `application/pdf`, or an Office MIME set (OOXML + legacy CFB +
  OpenDocument?); unknown types get `UNIMPLEMENTED`/`INVALID_ARGUMENT`.
- For URI inputs, sniffing reads only a prefix (e.g. first 3072 bytes) before
  committing to a pipeline.

### Deployment via quadlet (versitygw)

- `deployment/quadlet/` holds rootless podman quadlet units (D-12), installed to
  `~/.config/containers/systemd/`; full stack (D-10):
  - `versitygw.container` — versitygw with its POSIX backend over a volume,
    root credentials from an env file / podman secret, S3 port published on
    an unprivileged port (7070).
  - `gahaku.container` — the gahaku server image (soffice bundled).
  - `gahaku.network` + `versitygw.volume` supporting units.
- versitygw is the S3-compatible endpoint callers presign against in
  self-hosted deployments; gahaku itself only ever sees presigned URLs, so
  no unit passes storage credentials to gahaku.
- Container image reference and env var names (`ROOT_ACCESS_KEY` /
  `ROOT_SECRET_KEY`, `versitygw posix <dir>`) to be verified against the
  versitygw release docs at implementation time.

### Quadlet testing via podman-in-podman

No systemd on the dev host, so quadlet units are tested inside a container
that runs systemd as PID 1 with podman available (e.g.
`quay.io/podman/stable` started with `--privileged --systemd=always` and
`/usr/sbin/init`):

1. Outer podman starts the systemd container, bind-mounting
   `deployment/quadlet/` to `/etc/containers/systemd/`.
2. Inside, `systemctl daemon-reload` runs the quadlet generator; the harness
   waits for `versitygw.service` to become active.
3. Smoke test: create a bucket, presign a GET and a PUT (`aws-sdk-go-v2`
   presign client, test files only — D-11), round-trip an object through
   the presigned URLs against the in-container versitygw.
4. Harness lives in `deployment/test/` as a script + Go test gated behind a
   build tag / env var (`GAHAKU_TEST_QUADLET=1`), skipped when podman is
   absent.

The presigned-URL I/O code paths themselves (`pkg/input`/`pkg/output`) are
additionally tested without quadlet: a plain `podman run` versitygw (or
in-process fake S3 via httptest) inside ordinary integration tests, so the
common path stays fast and unprivileged.

### Rejected alternatives

- go-pdfium in CGo / hashicorp-plugin mode — wasm mode chosen per requirement;
  no native pdfium distribution problem, and the worker-subprocess design
  already provides isolation.
- Long-lived in-process rendering — defeats the OOM-kill/timeout-kill
  requirement.
- Separate worker binaries — more artifacts to ship, version skew risk;
  self-exec keeps one artifact.

## Implementation steps

1. **Module + toolchain**: `go mod init github.com/ngicks/gahaku`; `buf`
   setup under `api/` (`api/buf.yaml`, `api/buf.gen.yaml`; D-07);
   `Taskfile`/`Makefile` targets for generate/build/test.
2. **Proto + codegen**: `api/schema/proto/ngicks/gahaku/v1/gahaku.proto`
   (package `ngicks.gahaku.v1`) defining the four RPCs, `Source` oneof,
   `RenderOptions`, error detail messages; commit generated code under
   `api/gen/proto/go/ngicks/gahaku/v1`.
3. **`pkg/input`**: `Source` resolver — client-streamed bytes
   (cumulative-limit-enforcing reader), local file under allowlisted roots,
   presigned HTTP(S) GET via `net/http`; materializes input into a job temp
   dir; unit tests for limit enforcement, root allowlisting, URL scheme
   handling (httptest servers stand in for object storage). Companion
   **`pkg/output`**: page sink — response-stream chunker or presigned
   HTTP(S) PUT uploader with the count==range check and per-page retry.
4. **`pkg/worker`**: orchestrator with semaphore, self-exec spawn, stdio
   job protocol, process-group kill on deadline, exit-status→error mapping;
   tests use a fake worker subcommand that sleeps/allocates/exits nonzero.
5. **`pkg/render/imagejob`**: decode/resize/encode; worker subcommand
   `gahaku worker image`; golden-file tests.
6. **`pkg/render/pdfjob`**: go-pdfium wasm rasterization; worker subcommand
   `gahaku worker pdf`; test with a small fixture PDF.
7. **`pkg/render/officejob`**: soffice invocation (per-job profile dir) →
   PDF → pdfjob; worker subcommand `gahaku worker office`; test gated on
   soffice presence (skip otherwise), always-on test for command
   construction.
8. **`pkg/sniff`**: detection + kind routing table; table-driven tests
   over fixture headers.
9. **`pkg/server` + `cmd/gahaku`**: gRPC services wiring input → sniff →
   worker; `serve` command (flags: listen addr, limits, concurrency, temp
   dir, allowed URI roots); hidden `worker` subcommands.
10. **E2E + packaging**: end-to-end test over a real listener rendering one
    fixture per kind, including a presigned-URL round trip against a
    `podman run` versitygw (skipped without podman); Dockerfile bundling
    soffice; CI workflow.
11. **Quadlet deployment + harness**: `deployment/quadlet/` rootless units
    (`versitygw.container`, `gahaku.container`, `gahaku.network`,
    `versitygw.volume`); `deployment/test/` podman-in-podman harness per
    the approach section, gated on `GAHAKU_TEST_QUADLET=1`.

Each step compiles and passes tests independently before the next begins.

## Testing & verification

- Unit: input limits, URI resolution, sniff table, worker lifecycle (timeout
  kill, OOM exit mapping), option validation.
- Golden: image conversion outputs (dimension + format asserts rather than
  byte-exact where encoders vary).
- E2E: bufconn/real listener, all four RPCs, oversized-bytes rejection,
  timeout-kill path (worker subcommand that sleeps).
- Environment-gated: office tests skip when `soffice` is absent; CI image
  provides it.
- Integration: presigned GET/PUT round trip against a `podman run`
  versitygw; skipped when podman is unavailable.
- Deployment: podman-in-podman quadlet convergence test
  (`GAHAKU_TEST_QUADLET=1`) — units generate, `versitygw.service` goes
  active, presigned round trip succeeds inside the systemd container.

## Risks

- `soffice` startup latency (~seconds) per job; mitigations later (pool of
  warm profiles) if throughput matters — out of scope now.
- go-pdfium wasm mode is slower than native and single-threaded per instance;
  acceptable since parallelism comes from multiple workers.
- OOM-kill detection is heuristic (SIGKILL + cgroup/dmesg not portable);
  we map SIGKILL death to `RESOURCE_EXHAUSTED` with a note, which may
  occasionally mislabel an operator kill.
- Local-file URIs are an SSRF/path-traversal surface — must be restricted to
  configured root(s), deny-by-default (D-04). Presigned HTTP URLs are still
  an SSRF vector (the server GETs/PUTs caller-supplied URLs); mitigations:
  https-required-by-default, optional host allowlist, private-address-range
  blocking flag.
- Presigned URLs expire — a PUT presign must outlive queue wait + render
  time; document that callers should presign with generous expiry, and map
  storage 403s on upload to a clear `FAILED_PRECONDITION` detail.
- Podman-in-podman needs `--privileged` (or careful userns tuning); CI
  runners must allow it, and the quadlet test stays opt-in so local runs
  never require elevated podman.
- versitygw image/flag details are from memory — verify image ref, env var
  names, and healthcheck endpoint against upstream docs during step 11.

## Open questions

None — Q1–Q12 all resolved 2026-07-27; see DECISION.md D-01…D-13.
