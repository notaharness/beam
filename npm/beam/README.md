# @notaharness/beam

beam connects the machines one person owns: shells, commands and durable messages
between them, over encrypted peer-to-peer tunnels, with one passkey as the root of
trust.

```sh
npm install -g @notaharness/beam
beam init    # on the first machine
beam join    # on every other one
```

This package is a launcher. The binary comes from whichever of
`@notaharness/beam-{darwin-arm64,darwin-x64,linux-x64,linux-arm64}` matches the machine,
installed as an optional dependency; `require("@notaharness/beam").binaryPath()` is its
path.

Source, specification and issues: <https://github.com/notaharness/beam>. MIT licence.
