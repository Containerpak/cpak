import { readFile } from "node:fs/promises";
import vm from "node:vm";

const [modulePath, runtimePath, expectedVersion] = process.argv.slice(2);
if (!modulePath || !runtimePath || !expectedVersion) {
  throw new Error(
    "usage: smoke.mjs <cpak-core.wasm> <wasm_exec.js> <expected-version>",
  );
}

vm.runInThisContext(await readFile(runtimePath, "utf8"), {
  filename: runtimePath,
});

const go = new globalThis.Go();
const { instance } = await WebAssembly.instantiate(
  await readFile(modulePath),
  go.importObject,
);
void go.run(instance);

for (let attempt = 0; attempt < 200 && !globalThis.cpak; attempt += 1) {
  await new Promise((resolve) => setTimeout(resolve, 5));
}
if (!globalThis.cpak) {
  throw new Error("the module published no API");
}
if (globalThis.cpak.version !== expectedVersion) {
  throw new Error(`unexpected module version: ${globalThis.cpak.version}`);
}

function ask(call, request = {}) {
  const answer = JSON.parse(globalThis.cpak[call](JSON.stringify(request)));
  if (!answer.ok) {
    throw new Error(`${call}: ${answer.error}`);
  }
  return answer.result;
}

const catalog = ask("permissionCatalog").permissions;
for (const key of [
  "bluetooth",
  "displayX11",
  "hostNetwork",
  "sessionBus",
  "userNamespaces",
]) {
  const permission = catalog.find((entry) => entry.key === key);
  if (!permission?.manifestV3) {
    throw new Error(`current permission missing from catalog: ${key}`);
  }
}

const manifest = {
  manifest_version: "3.0",
  name: "Learn",
  description: "The cpak Learn decision core smoke test",
  version: expectedVersion,
  image:
    "ghcr.io/containerpak/learn@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  binaries: ["/usr/bin/learn"],
  idle_time: 5,
  override: {
    network: true,
    hostActions: [
      { provider: "cpak", capabilities: ["read", "manage", "exec"] },
    ],
  },
};
const valid = ask("validateManifest", { manifest });
if (!valid.valid || valid.manifestVersion !== "3.0") {
  throw new Error(`current manifest was refused: ${JSON.stringify(valid)}`);
}

manifest.override.hostNetwork = true;
manifest.override.network = false;
const invalid = ask("validateManifest", { manifest });
if (invalid.valid || !invalid.error.includes("hostNetwork requires network")) {
  throw new Error(`invalid host network policy was accepted: ${JSON.stringify(invalid)}`);
}

process.stdout.write("CPAK_LEARN_CORE=PASS\n");
