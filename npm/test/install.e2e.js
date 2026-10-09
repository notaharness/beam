"use strict";
// docs/09 npm, end to end: the packages pack.mjs makes from make dist's
// binaries install from their tarballs, and beam runs through npm's own bin
// entry on this machine, with its version, its exit status and its binary
// where binaryPath says; the test kit's binary prints its line. Run after
// make dist with a canonical VERSION:
//
//	make dist VERSION=0.0.0-ci && node --test npm/test/install.e2e.js
const { test } = require("node:test");
const assert = require("node:assert");
const { spawn, spawnSync } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const root = path.join(__dirname, "..", "..");
const windows = process.platform === "win32";
const target = `${{ win32: "windows" }[process.platform] ?? process.platform}-${{ x64: "amd64" }[process.arch] ?? process.arch}${windows ? ".exe" : ""}`;
const platform = `beam-${process.platform}-${process.arch}`;

// run is a command's result; npm is npm.cmd on Windows, which only a shell
// runs.
function run(cmd, args, opts = {}) {
  const r = spawnSync(cmd, args, { encoding: "utf8", shell: windows && cmd === "npm", ...opts });
  assert.ifError(r.error);
  return r;
}

test("the packed packages install and run beam on this machine", (t) => {
  const version = run(path.join(root, "dist", `beam-${target}`), ["version"]).stdout.trim();
  assert.strictEqual(run(process.execPath, [path.join(root, "npm", "pack.mjs"), version], { cwd: root }).status, 0);
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "beam-install-"));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const tarballs = ["beam", platform].map((p) => {
    const r = run("npm", ["pack", "--json", "--pack-destination", dir, path.join(root, "dist", "npm", p)]);
    assert.strictEqual(r.status, 0, r.stderr);
    return path.join(dir, JSON.parse(r.stdout)[0].filename);
  });
  fs.writeFileSync(path.join(dir, "package.json"), "{}");
  const install = run("npm", ["install", "--offline", "--no-audit", "--no-fund", ...tarballs], { cwd: dir });
  assert.strictEqual(install.status, 0, install.stderr);

  const beam = (...args) => run("npm", ["exec", "--offline", "--", "beam", ...args], { cwd: dir });
  const v = beam("version");
  assert.strictEqual(v.stdout.trim(), version, v.stderr);
  assert.strictEqual(beam("no-such-command").status, 2, "the binary's usage exit");

  const bin = run(process.execPath, ["-p", 'require("@notaharness/beam").binaryPath()'], { cwd: dir }).stdout.trim();
  assert.strictEqual(path.basename(bin), windows ? "beam.exe" : "beam");
  assert.ok(bin.includes(path.join("node_modules", "@notaharness", platform)), bin);
});

test("the test kit's binary prints its line and exits with its parent", async () => {
  const kit = spawn(path.join(root, "dist", `beamtest-${target}`), ["testkit", "--exit-with-parent"], { stdio: ["pipe", "pipe", "inherit"] });
  const line = await new Promise((resolve, reject) => {
    let out = "";
    kit.stdout.on("data", (b) => {
      out += b;
      if (out.includes("\n")) resolve(out.split("\n")[0]);
    });
    kit.on("error", reject);
  });
  const { derpMap, directory } = JSON.parse(line);
  assert.ok(derpMap && directory, line);
  kit.stdin.end();
  const code = await new Promise((resolve) => kit.on("exit", resolve));
  assert.strictEqual(code, 0);
});
