# Quadlet units for the gahaku stack

Four rootless podman quadlet units that bring up gahaku together with the
versitygw S3 gateway callers presign against:

| Unit | Generated service | What it is |
| --- | --- | --- |
| `gahaku.network` | `gahaku-network.service` | the podman network `gahaku`, the only thing the two containers share |
| `versitygw.volume` | `versitygw-volume.service` | the podman volume `versitygw-data`, the posix backend's top-level directory |
| `versitygw.container` | `versitygw.service` | versitygw `v1.7.0`, S3 on `:7070` |
| `gahaku.container` | `gahaku.service` | the render service, gRPC on `:9000` |

The container units pull in the network and volume units through generated
`Requires=`, so starting `versitygw.service` is enough to create both.

## Install

Rootless, under the service user's own systemd (D-12) — nothing here wants a
privileged port or root:

```sh
mkdir -p ~/.config/containers/systemd
cp deployment/quadlet/*.container \
   deployment/quadlet/*.network \
   deployment/quadlet/*.volume \
   ~/.config/containers/systemd/
```

Build the gahaku image, which is not published anywhere — the unit refers to it
as `localhost/gahaku:latest`:

```sh
podman build --tag gahaku:latest .
```

Write the gateway's root credentials where `versitygw.container` expects them.
`%h/.config/gahaku/versitygw.env` is `~/.config/gahaku/versitygw.env` under a
user manager:

```sh
mkdir -p ~/.config/gahaku
umask 077
cat > ~/.config/gahaku/versitygw.env <<EOF
ROOT_ACCESS_KEY_ID=$(openssl rand -hex 16)
ROOT_SECRET_ACCESS_KEY=$(openssl rand -hex 32)
EOF
```

Then generate and start:

```sh
systemctl --user daemon-reload
systemctl --user start versitygw.service gahaku.service
systemctl --user status versitygw.service gahaku.service
```

`daemon-reload` is what runs the quadlet generator; `systemctl --user cat
versitygw.service` shows what it produced. The `[Install] WantedBy=default.target`
of both container units means a `daemon-reload` alone already schedules them
for the next login — `loginctl enable-linger $USER` keeps them running when
nobody is logged in.

To stop and remove:

```sh
systemctl --user stop gahaku.service versitygw.service
rm ~/.config/containers/systemd/{gahaku,versitygw}.* # then daemon-reload
```

### Credentials in podman's secret store instead

The env file is the simplest correct rootless form: one file, `0600`, no state
outside the config directory, and podman's own default secret driver stores
secrets in plaintext under `~/.local/share/containers/storage` anyway. If the
deployment already manages podman secrets, replace the `EnvironmentFile=` line
of `versitygw.container` with

```ini
Secret=versitygw-root-access-key,type=env,target=ROOT_ACCESS_KEY_ID
Secret=versitygw-root-secret-key,type=env,target=ROOT_SECRET_ACCESS_KEY
```

and create them before the first start:

```sh
printf %s "$ACCESS_KEY" | podman secret create versitygw-root-access-key -
printf %s "$SECRET_KEY" | podman secret create versitygw-root-secret-key -
```

The trade is that the units then depend on podman state that no longer lives in
a file you can back up with the rest of the configuration, and `podman system
reset` silently takes it away.

## Presigning against this stack

gahaku holds no storage credentials (D-09). The caller signs the urls and hands
them over, which means **the caller must sign for the host gahaku will
resolve**, not the one the caller itself uses. In this stack those differ:

- The caller's own traffic — creating buckets, uploading the source document,
  downloading rendered pages — goes to the published port, e.g.
  `http://your-host:7070`.
- The urls handed to gahaku are fetched from inside the podman network, where
  the gateway answers as `versitygw`. Sign those against
  `http://versitygw:7070`.

With `aws-sdk-go-v2` that is two `s3.Client`s over the same credentials
differing only in `BaseEndpoint`. Both need `UsePathStyle: true` (the gateway
serves path-style addressing) and `Region: "us-east-1"` (versitygw's default —
a presign signed for another region comes back as a 403 that reads like an
expired url).

Presign with a generous expiry: the url has to outlive the queue wait plus the
render, not just the request.

## What this stack loosens, and what to do about it

`gahaku.container` sets `GAHAKU_INPUT_ALLOW_HTTP=true` and
`GAHAKU_INPUT_BLOCK_PRIVATE_ADDRESSES=false`. Both are guards the binary turns
on by default, and both have to come off for a gateway that speaks plain http
on a podman network — RFC1918 addresses are exactly what the second one
refuses.

The cost is that gahaku will fetch and upload to any url a caller names,
including ones pointing back into the network it sits in. That is acceptable
only while the callers are trusted. If they are not:

- put TLS in front of versitygw, publish it under a name that resolves the same
  inside and outside, and drop `GAHAKU_INPUT_ALLOW_HTTP` back to its default;
- keep `GAHAKU_INPUT_BLOCK_PRIVATE_ADDRESSES` on and give gahaku a route to the
  gateway that is not a private address;
- or terminate the untrusted input upstream and let only your own service
  choose the urls.

`gahaku.container` publishes gRPC on `:9000` with no authentication of its own
(out of scope for this deployment — see PLAN.md non-goals); restrict the
published port or front it with something that authenticates.

## Verifying a change to these units

`deployment/test/` runs them under systemd inside a container, since a
development host generally has neither systemd nor a spare machine:

```sh
make test-quadlet
```

See `deployment/test/README.md` for what it does and does not cover.
