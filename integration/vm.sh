#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
work=$(mktemp -d)
image_url=https://cloud-images.ubuntu.com/releases/24.04/release-20260826/ubuntu-24.04-server-cloudimg-amd64.img
image_sha256=d0fe84bb5f80853425fa6be28e2c106f30104c3cfe8611933f2e65c9b63f0e30
cache_root=${XDG_CACHE_HOME:-$HOME/.cache}/cpak-integration
base="$cache_root/ubuntu-24.04-20260826.img"
disk="$work/disk.qcow2"
seed="$work/seed.img"
key="$work/id_ed25519"
serial="$work/serial.log"

cleanup() {
	if [ -n "${qemu_pid:-}" ]; then
		kill "$qemu_pid" 2>/dev/null || true
	fi
	rm -rf "$work"
}
trap cleanup EXIT INT TERM

mkdir -p "$root/integration/out"
CGO_ENABLED=0 go -C "$root" build -trimpath -o integration/out/cpak .
CGO_ENABLED=0 go -C "$root" build -trimpath -o integration/out/cpak-storaged ./cmd/cpak-storaged
CGO_ENABLED=0 go -C "$root" build -trimpath -o integration/out/cpak-integration-probe ./integration/probe
CGO_ENABLED=0 go -C "$root" build -trimpath -o integration/out/cpak-integration-registry ./integration/registry

mkdir -p "$cache_root"
if ! printf '%s  %s\n' "$image_sha256" "$base" | sha256sum --check --status >/dev/null 2>&1; then
	curl -fL --retry 4 --retry-delay 2 "$image_url" -o "$base.partial"
	mv "$base.partial" "$base"
fi
printf '%s  %s\n' "$image_sha256" "$base" | sha256sum --check --status
qemu-img create -q -f qcow2 -F qcow2 -b "$base" "$disk" 16G
ssh-keygen -q -t ed25519 -N '' -f "$key"
public_key=$(cat "$key.pub")

cat >"$work/user-data" <<EOF
#cloud-config
ssh_authorized_keys:
  - $public_key
package_update: true
packages:
  - busybox-static
  - ca-certificates
  - curl
  - dbus
  - desktop-file-utils
  - dnsmasq-base
  - fuse3
  - fuse-overlayfs
  - libglib2.0-bin
  - openssl
  - slirp4netns
  - uidmap
  - weston
  - xdg-utils
runcmd:
  - [sh, -c, 'sysctl -w kernel.unprivileged_userns_clone=1 || true']
  - [sh, -c, 'sysctl -w kernel.apparmor_restrict_unprivileged_userns=0 || true']
  - [sh, -c, 'modprobe fuse || true']
EOF
printf 'instance-id: cpak-integration\nlocal-hostname: cpak-integration\n' >"$work/meta-data"
cloud-localds "$seed" "$work/user-data" "$work/meta-data"

if [ -r /dev/kvm ] && [ -w /dev/kvm ]; then
	accel=kvm
	cpu=host
else
	accel=tcg
	cpu=max
fi

qemu-system-x86_64 \
	-machine "accel=$accel" \
	-cpu "$cpu" \
	-smp 2 \
	-m 4096 \
	-display none \
	-serial "file:$serial" \
	-drive "if=virtio,file=$disk,format=qcow2" \
	-drive "if=virtio,file=$seed,format=raw" \
	-netdev user,id=net0,hostfwd=tcp::2222-:22 \
	-device virtio-net-pci,netdev=net0 &
qemu_pid=$!

ssh_options="-i $key -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=3 -p 2222"
scp_options="-i $key -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=3 -P 2222"
for attempt in $(seq 1 180); do
	if ssh $ssh_options ubuntu@127.0.0.1 true >/dev/null 2>&1; then
		break
	fi
	if [ "$attempt" -eq 180 ]; then
		cat "$serial" >&2
		exit 1
	fi
	sleep 1
done
ssh $ssh_options ubuntu@127.0.0.1 'cloud-init status --wait'

tar -C "$root/integration" -cf "$work/integration.tar" run.sh https_server.py out
scp $scp_options "$work/integration.tar" ubuntu@127.0.0.1:/home/ubuntu/integration.tar
ssh $ssh_options ubuntu@127.0.0.1 'mkdir integration && tar -C integration -xf integration.tar && chmod 0755 integration/run.sh integration/out/* && dbus-run-session -- integration/run.sh'
