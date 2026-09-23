"use strict";
// Preloaded into the shim by shim.test.js: spawn returns 300 ms after the
// binary started, as it can on a loaded machine, so a signal the binary sends
// at once reaches the shim while it is still inside spawn.
const childProcess = require("node:child_process");
const { spawn } = childProcess;
childProcess.spawn = (...args) => {
  const child = spawn(...args);
  for (const until = Date.now() + 300; Date.now() < until; );
  return child;
};
