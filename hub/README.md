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
- **system-tray minimization** — closing the window hides it to the system
  tray (the relay and all providers keep running); the tray menu offers
  "Show hub" to restore the window and "Quit" to exit;
- **a toggleable debug panel** ("Debug messages" checkbox) showing every
  message that passes through the relay, with timestamps and source;
- **asynchronous album-art lookup** ("MusicBrainz artwork" checkbox, on by
  default) — when a provider announces a `TRACK` with an empty art argument,
  the hub queries MusicBrainz for the release group matching the track's
  artist and album, then broadcasts a `TRACK.ART` message (the Asynchronous
  Album Art extension, SPEC.md 5.1) pointing at the Cover Art Archive
  `front-500` cover. Results are cached on disk (keyed by normalized
  artist+album, so repeat tracks never re-query), "not found" results are
  remembered for 7 days, MusicBrainz's one-request-per-second limit is
  respected, and providers that already supply art are left untouched. Lookups
  can be disabled with the toolbar checkbox; the choice persists.
- **a simulated MPRIS provider** ("MPRIS provider" checkbox, off by default;
  not available on Windows) — every media player on the system MPRIS D-Bus
  interface appears as its own MMCP provider (source derived from the player
  name, e.g. `VLC`), announcing `PLAY`/`PAUSE`/`NEXT`/`PREV`/`SEEK` and
  streaming TRACK/POS from the players' live state. `CONTROL` commands are
  performed on the underlying players. Album art is deliberately not
  advertised: MPRIS artwork is a local file URI, not globally accessible
  (the MusicBrainz resolver above fills art in for it anyway).
- **a simulated SMTC provider** ("SMTC provider" checkbox, off by default;
  only shown on Windows) — the Windows counterpart of the MPRIS provider.
  Every System Media Transport Controls session appears as its own MMCP
  provider (source derived from the app id, e.g. `SPOTIFY`), with the same
  capabilities, TRACK/POS streaming, and CONTROL execution — and likewise no
  ART capability, since SMTC artwork is a local thumbnail, not a URL. It is
  implemented with an embedded PowerShell helper that talks to the WinRT
  `Windows.Media.Control` API and is respawned automatically if it dies.

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

### Building in a container

Without the toolchain on the host, use the container build from the
repository root (mounts `./hub` at `/src`, so artifacts appear in
`hub/dist/` on the host):

```sh
docker compose -f compose.build.yml build
docker compose -f compose.build.yml run --rm hub-build
```

The `.git` folder is bind-mounted read-only so the embedded version string
is accurate; without it the version falls back to `dev`.

## Files

| File         | Purpose                                                     |
| ------------ | ----------------------------------------------------------- |
| `main.go`    | App entry point, wiring                                     |
| `relay.go`   | Stateless WebSocket relay, message log, local UI client     |
| `protocol.go`| MMCP message parsing/encoding                               |
| `state.go`   | Derived provider state (TRACK / POS / CAPABILITIES)         |
| `artresolver.go` | MusicBrainz album-art lookup (Asynchronous Album Art)   |
| `mpris.go`   | Simulated provider backed by system MPRIS players (not on Windows) |
| `smtc.go`    | Simulated provider core backed by Windows SMTC sessions (platform neutral core; PowerShell backend in `smtc_windows.go`, stub in `smtc_other.go`) |
| `ui.go`      | Fyne UI: list, detail panel, controls, debug feed, artwork  |
| `tray.go`    | System-tray menu and "minimize to tray" close handling      |
