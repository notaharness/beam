// pack writes the five npm packages of docs/09 at a version into dist/npm,
// from the binaries `make dist` leaves in dist/: the shim, and a package per
// platform the shim depends on, holding that platform's binary. Each carries
// the repository's LICENSE.
//
//	node npm/pack.mjs 1.2.3
import { chmodSync, copyFileSync, cpSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";

const version = process.argv[2] ?? "";
if (!/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(version)) {
  console.error("usage: node npm/pack.mjs <semver without v>");
  process.exit(2);
}
const json = (url) => JSON.parse(readFileSync(new URL(url, import.meta.url)));
const write = (file, value) => writeFileSync(file, JSON.stringify(value, null, 2) + "\n");
const goArch = { x64: "amd64", arm64: "arm64" };
const license = new URL("../LICENSE", import.meta.url);

const shim = json("beam/package.json");
const template = json("platform/package.json");
shim.version = version;
for (const name of Object.keys(shim.optionalDependencies)) {
  const [os, cpu] = name.slice("@notaharness/beam-".length).split("-");
  const dir = `dist/npm/beam-${os}-${cpu}`;
  mkdirSync(dir, { recursive: true });
  write(`${dir}/package.json`, { ...template, name, version, os: [os], cpu: [cpu] });
  copyFileSync(`dist/beam-${os}-${goArch[cpu]}`, `${dir}/beam`);
  chmodSync(`${dir}/beam`, 0o755);
  copyFileSync(license, `${dir}/LICENSE`);
  shim.optionalDependencies[name] = version;
}
cpSync(new URL("beam", import.meta.url), "dist/npm/beam", { recursive: true });
write("dist/npm/beam/package.json", shim);
copyFileSync(license, "dist/npm/beam/LICENSE");
