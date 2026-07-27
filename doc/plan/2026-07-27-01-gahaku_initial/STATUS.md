# STATUS — gahaku initial plan

Current state: **planned, ready to implement** — initial plan plus the
versitygw/quadlet/presigned-URL amendment and the api/cmd/deployment/pkg
restructure; all questions resolved 2026-07-27 (DECISION.md D-01…D-13).
No code written yet.

## Checklist (mirrors PLAN.md steps)

- [ ] 1. Module + toolchain (go mod init, buf under `api/`, task targets)
- [ ] 2. Proto + codegen (`api/schema/proto/ngicks/gahaku/v1/gahaku.proto`,
      `api/gen/proto/go/ngicks/gahaku/v1`)
- [ ] 3. `pkg/input` + `pkg/output` (source resolution, size limits, page sinks)
- [ ] 4. `pkg/worker` (subprocess orchestrator, timeout kill, semaphore)
- [ ] 5. `pkg/render/imagejob` (+ `gahaku worker image`)
- [ ] 6. `pkg/render/pdfjob` (go-pdfium wasm, + `gahaku worker pdf`)
- [ ] 7. `pkg/render/officejob` (soffice → pdf → raster, + `gahaku worker office`)
- [ ] 8. `pkg/sniff` (detection + routing table)
- [ ] 9. `pkg/server` + `cmd/gahaku` (serve command, wiring)
- [ ] 10. E2E tests (incl. presigned round trip vs versitygw) + Dockerfile + CI
- [ ] 11. Quadlet units (`deployment/quadlet/`) + podman-in-podman harness
      (`deployment/test/`)

## Blocked / waiting

- Nothing.

## Next action

Step 1 — `go mod init github.com/ngicks/gahaku`, buf setup, task targets.
