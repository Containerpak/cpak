#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
runtime="/run/user/$(id -u)"
manifests="$work/manifests"
web="$work/web"
manifest_host=cpak.test
mkdir -p "$runtime" "$manifests" "$web"
chmod 0700 "$runtime"

cleanup() {
	status=$?
	if [ "$status" -ne 0 ]; then
		for log in "$work"/*.log "$runtime"/cpak/*/*.log "$CPAK_INSTALLATION_PATH"/store/containers/*/state/*.log; do
			if [ -f "$log" ] && [ "$(wc -c <"$log")" -lt 1048576 ]; then
				echo "===== $log =====" >&2
				cat "$log" >&2
			fi
		done
	fi
	if [ -n "${cpak:-}" ]; then
		for origin in ${installed_origins:-}; do
			timeout -k 2s 5s "$cpak" stop "$origin" >/dev/null 2>&1 || true
		done
	fi
	if [ -n "${browser_client_pid:-}" ]; then
		kill "$browser_client_pid" 2>/dev/null || true
	fi
	if [ -n "${uri_client_pid:-}" ]; then
		kill "$uri_client_pid" 2>/dev/null || true
	fi
	if [ -n "${dns_pid:-}" ]; then
		sudo kill "$dns_pid" 2>/dev/null || true
	fi
	for pid in ${service_pids:-}; do
		kill "$pid" 2>/dev/null || true
	done
	if [ "${resolver_mount_active:-false}" = true ]; then
		sudo umount /etc/resolv.conf 2>/dev/null || true
	fi
	if [ "$status" -eq 0 ]; then
		rm -rf "$work"
	fi
	return "$status"
}
trap cleanup EXIT INT TERM

export XDG_RUNTIME_DIR="$runtime"
export WAYLAND_DISPLAY=wayland-cpak-integration
export XDG_CURRENT_DESKTOP=GNOME
export CPAK_INSTALLATION_PATH="$work/cpak"
export CPAK_OPTS_FILE="$work/no-config.json"
export CPAK_REGISTRY_AUTH_FILE="$work/registry-auth.json"

openssl req -x509 -newkey rsa:2048 -nodes \
	-keyout "$work/server.key" \
	-out "$work/server.crt" \
	-days 1 \
	-subj /CN=cpak.test \
	-addext "subjectAltName=DNS:cpak.test,DNS:api.github.com,DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1
sudo install -m 0644 "$work/server.crt" /usr/local/share/ca-certificates/cpak-integration.crt
sudo update-ca-certificates >/dev/null
printf '127.0.0.1 cpak.test api.github.com\n' | sudo tee -a /etc/hosts >/dev/null
cat >"$work/bluez.conf" <<'EOF'
<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-BUS Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <policy user="root">
    <allow own="org.bluez"/>
  </policy>
</busconfig>
EOF
sudo install -m 0644 "$work/bluez.conf" /etc/dbus-1/system.d/cpak-integration-bluez.conf
sudo busctl --system call org.freedesktop.DBus /org/freedesktop/DBus org.freedesktop.DBus ReloadConfig

printf 'ready\n' > "$web/ready"
metadata="$work/images.json"
"$root/out/cpak-integration-registry" --probe "$root/out/cpak-integration-probe" --shell /bin/busybox --metadata "$metadata" >"$work/registry.log" 2>&1 &
service_pids="$!"
for attempt in $(seq 1 100); do
	if curl -fsS http://127.0.0.1:5000/v2/ >/dev/null; then
		break
	fi
	if [ "$attempt" -eq 100 ]; then
		echo "registry did not become ready" >&2
		exit 1
	fi
	sleep 0.1
done

python3 - "$manifests" "$manifest_host" "$metadata" <<'PY'
import json
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
manifest_host = sys.argv[2]
digests = json.loads(pathlib.Path(sys.argv[3]).read_text(encoding="utf-8"))
schema = "https://raw.githubusercontent.com/Containerpak/cpak/v2/schema/manifest-v3.json"


def write(name, title, image="probe", override=None, dependencies=None, addons=None, provider=None, desktop_entries=None):
    policy = {
        "filesystem": [],
        "network": False,
        "hostNetwork": False,
        "bluetooth": False,
        "socketWayland": False,
        "asRoot": False,
    }
    policy.update(override or {})
    manifest = {
        "$schema": schema,
        "manifest_version": "3.0",
        "name": title,
        "description": "cpak runtime integration fixture.",
        "version": "1.0.0",
        "image": f"localhost:5000/{image}@{digests[image]}",
        "binaries": ["/usr/local/bin/cpak-integration-probe"],
        "desktop_entries": desktop_entries or [],
        "dependencies": dependencies or [],
        "addons": addons or [],
        "idle_time": 0,
        "override": policy,
    }
    if provider:
        manifest["addon_provider"] = provider
    path = root / "integration" / name / "raw" / "main"
    path.mkdir(parents=True)
    (path / "cpak.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")


write("desktop", "Desktop probe", override={"socketWayland": True}, desktop_entries=["/usr/share/applications/cpak-integration.desktop"])
write(
    "browser",
    "Browser probe",
    image="browser",
    override={"socketWayland": True},
    desktop_entries=["/usr/share/applications/cpak-integration-browser.desktop"],
)
write("uri", "URI probe", override={"socketWayland": True, "openURI": True})
write("bluetooth", "Bluetooth probe", override={"network": True, "bluetooth": True})
write("loopback", "Loopback probe", override={"network": True, "hostNetwork": True})
write("network", "Network probe", override={"network": True})
write("network-disabled", "Network-disabled probe")
write("private", "Private package probe", image="private")
write(
    "guest-environment",
    "Guest environment probe",
    image="locale-app",
    override={"env": ["LANG=C.UTF-8", "LC_ALL=C.UTF-8"]},
)
write("nested-sandbox", "Nested sandbox probe", override={"userNamespaces": True})
write("nested-sandbox-disabled", "Blocked nested sandbox probe")
write("environment", "Environment probe", override={"asRoot": True})
write("dependency", "Dependency probe", image="dependency")
write(
    "dependency-main",
    "Dependency consumer",
    dependencies=[{"origin": f"{manifest_host}/integration/dependency", "mode": "layer"}],
)
write(
    "addon",
    "Addon probe",
    image="addon",
    provider={
        "id": "integration",
        "slot": "integration.fixture",
        "mode": "exclusive",
        "exports": {"environment": ["CPAK_INTEGRATION_ADDON=present"]},
    },
)
write("addon-main", "Addon consumer", addons=[f"{manifest_host}/integration/addon"])
PY

cat >"$CPAK_REGISTRY_AUTH_FILE" <<'EOF'
{"records":[{"origin":"github.com/integration/private","source_host":"github.com","registry":"localhost:5000","repository":"private","username":"github-user","password":"source-secret"}]}
EOF
chmod 0600 "$CPAK_REGISTRY_AUTH_FILE"

sudo python3 "$root/https_server.py" --cert "$work/server.crt" --key "$work/server.key" --directory "$manifests" --port 443 --github-token source-secret --github-manifest "$manifests/integration/private/raw/main/cpak.json" >"$work/manifests.log" 2>&1 &
service_pids="$service_pids $!"
python3 -m http.server 18080 --bind 0.0.0.0 --directory "$web" >"$work/http.log" 2>&1 &
service_pids="$service_pids $!"
weston --backend=headless-backend.so --socket="$WAYLAND_DISPLAY" --idle-time=0 >"$work/weston.log" 2>&1 &
service_pids="$service_pids $!"
stale_wayland_display=wayland-cpak-stale
weston --backend=headless-backend.so --socket="$stale_wayland_display" --idle-time=0 >"$work/weston-stale.log" 2>&1 &
service_pids="$service_pids $!"
sudo "$root/out/cpak-integration-probe" bluez-mock >"$work/bluez.log" 2>&1 &
service_pids="$service_pids $!"

for endpoint in "https://$manifest_host/integration/desktop/raw/main/cpak.json" http://127.0.0.1:18080/ready; do
	for attempt in $(seq 1 100); do
		if curl -fsS "$endpoint" >/dev/null; then
			break
		fi
		if [ "$attempt" -eq 100 ]; then
			echo "service did not become ready: $endpoint" >&2
			exit 1
		fi
		sleep 0.1
	done
done
if ! curl -fsS -H 'Authorization: Bearer source-secret' https://api.github.com/repos/integration/private >/dev/null; then
	echo "private GitHub fixture did not become ready" >&2
	exit 1
fi
for attempt in $(seq 1 100); do
	if [ -S "$runtime/$WAYLAND_DISPLAY" ] && [ -S "$runtime/$stale_wayland_display" ] && busctl --system call org.freedesktop.DBus /org/freedesktop/DBus org.freedesktop.DBus NameHasOwner s org.bluez | grep -q 'true'; then
		break
	fi
	if [ "$attempt" -eq 100 ]; then
		echo "desktop or Bluetooth fixture did not become ready" >&2
		exit 1
	fi
	sleep 0.1
done

cpak="$root/out/cpak"
"$root/out/cpak-integration-probe" user-manager-mock "$WAYLAND_DISPLAY" >"$work/user-manager.log" 2>&1 &
service_pids="$service_pids $!"
for attempt in $(seq 1 100); do
	if dbus-send --session --dest=org.freedesktop.DBus --type=method_call --print-reply \
		/org/freedesktop/DBus org.freedesktop.DBus.NameHasOwner \
		string:org.freedesktop.systemd1 | grep -q true; then
		break
	fi
	if [ "$attempt" -eq 100 ]; then
		echo "user manager fixture did not become ready" >&2
		exit 1
	fi
	sleep 0.05
done
WAYLAND_DISPLAY="$stale_wayland_display" "$cpak" service >"$work/cpak-service.log" 2>&1 &
service_pids="$service_pids $!"
mkdir -p "$work/init"
(
	cd "$work/init"
	"$cpak" init --name integration --package-version 1.0.0 --description integration --image example.invalid/integration@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa --binary /usr/local/bin/cpak-integration-probe
)
grep -F '"version": "1.0.0"' "$work/init/cpak.json" >/dev/null
install() {
	"$cpak" install --yes "$1"
	installed_origins="${installed_origins:-} $1"
}
run_command() {
	origin=$1
	shift
	"$cpak" run "$origin" -- /usr/local/bin/cpak-integration-probe "$@"
}
run_probe() {
	origin=$1
	shift
	run_command "$origin" "$@"
	"$cpak" stop "$origin"
}

wait_browser_url() {
	origin=$1
	url=$2
	for attempt in $(seq 1 100); do
		browser_state=$(run_command "$origin" browser-read 2>/dev/null || true)
		if printf '%s\n' "$browser_state" | grep -F "$url" >/dev/null; then
			return 0
		fi
		sleep 0.05
	done
	echo "browser did not receive $url" >&2
	return 1
}

slirp_pid() {
	pgrep -n -x slirp4netns 2>/dev/null || true
}

network_supervisor_pid() {
	ps -eo pid=,args= | awk '$0 ~ /cpak [n]etwork-helper/ {print $1}' | tail -n 1
}

wait_no_slirp() {
	for attempt in $(seq 1 100); do
		if [ -z "$(slirp_pid)" ]; then
			return 0
		fi
		sleep 0.05
	done
	echo "slirp4netns did not stop" >&2
	return 1
}

private_origin=github.com/integration/private
install "$private_origin"
run_probe "$private_origin" private

desktop_origin="$manifest_host/integration/desktop"
install "$desktop_origin"
run_command "$desktop_origin" desktop
for attempt in $(seq 1 32); do
	run_command "$desktop_origin" seccomp >/dev/null
done
"$cpak" stop "$desktop_origin"
echo "thread-pinned seccomp probe passed"

guest_origin="$manifest_host/integration/guest-environment"
LANG=en_US.UTF-8 LC_ALL= LC_NUMERIC=ru_RU.UTF-8 XDG_DATA_DIRS=/nix/store/desktop/share:/run/current-system/sw/share install "$guest_origin"
LANG=en_US.UTF-8 LC_ALL= LC_NUMERIC=ru_RU.UTF-8 XDG_DATA_DIRS=/nix/store/desktop/share:/run/current-system/sw/share run_probe "$guest_origin" guest-environment

nested_host_log="$work/nested-host.log"
if "$root/out/cpak-integration-probe" nested-mount >"$nested_host_log" 2>&1; then
	nested_host_mounts=true
else
	nested_host_mounts=false
	echo "host policy does not permit nested mounts; runtime coverage is enforced by the NixOS VM" >&2
	cat "$nested_host_log" >&2
fi
install "$manifest_host/integration/nested-sandbox"
if [ "$nested_host_mounts" = true ]; then
	run_probe "$manifest_host/integration/nested-sandbox" nested-mount
else
	"$cpak" stop "$manifest_host/integration/nested-sandbox"
fi
install "$manifest_host/integration/nested-sandbox-disabled"
run_probe "$manifest_host/integration/nested-sandbox-disabled" blocked-mount

browser_origin="$manifest_host/integration/browser"
uri_origin="$manifest_host/integration/uri"
install "$browser_origin"
install "$uri_origin"
desktop_file="$HOME/.local/share/applications/cpak-integration-browser.desktop"
if [ ! -f "$desktop_file" ]; then
	echo "browser desktop entry was not exported" >&2
	exit 1
fi
update-desktop-database "$HOME/.local/share/applications"
xdg-mime default cpak-integration-browser.desktop x-scheme-handler/http
xdg-mime default cpak-integration-browser.desktop x-scheme-handler/https
if [ "$(xdg-mime query default x-scheme-handler/https)" != "cpak-integration-browser.desktop" ]; then
	echo "browser desktop entry is not the HTTPS handler" >&2
	exit 1
fi
run_command "$browser_origin" browser-server >"$work/browser-container.log" 2>&1 &
browser_client_pid=$!
for attempt in $(seq 1 600); do
	browser_marker=$(sed -n 's/^server=//p' "$work/browser-container.log" | head -n 1)
	if [ -n "$browser_marker" ]; then
		break
	fi
	if ! kill -0 "$browser_client_pid" 2>/dev/null; then
		echo "browser fixture exited before readiness" >&2
		exit 1
	fi
	if [ "$attempt" -eq 600 ]; then
		echo "browser fixture did not become ready" >&2
		exit 1
	fi
	sleep 0.05
done
policy_marker="$work/uri-policy-marker"
touch "$policy_marker"
WAYLAND_DISPLAY="$stale_wayland_display" run_command "$uri_origin" desktop
uri_policy_count=$(find "$runtime/cpak/policies" -type f -newer "$policy_marker" | wc -l)
if [ "$uri_policy_count" -ne 1 ]; then
	echo "URI container created $uri_policy_count broker policies, want one" >&2
	exit 1
fi
uri_policy=$(find "$runtime/cpak/policies" -type f -newer "$policy_marker")
if grep -F '"desktop_environment"' "$uri_policy" >/dev/null; then
	echo "new broker policy persisted its container display" >&2
	exit 1
fi
sed 's/^{/{"desktop_environment":["WAYLAND_DISPLAY=wayland-cpak-stale"],/' "$uri_policy" >"$uri_policy.tmp"
chmod 0600 "$uri_policy.tmp"
mv "$uri_policy.tmp" "$uri_policy"
legacy_url="https://example.com/cpak-legacy-display"
legacy_client_log="$work/legacy-uri.log"
WAYLAND_DISPLAY="$stale_wayland_display" timeout -k 5s 15s "$cpak" run "$uri_origin" @/usr/local/bin/xdg-open -- "$legacy_url" >"$legacy_client_log" 2>&1 &
uri_client_pid=$!
for attempt in $(seq 1 400); do
	browser_process_state=$(awk '{print $3}' "/proc/$browser_client_pid/stat" 2>/dev/null || true)
	if [ -z "$browser_process_state" ] || [ "$browser_process_state" = Z ]; then
		cat "$legacy_client_log" >&2
		echo "legacy URI policy replaced the running browser container" >&2
		exit 1
	fi
	uri_process_state=$(awk '{print $3}' "/proc/$uri_client_pid/stat" 2>/dev/null || true)
	if [ -z "$uri_process_state" ] || [ "$uri_process_state" = Z ]; then
		if ! wait "$uri_client_pid"; then
			cat "$legacy_client_log" >&2
			echo "legacy URI handoff did not return" >&2
			exit 1
		fi
		uri_client_pid=
		break
	fi
	if [ "$attempt" -eq 400 ]; then
		cat "$legacy_client_log" >&2
		echo "legacy URI handoff did not return" >&2
		exit 1
	fi
	sleep 0.05
done
for attempt in $(seq 1 40); do
	browser_process_state=$(awk '{print $3}' "/proc/$browser_client_pid/stat" 2>/dev/null || true)
	if [ -z "$browser_process_state" ] || [ "$browser_process_state" = Z ]; then
		cat "$legacy_client_log" >&2
		echo "legacy URI policy replaced the running browser container" >&2
		exit 1
	fi
	sleep 0.05
done
wait_browser_url "$browser_origin" "$legacy_url"
legacy_browser_state=$(run_command "$browser_origin" browser-read)
if [ "$(printf '%s\n' "$legacy_browser_state" | sed -n 's/^server=//p' | head -n 1)" != "$browser_marker" ]; then
	echo "legacy URI policy replaced the running browser container" >&2
	exit 1
fi
xdg_url="https://example.com/cpak-xdg-open"
gio_url="https://example.com/cpak-gio-open"
warm_url="https://example.com/cpak-xdg-open-warm"
if ! timeout -k 5s 15s "$cpak" run "$uri_origin" @/usr/local/bin/xdg-open -- "$xdg_url"; then
	echo "xdg-open URI handoff did not return" >&2
	exit 1
fi
wait_browser_url "$browser_origin" "$xdg_url"
if ! timeout -k 5s 15s "$cpak" run "$uri_origin" @/usr/local/bin/gio -- open "$gio_url"; then
	echo "gio URI handoff did not return" >&2
	exit 1
fi
wait_browser_url "$browser_origin" "$gio_url"
if ! timeout -k 5s 15s "$cpak" run "$uri_origin" @/usr/local/bin/xdg-open -- "$warm_url"; then
	echo "warm URI handoff did not return" >&2
	exit 1
fi
wait_browser_url "$browser_origin" "$warm_url"
browser_state=$(run_command "$browser_origin" browser-read)
if [ "$(printf '%s\n' "$browser_state" | sed -n 's/^server=//p' | head -n 1)" != "$browser_marker" ]; then
	echo "URI handoff replaced the running browser container" >&2
	exit 1
fi
echo "URI handoff probe passed"
"$cpak" stop "$uri_origin"
"$cpak" stop "$browser_origin"
wait "$browser_client_pid" 2>/dev/null || true
browser_client_pid=

install "$manifest_host/integration/bluetooth"
run_probe "$manifest_host/integration/bluetooth" bluetooth
wait_no_slirp

install "$manifest_host/integration/loopback"
run_probe "$manifest_host/integration/loopback" loopback

host_interface=$(ip -4 route show default | awk 'NR == 1 {print $5}')
if [ -z "$host_interface" ]; then
	echo "host network interface is unavailable" >&2
	exit 1
fi
network_fixture_ip=192.0.2.1
sudo ip address add "$network_fixture_ip/32" dev "$host_interface"
network_origin="$manifest_host/integration/network"
network_url="http://$network_fixture_ip:18080/ready"
install "$network_origin"
printf 'nameserver 192.0.2.254\n' >"$work/resolver-old"
printf 'nameserver 127.0.0.1\n' >"$work/resolver-new"
sudo dnsmasq --keep-in-foreground --no-resolv --no-hosts --bind-interfaces \
	--listen-address=127.0.0.1 --address="/cpak-switch.test/$network_fixture_ip" >"$work/dns.log" 2>&1 &
dns_pid=$!
service_pids="$service_pids $dns_pid"
for attempt in $(seq 1 100); do
	if sudo ss -lunp | grep -F '127.0.0.1:53' >/dev/null; then
		break
	fi
	if [ "$attempt" -eq 100 ]; then
		echo "DNS fixture did not become ready" >&2
		exit 1
	fi
	sleep 0.05
done
sudo mount --bind "$work/resolver-old" /etc/resolv.conf
resolver_mount_active=true
run_command "$network_origin" network-slow "$network_url" >"$work/network-concurrent-1.log" 2>&1 &
network_client_1=$!
run_command "$network_origin" network-slow "$network_url" >"$work/network-concurrent-2.log" 2>&1 &
network_client_2=$!
if ! wait "$network_client_1"; then
	cat "$work/network-concurrent-1.log" >&2
	echo "first concurrent invocation failed" >&2
	exit 1
fi
if ! wait "$network_client_2"; then
	cat "$work/network-concurrent-2.log" >&2
	echo "second concurrent invocation failed" >&2
	exit 1
fi
run_command "$network_origin" network "$network_url"
network_helper=$(slirp_pid)
if [ -z "$network_helper" ] || ! kill -0 "$network_helper" 2>/dev/null; then
	echo "isolated network helper is not alive" >&2
	exit 1
fi
network_supervisor=$(network_supervisor_pid)
if [ -z "$network_supervisor" ] || ! kill -0 "$network_supervisor" 2>/dev/null; then
	echo "network supervisor is not alive" >&2
	exit 1
fi
if run_command "$network_origin" network "http://cpak-switch.test:18080/ready" >/dev/null 2>&1; then
	echo "stale resolver unexpectedly reached the DNS fixture" >&2
	exit 1
fi
sudo umount /etc/resolv.conf
resolver_mount_active=false
sudo mount --bind "$work/resolver-new" /etc/resolv.conf
resolver_mount_active=true
for attempt in $(seq 1 200); do
	refreshed_helper=$(slirp_pid)
	if [ -n "$refreshed_helper" ] && [ "$refreshed_helper" != "$network_helper" ]; then
		break
	fi
	if [ "$attempt" -eq 200 ]; then
		echo "resolver change did not refresh slirp4netns" >&2
		exit 1
	fi
	sleep 0.05
done
if [ "$(network_supervisor_pid)" != "$network_supervisor" ]; then
	echo "resolver refresh replaced the network supervisor" >&2
	exit 1
fi
run_command "$network_origin" network "http://cpak-switch.test:18080/ready"
if run_command "$network_origin" network "http://10.0.2.2:18080/ready" >/dev/null 2>&1; then
	echo "ordinary network access exposed the host gateway" >&2
	exit 1
fi
sudo umount /etc/resolv.conf
resolver_mount_active=false
network_helper=$refreshed_helper
for iteration in 1 2 3; do
	run_command "$network_origin" network "$network_url"
	if [ "$(slirp_pid)" != "$network_helper" ]; then
		echo "warm network invocation replaced its helper" >&2
		exit 1
	fi
done
kill "$network_helper"
for attempt in $(seq 1 100); do
	if ! kill -0 "$network_helper" 2>/dev/null; then
		break
	fi
	sleep 0.05
done
run_command "$network_origin" network "$network_url"
replacement_helper=$(slirp_pid)
if [ -z "$replacement_helper" ] || [ "$replacement_helper" = "$network_helper" ] || ! kill -0 "$replacement_helper" 2>/dev/null; then
	echo "dead network helper did not rebuild the container" >&2
	exit 1
fi
"$cpak" stop "$network_origin" --package-version main
wait_no_slirp

offline_origin="$manifest_host/integration/network-disabled"
install "$offline_origin"
run_command "$offline_origin" network-disabled "$network_url"
if [ -n "$(slirp_pid)" ]; then
	echo "network-disabled package started slirp4netns" >&2
	exit 1
fi
"$cpak" stop "$offline_origin"

install "$manifest_host/integration/environment"
run_probe "$manifest_host/integration/environment" root-identity
"$cpak" environment create --name system-identities --origin "$manifest_host/integration/environment" --package-version main
"$cpak" environment shell --environment system-identities --command /usr/local/bin/cpak-integration-probe -- persistence-write
"$cpak" environment stop --environment system-identities
"$cpak" environment shell --environment system-identities --command /usr/local/bin/cpak-integration-probe -- persistence-read
"$cpak" environment shell --environment system-identities --command /usr/local/bin/cpak-integration-probe -- system-identities
"$cpak" environment stop --environment system-identities
"$cpak" environment delete --environment system-identities

install "$manifest_host/integration/dependency-main"
run_probe "$manifest_host/integration/dependency-main" dependency

install "$manifest_host/integration/addon"
install "$manifest_host/integration/addon-main"
"$cpak" addon list "$manifest_host/integration/addon-main" --json | grep -F "$manifest_host/integration/addon" >/dev/null
run_probe "$manifest_host/integration/addon-main" addon

echo "cpak integration suite passed"
