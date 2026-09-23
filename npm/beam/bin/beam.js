#!/usr/bin/env node
"use strict";
// beam from npm (docs/09): the platform package's binary with this process's
// arguments and terminal, SIGINT, SIGTERM and SIGWINCH passed on, and its
// exit status as this one's.
const { spawn } = require("node:child_process");
const { constants } = require("node:os");
const { binaryPath } = require("..");

let bin;
try {
  bin = binaryPath();
} catch (err) {
  console.error(err.message);
  process.exit(1);
}
const child = spawn(bin, process.argv.slice(2), { stdio: "inherit" });
for (const signal of ["SIGINT", "SIGTERM", "SIGWINCH"]) {
  process.on(signal, () => child.kill(signal));
}
child.on("error", (err) => {
  console.error(`beam: ${err.message}`);
  process.exit(1);
});
child.on("exit", (code, signal) => process.exit(signal ? 128 + constants.signals[signal] : code));
