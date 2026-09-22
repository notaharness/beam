# 08 · n10 integration

n10 desktop and n10's TUI are clients of the control socket, exactly like the CLI.
They link nothing from beam and ship the beam binary as a dependency. No transport,
pairing or identity code lives in n10.

## Process model

The desktop's main process spawns `beam daemon --detach` from the platform package in
`node_modules` (see [09-build-and-distribution](09-build-and-distribution.md)) if the
socket is absent, then connects to `$BEAM_DIR/run/beam.sock`. A restart policy (backoff
500 ms → 30 s, five attempts, reset after 30 s healthy) supervises the socket connection
and respawns the daemon if it dies. If the daemon was already running when the app
started, the app never stops it. Quitting the app leaves the daemon up unless
the user chooses "quit and stop beam", which sends `daemon.shutdown`.


## Machines panel

The machines list renders one row per peer plus the local machine:

```ts
interface MachineView {
  peerId: string;
  label: string;
  isLocal: boolean;
  state: 'connected' | 'offline' | 'revoked';
  path: 'direct' | `relay:${string}` | null;   // set while connected
  lastSeenAt: number | null;
  queueDepth: number;
  pinnedAt: number | null;
  revokedAt: number | null;
  scopes: StreamKind[] | null;
  inboundWaiting: InboundMailItem[];
  inboundRefused: InboundMailItem[];
}
```


First run with no `fleet.json`: one card with two buttons, **Create a fleet** (`init`)
and **Join my fleet** (`join`). Either opens the ceremony URL in the system browser via
`shell.openExternal`, shows "waiting for your passkey…", and resolves on the `*.wait`
result or shows the error token's text. A machine that joins appears in every other
desktop's list as it connects, via the `peer.new` event; nothing has to be done on the
other desktops.

The row menu has rename, grant, forget and revoke. Revoke opens the browser for one tap
(`revoke.start` / `revoke.wait`) and shows the same waiting state.

## Remote sessions

A remote terminal is `pty.open` plus an attach connection: the desktop reads frames from
it into the terminal and writes resize control frames back. `MachineExecutor` is
`exec.open` plus an attach connection. The bridge exposes both as Node streams so the
rest of the app sees ordinary readable and writable streams.

## Mail relay

`msg.subscribe` on a dedicated control connection. Each `mail` event is resolved to a
target pane and injected; a successful injection sends `msg.ack`; a refusal or a missing
target does not, so the envelope stays in the daemon's inbound store and shows in the
panel as waiting or refused. Anything unacked is re-sent by the daemon on subscribe, so
an app restart loses nothing.

## `libs/core`

Core does not know about beam. It takes a machine resolver the desktop installs, and
`machine-registry.ts`, `RemoteSessionPoller` and `MachineExecutor` are written against
that seam.

## TUI

Where the TUI reaches a remote machine it does so through the same bridge module, moved
to `libs/app-core` if both shells need it. No Ink code touches the socket protocol
directly.

## Orchestra scripts

Scripts use the CLI: `beam msg send "$BEAM_CALLER_ID" …` to report home and
`beam msg listen` to receive. They find the daemon through `BEAM_SOCKET` in the injected
environment.

