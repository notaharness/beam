// version reads a release tag (docs/09): v and a canonical semver without
// build metadata, which npm publishes as spelled. It prints the version and
// whether it is a prerelease, as the release job's step outputs.
//
//	node npm/version.mjs v1.2.3-rc.1
import { fileURLToPath } from "node:url";

const id = "(0|[1-9]\\d*|\\d*[A-Za-z-][0-9A-Za-z-]*)";
const semver = new RegExp(`^(0|[1-9]\\d*)\\.(0|[1-9]\\d*)\\.(0|[1-9]\\d*)(-${id}(\\.${id})*)?$`);

// canonical reports whether v is a version npm publishes unchanged.
export const canonical = (v) => semver.test(v);

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const tag = process.argv[2] ?? "";
  const v = tag.slice(1);
  if (!tag.startsWith("v") || !canonical(v)) {
    console.error("usage: node npm/version.mjs v<semver without build metadata>");
    process.exit(2);
  }
  console.log(`version=${v}\nprerelease=${v.includes("-")}`);
}
