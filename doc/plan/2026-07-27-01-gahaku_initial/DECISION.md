# DECISION LOG — gahaku initial plan

Format: one entry per material decision — choice, rationale, rejected
alternatives. Stubs below mirror PLAN.md open questions; each gets filled as
it resolves.

## Decided at planning time (from the user's brief)

### D-A: PDF rendering uses go-pdfium in WebAssembly mode

- **Choice**: `go-pdfium` wasm (wazero) runtime inside the worker subprocess.
- **Rationale**: user requirement; avoids native pdfium distribution; process
  isolation already comes from the worker model.
- **Rejected**: CGo mode, hashicorp-plugin subprocess mode of go-pdfium
  (redundant with our own subprocess layer).

### D-B: Rendering runs in subprocess workers

- **Choice**: each rendering job executes in a subprocess the orchestrator
  can kill on timeout and the kernel OOM killer can reap independently.
- **Rationale**: user requirement; keeps the gRPC server alive through
  memory blowups and hangs.
- **Rejected**: in-process rendering with runtime memory limits (Go cannot
  reliably bound third-party/wasm allocations, and a hung job would need
  cooperative cancellation).

### D-C: MS Office conversion shells out to `soffice`

- **Choice**: invoke LibreOffice `soffice` directly as the worker subprocess.
- **Rationale**: user requirement; only practical open-source converter with
  broad Office-format coverage.
- **Rejected**: pure-Go office parsers (nowhere near fidelity parity).

### D-D: Direct byte-stream input is size-limited; URI input is not

- **Choice**: enforce a configurable max size on direct bytes; URI-delivered
  inputs stream from source without that specific cap.
- **Rationale**: user requirement; keeps gRPC message memory bounded.

## Resolved 2026-07-27 (round 1)

### D-01: Output has two request-selected styles — byte stream or URI writes

- **Choice**: the request's output spec picks either (a) pages streamed back
  as `(page_index, chunk)` messages, or (b) a list of destination URIs the
  server writes pages to, returning per-page completion messages. The request
  also carries the page range; for URI output the URI count must equal the
  page-range length (`INVALID_ARGUMENT` on mismatch, `OUT_OF_RANGE` when the
  document has fewer pages than the range).
- **Rationale**: user decision — callers need both direct retrieval and
  render-into-storage; explicit per-page URIs keep naming under caller
  control.
- **Rejected**: stream-only output; first-page-only; single archive blob.

### D-02: Direct byte input is client-streamed chunks

- **Choice**: bidirectional streaming RPCs; first request message is a
  header, subsequent messages carry input chunks. Default cumulative input
  limit 32 MiB (configurable), enforced as chunks arrive.
- **Rationale**: user decision — chunking decouples the input limit from
  gRPC max message size and pairs naturally with the streamed output side.
- **Rejected**: unary `bytes` field.

### D-03: Object storage via gocloud.dev/blob — SUPERSEDED by D-09 (2026-07-27)

- **Choice**: `gocloud.dev/blob` for both URI input reads and URI output
  writes (s3://, gs://, azblob://, file://).
- **Rationale**: one dependency, uniform URI scheme handling, matches the
  multi-backend URI requirement.
- **Rejected**: aws-sdk-go-v2 S3-only; self-defined Fetcher interface.

### D-04: Local-file URIs restricted to allowlisted roots

- **Choice**: local paths accepted only under directories passed via
  `--allow-local-root`; no flag ⇒ local-file input disabled.
- **Rationale**: deny-by-default blocks path traversal / file exfiltration
  while keeping the local-file use case available.
- **Rejected**: dropping local files entirely; unrestricted access.

## Resolved 2026-07-27 (round 2)

### D-05: Image codecs — pure Go + wasm encoders

- **Choice**: stdlib `image` + `golang.org/x/image` for png/jpeg/gif/tiff/
  bmp and webp decode; `gen2brain/webp` and `gen2brain/avif` (wazero-based)
  for modern-format encode.
- **Rationale**: CGo-free, no system libraries in the image, consistent with
  the wasm-mode pdfium choice.
- **Rejected**: libvips/bimg (CGo + system dep); stdlib-only (no webp/avif
  encode).

### D-06: Office pipeline is soffice → PDF → pdfium raster

- **Choice**: `soffice --headless --convert-to pdf` into the job temp dir,
  then rasterize with the PDF pipeline.
- **Rationale**: single raster path shared across kinds; uniform page-range
  and DPI control.
- **Rejected**: soffice direct `--convert-to png` (poor paging/DPI control,
  divergent output behavior).

### D-07: Proto toolchain is buf

- **Choice**: `buf generate` with `buf.yaml`/`buf.gen.yaml`; `buf lint` and
  `buf breaking` in CI.
- **Rationale**: no protoc/plugin version juggling; standard modern setup.
- **Rejected**: raw protoc in a make target.

### D-08: One worker subprocess per job

- **Choice**: spawn, render, exit — no worker reuse.
- **Rationale**: strongest isolation; an OOM or timeout kill affects exactly
  one job, matching the core design requirement.
- **Rejected**: pooled long-lived workers (kills take out co-resident jobs;
  cross-job state leakage). Revisit only if soffice/wasm startup latency
  proves to matter.

## Round 3 (2026-07-27, versitygw/quadlet/presigned amendment)

### D-09: URI I/O uses presigned HTTP(S) URLs

- **Choice**: object-storage input is a presigned GET URL, output a list of
  presigned PUT URLs; gahaku performs plain `net/http` GET/PUT and holds no
  storage credentials. Supersedes D-03.
- **Rationale**: user decision — credential-free service, backend-agnostic
  (versitygw, S3, MinIO all presign the same way).
- **Rejected**: `gocloud.dev/blob` with in-service credentials — dropped
  entirely (Q9 resolved): the only URI kinds are presigned HTTP(S) URL and
  allowlisted local file; no credentialed s3://gs:// support.

### D-10: Quadlet units cover the full stack

- **Choice**: `versitygw.container`, `gahaku.container`, `gahaku.network`,
  `versitygw.volume` — the deployment converges from quadlet alone.
- **Rationale**: one deployable definition; the harness then tests what
  operators actually run.
- **Rejected**: versitygw-only units with gahaku deployment left to the
  operator.

### D-11: Test presigner is aws-sdk-go-v2

- **Choice**: `aws-sdk-go-v2` S3 presign client, imported from `_test.go`
  files only.
- **Rationale**: canonical SigV4 behavior; heavier module graph confined to
  tests.
- **Rejected**: minio-go (less canonical); hand-rolled SigV4 (maintenance
  burden).

### D-12: Quadlet units are rootless

- **Choice**: units documented for `~/.config/containers/systemd/`;
  versitygw published on unprivileged port 7070; volumes owned by the
  service user.
- **Rationale**: smaller blast radius; nothing in the stack needs low ports
  or root.
- **Rejected**: rootful units under `/etc/containers/systemd/`.

### D-E: Quadlet units tested via podman-in-podman

- **Choice**: `deployment/test/` harness runs a privileged systemd+podman
  container (`--systemd=always`), mounts `deployment/quadlet/` into
  `/etc/containers/systemd/`, waits for `versitygw.service`, then does a
  presigned round trip. Opt-in via `GAHAKU_TEST_QUADLET=1`.
- **Rationale**: user requirement — dev environment lacks systemd, so
  quadlet can only be exercised inside a systemd container.
- **Rejected**: requiring a systemd host for tests; skipping deployment
  testing altogether.

## Round 4 (2026-07-27, repo structure)

### D-13: Top-level layout is api/ + cmd/ + deployment/ + pkg/

- **Choice** (user-specified):
  - `api/buf.yaml` (+ `buf.gen.yaml`), proto sources under
    `api/schema/proto/ngicks/gahaku/v1/`, generated Go committed under
    `api/gen/proto/go/ngicks/gahaku/v1/`;
  - all Go service packages under `pkg/` (`pkg/input`, `pkg/output`,
    `pkg/sniff`, `pkg/worker`, `pkg/render/...`, `pkg/server`);
  - quadlet units and their podman-in-podman harness under `deployment/`
    (`deployment/quadlet/`, `deployment/test/`);
  - `cmd/` unchanged.
- **Rationale**: user decision; matches the buf-style
  `gen/proto/go/<namespace>` convention and keeps deployables separate from
  code.
- **Rejected**: prior flat layout (`proto/`, `gen/`, top-level packages,
  `deploy/quadlet/`, `test/quadlet/`).
- Routine calls within this: proto package renamed `ngicks.gahaku.v1` to
  match the schema path; `buf.gen.yaml` sits beside `api/buf.yaml`; the
  harness slot in the user's tree is `deployment/test/` (the tree listed
  only top levels).

## Implementation-time decisions (2026-07-28)

### D-14: Worker error frames carry a machine-readable error class

- **Choice**: the stdout result frame's error is a `worker.JobError{Code,
  Message, Details, Stderr}`; `worker.Handle` takes a `Classifier` hook, and
  `pkg/render/workererr` owns the bidirectional class table (`Classify` on
  the worker side, `Restore` in `pkg/server` before status mapping).
- **Rationale**: without it every worker-reported render error (corrupt
  input, page range out of range, soffice missing) crossed the stdio
  boundary as a bare string and surfaced as INTERNAL; with it subprocess
  failures map to the same gRPC codes and proto details as in-process ones.
- **Rejected**: pkg/worker importing render error types directly (import
  cycle; worker stays proto- and render-agnostic); exit-code-based
  classification (too coarse, no structured detail fields).

### D-15: Gateway credentials come from an EnvironmentFile, not a podman secret

- **Choice**: `versitygw.container` reads
  `EnvironmentFile=%h/.config/gahaku/versitygw.env` (podman `--env-file`), with
  the `Secret=type=env` form documented as the alternative.
- **Rationale**: podman's default secret driver stores secrets in plaintext
  under the same user's storage anyway, so a `0600` file is no weaker and is
  strictly simpler: no out-of-band podman state to create before the first
  start, nothing `podman system reset` can silently take away, and one path
  that resolves to the service user's home rootless and to `/root` under the
  system manager the harness runs.
- **Rejected**: `Secret=` as the default (units depend on state that is not in
  a file the rest of the configuration is backed up with); credentials inline
  in the unit (uncommittable).

### D-16: The units carry a healthcheck but not `Notify=healthy`

- **Choice**: `Environment=VGW_HEALTH=/health` plus `HealthCmd=` using the
  image's busybox wget; `Notify` left at podman's default.
- **Rationale**: `--health` and the wget binary were both read off the v1.7.0
  image, so the check is real. `Notify=healthy` would additionally make
  systemd's readiness depend on podman's healthcheck timer units, which cannot
  be exercised in this dev environment — a timer that fails to arm would turn
  every start into a `TimeoutStartSec` expiry instead of an unhealthy
  container. Readiness is asserted by the harness over HTTP instead.
- **Rejected**: `Notify=healthy` (untestable failure mode, fail-closed); no
  healthcheck at all (loses the one readiness signal versitygw exposes).

### D-17: The harness converges versitygw only; gahaku.service is asserted generated

- **Choice**: `deployment/test/` waits for `versitygw.service` and does the
  presigned round trip; for `gahaku.service` it asserts the generator produced
  the unit and logs whatever state it reached.
- **Rationale**: PLAN.md scopes the harness to exactly that, and the
  alternatives — building the LibreOffice-bearing image inside the throwaway
  container, or streaming a host-built one into the inner podman — cost minutes
  per run for a container the round trip never touches.
- **Rejected**: requiring `gahaku.service` active (turns a deployment-unit test
  into an image-build test); omitting `gahaku.container` from the harness
  entirely (its syntax would then go unchecked).

## Routine calls (noted, not user-decided)

- Content sniffing uses `github.com/gabriel-vasile/mimetype` (broad
  Office/CFB coverage, prefix-only detection).
