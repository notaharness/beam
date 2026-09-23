"use strict";

const { optionalDependencies } = require("./package.json");

// binaryPath is the beam binary for this machine, from its platform package
// (docs/09): one of this package's optional dependencies.
function binaryPath() {
  const platform = `${process.platform}-${process.arch}`;
  const name = `@notaharness/beam-${platform}`;
  if (!(name in optionalDependencies)) {
    throw new Error(`beam has no binary for ${platform}`);
  }
  return require.resolve(`${name}/beam`);
}

module.exports = { binaryPath };
