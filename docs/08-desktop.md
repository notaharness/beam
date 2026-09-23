# 08 · n10 integration

n10 desktop and n10's TUI are clients of the control socket, like the CLI. They link
nothing from beam and ship the beam binary as a dependency. No transport, membership or
identity code lives in n10.

## Process model

The desktop leaves `BEAM_CONFIG_DIR` and `BEAM_SOCKET` unset, so that it and the CLI find
the same daemon, or sets them to absolute paths ([02](02-identity.md)) for the daemon it
spawns and every process it starts.

The desktop owns the lifetime of a daemon it started, and of no other:

- On start it connects to the socket. A daemon that answers was started by someone else
  (the CLI, a service, another app); the desktop uses it and leaves it running when it
  quits.
- Otherwise it spawns `beam daemon --exit-with-parent` as its own child, with stdin a
  pipe it holds for as long as it runs ([07](07-cli.md)), stdout and stderr appended to
  the app's log, and connects once the socket answers. Node's `spawn` pipes are close-on-exec, so no other child of the app
  inherits the pipe. On quit it closes its end and waits for the child to exit; if the
  app crashes or is killed, the kernel closes it, with the same effect.
- A spawn that loses the lock race to another daemon exits 1; the desktop connects to
  the winner and treats it as someone else's.

On an unexpected socket loss the desktop shows "beam restarting…" in the machines panel
and starts over as on start, retrying with backoff (500 ms → 30 s): connect, else spawn
its own.

The TUI uses the shared connect-or-spawn helper (a TypeScript port of the rules in
[06](06-control-socket.md)); a daemon it starts is detached and outlives it.

## Machines panel

Rows come from `peers` and `peer` / `peer.new` events, one per `PeerView` plus this
machine. Shown: label or alias, fingerprint, state (`connected` / `offline` /
`revoked`), `inbound` and `path` as a small badge (`direct`, `relay fra`, `unknown`),
queue counts. Clicking a queue count opens `msg.queue`.

First run (`status.enrolled == false`): one card, **Create a fleet** or **Join my
fleet**, with a fleet name field for create. Each opens the ceremony URL with
`shell.openExternal` and draws it as a QR code beside the stages, so a phone can answer
instead; it shows the stage names from [07](07-cli.md), handles the second
`ceremony` event for init, and ends on the `*.wait` result. `prf-unsupported` shows
"this passkey provider doesn't support what beam needs; try another".

Row menu: alias, grant (`all` / `msg` / `none`), revoke. Revoke opens the browser and
then shows `revoked here · published | pending · acknowledged by n`. There is no forget.

## Remote sessions

A remote terminal is `pty.open` + attach; the pane reads frames and writes resize
controls. `MachineExecutor` is `exec.open` + attach. The bridge exposes both as Node
streams. Connection loss shows in the pane as text, not a spinner.

## Mail relay

`msg.subscribe` on a dedicated connection. Each `mail` event is resolved to a target
pane; a successful injection sends `msg.ack`; a missing or refused target sends
`msg.defer { reason }`, which shows in that peer's refused count and is retried on the
next subscribe. An app restart re-receives anything unacked.

## `libs/core`

Core does not know about beam; it takes a machine resolver the desktop installs.

## Orchestra scripts

`beam msg send "$BEAM_CALLER_ID" …` and `beam msg listen`, finding the daemon through the
injected `BEAM_SOCKET`.
