#!/usr/bin/env bash
#
# Brings the deployment/quadlet/ units up under systemd inside a container and
# leaves them there, for looking at what the automated harness only asserts on.
#
#   quadlet-sandbox.sh up      start the container and wait for the gateway
#   quadlet-sandbox.sh status  what systemd made of the units
#   quadlet-sandbox.sh logs    the gateway's journal
#   quadlet-sandbox.sh shell   a shell inside, where systemctl and podman are
#   quadlet-sandbox.sh down    remove the container
#
# The automated counterpart is `go test ./deployment/test/`, which is gated on
# GAHAKU_TEST_QUADLET=1 (`make test-quadlet`). This script has no gate: running
# it is already the deliberate act the gate exists to ask for.
#
# Same deviation as that harness: the units run rootful here, under
# /etc/containers/systemd, rather than rootless as deployment/quadlet/README.md
# installs them.

set -euo pipefail

readonly image=quay.io/podman/stable
readonly name=gahaku-quadlet-sandbox
readonly port=7070
readonly access_key=gahakuquadlet
readonly secret_key=gahakuquadletsecret

readonly here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly units="${here}/../quadlet"
readonly credentials="${TMPDIR:-/tmp}/${name}-credentials"

# The header comment above, minus the shebang, is the help text. Read off the
# file rather than repeated in a heredoc, so the two cannot drift apart.
usage() {
	while IFS= read -r line; do
		case "${line}" in
		'#!'*) ;;
		'#') echo ;;
		'# '*) echo "${line#\# }" ;;
		*) break ;;
		esac
	done <"${BASH_SOURCE[0]}"
}

up() {
	# Written before the container starts: systemd reaches default.target and
	# starts versitygw.service on its own, and the unit's EnvironmentFile is
	# deliberately not optional.
	umask 077
	mkdir -p "${credentials}"
	cat >"${credentials}/versitygw.env" <<-EOF
		ROOT_ACCESS_KEY_ID=${access_key}
		ROOT_SECRET_ACCESS_KEY=${secret_key}
	EOF

	podman run \
		--detach \
		--rm \
		--name "${name}" \
		--privileged \
		--systemd=always \
		--publish "127.0.0.1:${port}:${port}" \
		--volume "${units}:/etc/containers/systemd:ro" \
		--volume "${credentials}:/root/.config/gahaku:ro" \
		"${image}" \
		/usr/sbin/init

	echo "waiting for versitygw.service" >&2
	local waited=0
	until [ "$(podman exec "${name}" systemctl is-active versitygw.service 2>/dev/null)" = active ]; do
		if [ "${waited}" -ge 360 ]; then
			echo "versitygw.service did not become active; try '${0} status'" >&2
			return 1
		fi
		sleep 1
		waited=$((waited + 1))
	done

	cat <<-EOF
		the gateway is at http://127.0.0.1:${port}
		  ROOT_ACCESS_KEY_ID=${access_key}
		  ROOT_SECRET_ACCESS_KEY=${secret_key}
		presign against http://versitygw:${port} for urls handed to gahaku
	EOF
}

# Diagnostics, so a non-zero exit from any one of them must not stop the rest.
status() {
	podman exec "${name}" systemctl list-units --all --no-pager \
		'gahaku*' 'versitygw*' || true
	podman exec "${name}" systemctl status --no-pager --full versitygw.service || true
	podman exec "${name}" podman ps --all || true
}

logs() {
	podman exec "${name}" journalctl --no-pager --unit=versitygw.service || true
	podman exec "${name}" podman logs versitygw || true
}

shell() {
	podman exec --interactive --tty "${name}" /bin/bash
}

down() {
	podman rm --force "${name}"
	rm -rf "${credentials}"
}

case "${1:-}" in
up | status | logs | shell | down) "$1" ;;
*)
	usage
	exit 1
	;;
esac
