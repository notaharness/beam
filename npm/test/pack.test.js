"use strict";
// docs/09 npm: pack.mjs turns make dist's four binaries into the five
// packages, every one at the release's version.
const { test } = require("node:test");
const assert = require("node:assert");
const { spawnSync } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const pack = path.join(__dirname, "..", "pack.mjs");
const license = fs.readFileSync(path.join(__dirname, "..", "..", "LICENSE"), "utf8");

function inDist(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "beam-pack-"));
  t.after(() => fs.rmSync(dir, { recursive: true }));
  fs.mkdirSync(path.join(dir, "dist"));
  for (const target of ["darwin-arm64", "darwin-amd64", "linux-amd64", "linux-arm64"]) {
    fs.writeFileSync(path.join(dir, "dist", `beam-${target}`), target);
  }
  return dir;
}

const read = (dir, file) => JSON.parse(fs.readFileSync(path.join(dir, "dist", "npm", file)));

test("writes the shim and a package per platform at the version", (t) => {
  const dir = inDist(t);
  const r = spawnSync(process.execPath, [pack, "1.2.3"], { cwd: dir, encoding: "utf8" });
  assert.strictEqual(r.status, 0, r.stderr);

  const shim = read(dir, "beam/package.json");
  assert.strictEqual(shim.version, "1.2.3");
  assert.deepStrictEqual(shim.optionalDependencies, {
    "@notaharness/beam-darwin-arm64": "1.2.3",
    "@notaharness/beam-darwin-x64": "1.2.3",
    "@notaharness/beam-linux-x64": "1.2.3",
    "@notaharness/beam-linux-arm64": "1.2.3",
  });
  for (const file of ["index.js", "bin/beam.js"]) {
    assert.ok(fs.existsSync(path.join(dir, "dist", "npm", "beam", file)), file);
  }

  for (const [platform, target] of [
    ["darwin-arm64", "darwin-arm64"],
    ["darwin-x64", "darwin-amd64"],
    ["linux-x64", "linux-amd64"],
    ["linux-arm64", "linux-arm64"],
  ]) {
    const pkg = read(dir, `beam-${platform}/package.json`);
    const [os_, cpu] = platform.split("-");
    assert.deepStrictEqual([pkg.name, pkg.version, pkg.os, pkg.cpu], [`@notaharness/beam-${platform}`, "1.2.3", [os_], [cpu]]);
    const bin = path.join(dir, "dist", "npm", `beam-${platform}`, "beam");
    assert.strictEqual(fs.readFileSync(bin, "utf8"), target);
    assert.strictEqual(fs.statSync(bin).mode & 0o777, 0o755);
  }
});

test("every package is MIT and carries LICENSE", (t) => {
  const dir = inDist(t);
  const r = spawnSync(process.execPath, [pack, "1.2.3"], { cwd: dir, encoding: "utf8" });
  assert.strictEqual(r.status, 0, r.stderr);
  for (const pkg of ["beam", "beam-darwin-arm64", "beam-darwin-x64", "beam-linux-x64", "beam-linux-arm64"]) {
    assert.strictEqual(read(dir, `${pkg}/package.json`).license, "MIT", pkg);
    assert.strictEqual(fs.readFileSync(path.join(dir, "dist", "npm", pkg, "LICENSE"), "utf8"), license, pkg);
  }
});

test("refuses a version that is not canonical semver", (t) => {
  const dir = inDist(t);
  for (const v of ["v1.2.3", "01.2.3", "1.2.3-01", "1.2.3+build.1"]) {
    const r = spawnSync(process.execPath, [pack, v], { cwd: dir, encoding: "utf8" });
    assert.strictEqual(r.status, 2, v);
    assert.match(r.stderr, /usage/, v);
  }
});

test("npm publishes each package whole, at the version pack wrote", (t) => {
  const dir = inDist(t);
  const r = spawnSync(process.execPath, [pack, "1.2.3-rc.1"], { cwd: dir, encoding: "utf8" });
  assert.strictEqual(r.status, 0, r.stderr);
  for (const p of ["beam", "beam-darwin-arm64", "beam-darwin-x64", "beam-linux-x64", "beam-linux-arm64"]) {
    const dry = spawnSync("npm", ["publish", "--dry-run", "--json", "--offline", "--tag", "next"], { cwd: path.join(dir, "dist", "npm", p), encoding: "utf8" });
    const out = JSON.parse(dry.stdout); // keyed by the package's name since npm 11.19
    const got = out[`@notaharness/${p}`] ?? out;
    assert.strictEqual(got.version, "1.2.3-rc.1", p);
    const files = Object.fromEntries(got.files.map((f) => [f.path, f.mode]));
    const bin = p === "beam" ? ["bin/beam.js", "index.js"] : ["beam"];
    assert.deepStrictEqual(Object.keys(files).sort(), ["LICENSE", "README.md", ...bin, "package.json"].sort(), p);
    if (p !== "beam") {
      assert.strictEqual(files.beam & 0o777, 0o755, `${p}: the binary's mode in the tarball`);
    }
  }
});
