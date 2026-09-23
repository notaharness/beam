"use strict";
// docs/09 Versioning: a release tag is v and a canonical semver without build
// metadata, the one spelling the binary, the GitHub release and npm all
// carry; a prerelease is one on GitHub and under npm's next.
const { test } = require("node:test");
const assert = require("node:assert");
const { spawnSync } = require("node:child_process");
const path = require("node:path");

const version = path.join(__dirname, "..", "version.mjs");
const run = (tag) => spawnSync(process.execPath, [version, tag], { encoding: "utf8" });

test("reads a release and whether it is a prerelease", () => {
  for (const [tag, out] of [
    ["v1.2.3", "version=1.2.3\nprerelease=false\n"],
    ["v0.1.0-rc.1", "version=0.1.0-rc.1\nprerelease=true\n"],
    ["v1.0.0-alpha-1.0.x-y", "version=1.0.0-alpha-1.0.x-y\nprerelease=true\n"],
  ]) {
    const r = run(tag);
    assert.deepStrictEqual([r.status, r.stdout], [0, out], tag);
  }
});

test("refuses a tag npm would publish under another spelling, or not at all", () => {
  for (const tag of ["1.2.3", "v1.2", "v01.2.3", "v1.02.3", "v1.2.3-01", "v1.2.3-rc..1", "v1.2.3-", "v1.2.3+build.1", "v1.2.3-rc.1+b", "vv1.2.3"]) {
    const r = run(tag);
    assert.strictEqual(r.status, 2, tag);
    assert.match(r.stderr, /usage/, tag);
  }
});
