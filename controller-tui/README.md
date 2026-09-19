# tui-controller

A terminal controller for the [Multicast Media Control Protocol (MMCP)](../SPEC.md)
written in Go using [Bubble Tea](https://github.com/charmbracelet/bubbletea) and
[Lip Gloss](https://github.com/charmbracelet/lipgloss).

## Running

```sh
go build -o mmcp-controller .
./mmcp-controller                      # uses ws://localhost:9994
./mmcp-controller ws://host:9994      # or pass an endpoint
MMCP_RELAY=ws://host:9994 ./mmcp-controller
```

## Keys

| Key            | Action                                          |
| -------------- | ----------------------------------------------- |
| `SPACE`        | PLAYPAUSE                                       |
| `p` / `s`      | PLAY / PAUSE                                    |
| `n` / `b`      | NEXT / PREV                                     |
| `←` / `→`      | SEEK −5s / +5s                                  |
| `C-←` / `C-→`  | SEEK −30s / +30s                                |
| `g`            | Go to timestamp (seconds, `MM:SS`, `HH:MM:SS`)  |
| `TAB` / arrows | Select which provider commands are addressed to |
| `ESC`          | Follow the active (playing) provider again      |
| `i`            | Broadcast `* INFO` (re-discover all providers)  |
| `r`            | Targeted `INFO` to the current target           |
| `q` / `C-c`    | Quit                                            |

## Behavior

- On connect the controller sends `1/CONTROL<RS>*<RS>INFO` and displays every
  provider that responds.
- Playback state and track metadata come exclusively from `TRACK`; position
  comes from `POS` (telemetry only, interpolated between samples while the
  provider reports `P`, shown as *stale* after 10s).
- Commands are addressed to the selected provider, or to the provider that
  most recently announced `TRACK` state `P`.
- Malformed, unknown, duplicate, stale, and out-of-order messages are silently
  ignored and never cause a disconnect.
- Multiple simultaneous players are tolerated; the latest `P` announcement
  wins.
