# Podman-in-podman harness for the quadlet units

`deployment/quadlet/` describes a systemd deployment, and a development host
generally has neither systemd nor a machine to spare for one. This directory
runs the units inside a privileged container that has systemd as PID 1 and a
podman of its own (D-E).

```sh
make test-quadlet          # the automated harness
./quadlet-sandbox.sh up    # the same stack, left running to poke at
```

| File | What it is |
| --- | --- |
| `quadlet_test.go` | the assertions: units generate, `versitygw.service` converges, an object round-trips through presigned urls |
| `podman_test.go` | the container plumbing: pull, boot, `systemctl` polling, diagnostics |
| `quadlet-sandbox.sh` | the manual counterpart — brings the same stack up and leaves it, with `status` / `logs` / `shell` / `down` |

## The gate

`go test ./deployment/test/` skips unless `GAHAKU_TEST_QUADLET=1`, and skips
again — rather than failing — when podman is absent, cannot reach a registry,
or cannot run a privileged container. `make test-quadlet` is the switch. Same
shape as `e2e/`'s `GAHAKU_TEST_E2E`, for the same reason: an ordinary
`go test ./...` should not need a container runtime.

The sandbox script has no gate; running it is already the deliberate act.

## What it does

1. Pulls `quay.io/podman/stable` and starts it `--privileged --systemd=always`
   with `/usr/sbin/init` as PID 1.
2. Bind-mounts `deployment/quadlet/` read-only at `/etc/containers/systemd/`,
   and a generated credentials file at `/root/.config/gahaku/versitygw.env` —
   before the container starts, since systemd reaches `default.target` and
   starts `versitygw.service` well before an exec could land, and the unit's
   `EnvironmentFile=` is deliberately not optional.
3. Waits for the boot to finish, runs `systemctl daemon-reload` (the operator's
   own step, and the reload path rather than the boot path), and asserts the
   generator produced all four services — including the two supporting ones,
   which have no `[Install]` and are only reached through the `Requires=` the
   generator derives from `Network=`/`Volume=`.
4. Waits for `versitygw.service` to be active, then for the gateway to answer
   over the doubly-published port.
5. Creates a bucket, presigns a GET and a PUT with `aws-sdk-go-v2` (D-11), and
   round-trips an object through them with plain `net/http` — the same two
   calls `pkg/input` and `pkg/output` make at render time.

## What it deliberately does not do

- **Rootless.** The units install rootless (D-12); they run rootful here. In a
  throwaway privileged container there is no service user worth creating, and
  `%h` then resolves to `/root` rather than to that user's home. The generator
  path (`/etc/containers/systemd`) is the only unit-visible difference.
- **Start `gahaku.service`.** Its image is built from the repository's
  Dockerfile and published nowhere, so making it start would mean building
  LibreOffice into an image inside the throwaway container, or streaming a
  host-built one in — minutes per run for a container the presigned round trip
  never touches. PLAN.md scopes the harness to `versitygw.service` convergence
  plus that round trip. The harness asserts `gahaku.service` was generated and
  logs whatever state it reached; an absent image is expected, not a failure.
- **Render anything.** `e2e/` covers rendering, including a presigned round
  trip against a `podman run` versitygw. What is under test here is the units.

## When it fails

Every failure prints the generated unit list, `systemctl status` and the
journal for `versitygw.service`, and the inner `podman ps` / `podman logs`.
`./quadlet-sandbox.sh up` then leaves the same stack standing for a closer
look.

Two failures worth naming before chasing the wrong layer:

- **Extended attributes.** versitygw's posix backend wants a filesystem that
  supports them — it keeps bucket and object metadata in `user.*` xattrs. Here
  the volume sits on the storage of a container that is itself on the host's
  overlay, which is one layer further down than anything tested so far. Lost
  xattrs surface as metadata errors from ordinary S3 calls, not as anything
  mentioning xattrs. `versitygw posix --sidecar` and `--nometa` are the escape
  hatches, but they belong in the harness rather than in the shipped unit,
  which has to describe a real deployment.
- **The doubly-published port.** The gateway is published out of the inner
  container into this one, and out of this one onto a loopback port of the
  host. The inner hop needs the container's own podman to bind `0.0.0.0:7070`
  in its network namespace; a gateway that answers `podman exec ... wget` but
  not the host has failed at that hop, not at the unit.
