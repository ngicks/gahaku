# STATUS — gahaku initial plan

Current state: **planned, ready to implement** — initial plan plus the
versitygw/quadlet/presigned-URL amendment and the api/cmd/deployment/pkg
restructure; all questions resolved 2026-07-27 (DECISION.md D-01…D-13).
No code written yet.

## Checklist (mirrors PLAN.md steps)

- [x] 1. Module + toolchain (go mod init, buf under `api/`, Makefile targets —
      Makefile not Taskfile: no `task` binary in this environment)
- [x] 2. Proto + codegen (`api/schema/proto/ngicks/gahaku/v1/gahaku.proto`,
      `api/gen/proto/go/ngicks/gahaku/v1`)
- [x] 3. `pkg/input` + `pkg/output` (source resolution, size limits, page sinks)
- [x] 4. `pkg/worker` (subprocess orchestrator, timeout kill, semaphore)
- [x] 5. `pkg/render/imagejob` (worker subcommands deferred to step 9)
- [x] 6. `pkg/render/pdfjob` (go-pdfium wasm)
- [x] 7. `pkg/render/officejob` (soffice → pdf → raster; real-soffice test passes
      with nix-installed LibreOffice 25.8.5.2)
- [x] 8. `pkg/sniff` (detection + routing table)
- [x] 9. `pkg/server` + `cmd/gahaku` (gRPC service, central error mapping, cobra
      scaffold with `serve` plus the hidden `worker` subcommands)
- [x] 10. E2E tests (incl. presigned round trip vs versitygw) + Dockerfile + CI
      (gated `GAHAKU_TEST_E2E=1`; podman cannot *run* containers in this dev
      env, so the versitygw test ran via an endpoint override against the
      binary extracted from the image — the podman path first runs in CI)
- [x] 11. Quadlet units (`deployment/quadlet/`) + podman-in-podman harness
      (`deployment/test/`, `make test-quadlet`, gated `GAHAKU_TEST_QUADLET=1`).
      Units verified with the podman quadlet generator's `-dryrun`; the harness
      itself has never run here, since podman in this dev env cannot start
      containers at all — it first runs in CI.

## Blocked / waiting

- Nothing.

## Next action

All eleven steps are implemented. What is left is environment-bound: the
`GAHAKU_TEST_E2E` podman path and the whole `GAHAKU_TEST_QUADLET` harness need
a host whose podman can run containers.
