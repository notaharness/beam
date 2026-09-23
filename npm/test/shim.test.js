"use strict";
// docs/09 npm: bin/beam.js runs the platform package's binary with its
// arguments and terminal, passes SIGINT, SIGTERM and SIGWINCH on, and exits
// with the binary's code or 128+signal. A shell script stands in for beam.
const { test } = require("node:test");
const assert = require("node:assert");
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const shim = path.join(__dirname, "..", "beam");

// install lays the shim out as npm would, beside a platform package whose
// binary is script, for the rest of test t, and returns the shim's bin.
function install(t, script) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "beam-npm-"));
  t.after(() => fs.rmSync(dir, { recursive: true }));
  fs.cpSync(shim, path.join(dir, "node_modules", "@notaharness", "beam"), { recursive: true });
  const platform = path.join(dir, "node_modules", "@notaharness", `beam-${process.platform}-${process.arch}`);
  fs.mkdirSync(platform, { recursive: true });
  fs.writeFileSync(path.join(platform, "beam"), `#!/bin/sh\n${script}\n`, { mode: 0o755 });
  return path.join(dir, "node_modules", "@notaharness", "beam", "bin", "beam.js");
}

// run starts the shim with args, node given its own options first; done
// resolves to its exit code and output.
function run(bin, args, node = []) {
  const child = spawn(process.execPath, [...node, bin, ...args], { stdio: ["ignore", "pipe", "pipe"] });
  let out = "";
  child.stdout.on("data", (b) => (out += b));
  child.stderr.on("data", (b) => (out += b));
  const done = new Promise((resolve) => child.on("exit", (code) => resolve({ code, out })));
  const printed = (text) =>
    new Promise((resolve) => {
      const check = () => out.includes(text) && resolve();
      child.stdout.on("data", check);
      check();
    });
  return { child, done, printed };
}

test("passes the arguments and exits with the binary's code", async (t) => {
  const { done } = run(install(t, 'printf "%s|" "$@"; exit 3'), ["exec", "box", "--", "a b"]);
  assert.deepStrictEqual(await done, { code: 3, out: "exec|box|--|a b|" });
});

test("exits 128+signal when the binary is killed", async (t) => {
  const { done } = run(install(t, "kill -KILL $$"), []);
  assert.strictEqual((await done).code, 128 + os.constants.signals.SIGKILL);
});

for (const signal of ["SIGINT", "SIGTERM", "SIGWINCH"]) {
  test(`passes ${signal} on`, { timeout: 5000 }, async (t) => {
    const name = signal.slice(3);
    const bin = install(t, `trap 'echo got ${name}; exit 0' ${name}; echo ready; i=0; while [ $i -lt 100 ]; do sleep 0.05; i=$((i+1)); done; exit 9`);
    const { child, done, printed } = run(bin, []);
    t.after(() => child.kill("SIGKILL")); // a binary the signal never reached
    await printed("ready");
    child.kill(signal);
    assert.deepStrictEqual(await done, { code: 0, out: `ready\ngot ${name}\n` });
  });
}

// A signal that comes while the binary starts, before spawn has returned, is
// passed on too: the shim neither dies of it nor leaves the binary running.
test("passes on a signal that comes while spawn runs", { timeout: 5000 }, async (t) => {
  const bin = install(t, "trap 'echo got TERM; exit 0' TERM; kill -TERM $PPID; i=0; while [ $i -lt 100 ]; do sleep 0.05; i=$((i+1)); done; exit 9");
  const { child, done } = run(bin, [], ["--require", path.join(__dirname, "slow-spawn.js")]);
  t.after(() => child.kill("SIGKILL"));
  assert.deepStrictEqual(await done, { code: 0, out: "got TERM\n" });
});

test("says which platform has no binary", () => {
  const { binaryPath } = require(path.join(shim, "index.js"));
  const arch = Object.getOwnPropertyDescriptor(process, "arch");
  Object.defineProperty(process, "arch", { value: "mips" });
  try {
    assert.throws(binaryPath, { message: `beam has no binary for ${process.platform}-mips` });
  } finally {
    Object.defineProperty(process, "arch", arch);
  }
});
