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
	for pid in ${service_pids:-}; do
		kill "$pid" 2>/dev/null || true
	done
	if [ "$status" -eq 0 ]; then
		rm -rf "$work"
	fi
	return "$status"
}
trap cleanup EXIT INT TERM

export XDG_RUNTIME_DIR="$runtime"
export WAYLAND_DISPLAY=wayland-cpak-integration
export CPAK_INSTALLATION_PATH="$work/cpak"
export CPAK_OPTS_FILE="$work/no-config.json"

openssl req -x509 -newkey rsa:2048 -nodes \
	-keyout "$work/server.key" \
	-out "$work/server.crt" \
	-days 1 \
	-subj /CN=cpak.test \
	-addext "subjectAltName=DNS:cpak.test,DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1
sudo install -m 0644 "$work/server.crt" /usr/local/share/ca-certificates/cpak-integration.crt
sudo update-ca-certificates >/dev/null
printf '127.0.0.1 cpak.test\n' | sudo tee -a /etc/hosts >/dev/null
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
"$root/out/cpak-integration-registry" --probe "$root/out/cpak-integration-probe" --metadata "$metadata" >"$work/registry.log" 2>&1 &
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


def write(name, title, image="probe", override=None, dependencies=None, addons=None, provider=None):
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
        "desktop_entries": ["/usr/share/applications/cpak-integration.desktop"] if name == "desktop" else [],
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


write("desktop", "Desktop probe", override={"socketWayland": True})
write("bluetooth", "Bluetooth probe", override={"network": True, "bluetooth": True})
write("loopback", "Loopback probe", override={"network": True, "hostNetwork": True})
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

sudo python3 "$root/https_server.py" --cert "$work/server.crt" --key "$work/server.key" --directory "$manifests" --port 443 >"$work/manifests.log" 2>&1 &
service_pids="$service_pids $!"
python3 -m http.server 18080 --bind 127.0.0.1 --directory "$web" >"$work/http.log" 2>&1 &
service_pids="$service_pids $!"
weston --backend=headless-backend.so --socket="$WAYLAND_DISPLAY" --idle-time=0 >"$work/weston.log" 2>&1 &
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
for attempt in $(seq 1 100); do
	if [ -S "$runtime/$WAYLAND_DISPLAY" ] && busctl --system call org.freedesktop.DBus /org/freedesktop/DBus org.freedesktop.DBus NameHasOwner s org.bluez | grep -q 'true'; then
		break
	fi
	if [ "$attempt" -eq 100 ]; then
		echo "desktop or Bluetooth fixture did not become ready" >&2
		exit 1
	fi
	sleep 0.1
done

cpak="$root/out/cpak"
install() {
	"$cpak" install --yes "$1"
}
run_probe() {
	origin=$1
	probe=$2
	"$cpak" run "$origin" -- /usr/local/bin/cpak-integration-probe "$probe"
	"$cpak" stop "$origin"
}

install "$manifest_host/integration/desktop"
run_probe "$manifest_host/integration/desktop" desktop

install "$manifest_host/integration/bluetooth"
run_probe "$manifest_host/integration/bluetooth" bluetooth

install "$manifest_host/integration/loopback"
run_probe "$manifest_host/integration/loopback" loopback

install "$manifest_host/integration/dependency-main"
run_probe "$manifest_host/integration/dependency-main" dependency

install "$manifest_host/integration/addon"
install "$manifest_host/integration/addon-main"
"$cpak" addon list "$manifest_host/integration/addon-main" --json | grep -F "$manifest_host/integration/addon" >/dev/null
run_probe "$manifest_host/integration/addon-main" addon

echo "cpak integration suite passed"
