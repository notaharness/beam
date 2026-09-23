# 08 · n10 integration

n10 desktop and n10's TUI are clients of the control socket, like the CLI. They link
nothing from beam and ship the beam binary as a dependency. No transport, membership or
identity code lives in n10.

## Process model

Both shells use the shared connect-or-spawn helper (a small TypeScript port of the same
rules in [06](06-control-socket.md)). The daemon outlives the app. "Quit and stop beam"
sends `daemon.shutdown`; plain quit does not. An unexpected socket loss reconnects with
backoff and shows "beam restarting…" in the machines panel.

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
