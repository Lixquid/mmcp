# MMCP Hub

A graphical hub for the [Multicast Media Control Protocol (MMCP)](../SPEC.md),
built with Go and the [Fyne](https://fyne.io) toolkit. It combines:

- **a stateless multicast relay** — every text message received from a
  connected client is rebroadcast to every other connected client
  (`ws://localhost:9994` by default, `-port` to change);
- **a controller** — discovers providers via broadcast `* INFO`, lists them,
  and lets you focus one to inspect its live state (title, artist, album,
  source, playback state, capabilities, artwork, interpolated position);
- **media controls** — play/pause, next, previous, and a seek bar at the
  bottom, each disabled when the focused provider does not advertise the
  corresponding capability (`NEXT`, `PREV`, `SEEK`; play/pause needs none);
- **a toggleable debug panel** ("Debug messages" checkbox) showing every
  message that passes through the relay, with timestamps and source.

## Downloading a release

Pre-built hub packages are published on the **Releases** page of this
GitHub repository (<https://github.com/<owner>/<repo>/releases>). Releases
are created automatically whenever a `hub/v*` tag is pushed (for example
`hub/v1.0.0` becomes release "v1.0.0").

Each release ships one package per platform:

| Asset                                           | Contents                                            |
| ----------------------------------------------- | --------------------------------------------------- |
| `mmcp-hub-<version>-linux-x86_64.tar.gz`        | `hub` (Linux x86-64 GUI binary), README             |
| `mmcp-hub-<version>-windows-x86_64.zip`         | `hub.exe` (Windows x86-64 GUI binary), README       |

### Installing on Linux

1. Download the `linux-x86_64` tarball from the release you want.
2. Extract and run:

   ```sh
   tar -xzf mmcp-hub-<version>-linux-x86_64.tar.gz
   ./hub
   ```

   The hub is a graphical application: it needs a desktop session with a
   display, plus the usual X11/OpenGL runtime libraries that desktop
   distributions ship (on a minimal server install you may need to install
   `libgl1`, `libx11-6`, and friends through your package manager).
3. The hub starts its MMCP relay on port 9994. To use another port:

   ```sh
   ./hub -port 9995
   ```

### Installing on Windows

1. Download the `windows-x86_64` zip from the release you want.
2. Unpack it anywhere and double-click `hub.exe` (it is a GUI application
   and does not open a console window).
3. The hub starts its MMCP relay on port 9994 by default. To use another
   port, start it from a command prompt with `hub.exe -port 9995`.

## Running from source

```sh
go build .
./hub
```

## Building distributions

```sh
./build.sh
```

Produces in `dist/`:

- `MMCP-Hub-windows-amd64.exe` — Windows x86-64 executable (GUI subsystem,
  embedded icon; requires mingw-w64 on the build host)
- `MMCP-Hub-x86_64.AppImage` — Linux AppImage (requires `appimagetool`;
  downloaded automatically to `build/` on first use)

## Files

| File         | Purpose                                                     |
| ------------ | ----------------------------------------------------------- |
| `main.go`    | App entry point, wiring                                     |
| `relay.go`   | Stateless WebSocket relay, message log, local UI client     |
| `protocol.go`| MMCP message parsing/encoding                               |
| `state.go`   | Derived provider state (TRACK / POS / CAPABILITIES)         |
| `ui.go`      | Fyne UI: list, detail panel, controls, debug feed, artwork  |
