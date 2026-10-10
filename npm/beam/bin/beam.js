#!/usr/bin/env node
"use strict";
// beam from npm (docs/09): the platform package's binary with this process's
// arguments and terminal, SIGINT, SIGTERM and SIGWINCH passed on, and its
// exit status as this one's. On Windows nothing is passed on: the console
// sends Ctrl+C and Ctrl+Break to the binary as to this process, which only
// outlives them, and Node's kill would terminate the binary outright. Ending
// this process any other way leaves the binary running there (D75).
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
// The handlers go in before the binary starts: a signal that came while spawn
// ran would otherwise end this process and leave the binary running. Node runs
// them from its event loop, after child is set.
let child;
const windows = process.platform === "win32";
for (const signal of windows ? ["SIGINT", "SIGBREAK"] : ["SIGINT", "SIGTERM", "SIGWINCH"]) {
  process.on(signal, () => windows || child.kill(signal));
}
child = spawn(bin, process.argv.slice(2), { stdio: "inherit" });
child.on("error", (err) => {
  console.error(`beam: ${err.message}`);
  process.exit(1);
});
child.on("exit", (code, signal) => process.exit(signal ? 128 + constants.signals[signal] : code));
