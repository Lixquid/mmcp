# controller-tui

A terminal controller for the [Multicast Media Control Protocol (MMCP)](../SPEC.md)
written in Go using [Bubble Tea](https://github.com/charmbracelet/bubbletea) and
[Lip Gloss](https://github.com/charmbracelet/lipgloss).

## Downloading a release

Pre-built controller binaries are published on the **Releases** page of this
GitHub repository (<https://github.com/<owner>/<repo>/releases>). Releases
are created automatically whenever a `controller-tui/v*` tag is pushed (for
example `controller-tui/v1.0.0` becomes release "v1.0.0").

Each release ships one package per platform:

| Asset                                                        | Contents                                                  |
| ------------------------------------------------------------ | --------------------------------------------------------- |
| `mmcp-controller-tui-<version>-linux-x86_64.tar.gz`          | `controller-tui` (Linux x86-64 static binary), README     |
| `mmcp-controller-tui-<version>-windows-x86_64.zip`           | `controller-tui.exe` (Windows x86-64 binary), README      |

The binaries are statically linked and need no runtime dependencies.

### Installing on Linux

1. Download the `linux-x86_64` tarball from the release you want.
2. Extract and run:

   ```sh
   tar -xzf mmcp-controller-tui-<version>-linux-x86_64.tar.gz
   ./controller-tui
   ```

### Installing on Windows

1. Download the `windows-x86_64` zip from the release you want.
2. Unpack it anywhere and run `controller-tui.exe` from a terminal
   (it is a full-screen terminal application).

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
