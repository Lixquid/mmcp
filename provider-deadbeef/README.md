# MMCP provider for DeaDBeeF

A [DeaDBeeF](https://deadbeef.sourceforge.net/) plugin that exposes the
player as a [Multicast Media Control Protocol (MMCP)](../SPEC.md) provider.
It connects to the stateless multicast WebSocket relay, announces playback
state and track metadata, streams position telemetry, and reacts to
controller commands.

## Capabilities

Advertised: `PLAY`, `PAUSE`, `NEXT`, `PREV`, `SEEK`.

Album art (`ART`) is **deliberately not advertised**: MMCP expects the
album-art field to be a globally accessible URL, which a local player
cannot guarantee, so the art value in `TRACK` is always empty.

## Downloading a release

Pre-built plugin packages are published on the **Releases** page of this
GitHub repository (<https://github.com/<owner>/<repo>/releases>). Releases
are created automatically whenever a `provider-deadbeef/v*` tag is pushed (for
example `provider-deadbeef/v1.0.0` becomes release "v1.0.0").

Each release ships one package per platform:

| Asset                                             | Contents                                        |
| ------------------------------------------------- | ----------------------------------------------- |
| `mmcp-deadbeef-<version>-linux-x86_64.zip`        | `mmcp.so` (Linux x86-64), README, LICENSE       |
| `mmcp-deadbeef-<version>-windows-x86_64.zip`      | `mmcp.dll` (Windows x86-64), README, LICENSE    |

### Installing on Linux

1. Download the `linux-x86_64` zip from the release you want.
2. Unpack `mmcp.so` into your user plugin directory:

   ```sh
   unzip mmcp-deadbeef-<version>-linux-x86_64.zip mmcp.so -d ~/.local/lib/deadbeef/
   ```

   (Deadbeef also scans `~/.local/lib64/deadbeef/` and, for a system-wide
   install, `/usr/lib/deadbeef/` or `/usr/lib64/deadbeef/`.)
3. Restart deadbeef. The provider starts automatically and connects to the
   relay; the relay URL can be changed in deadbeef's plugin preferences
   ("MMCP Provider" → MMCP relay URL).

### Installing on Windows

1. Download the `windows-x86_64` zip from the release you want.
2. Unpack `mmcp.dll` into your deadbeef plugins directory (typically
   `C:\Program Files\DeaDBeeF\plugins` or
   `%APPDATA%\deadbeef\plugins`).
3. Restart deadbeef.

## Building

The plugin has no dependencies beyond libc and the DeaDBeeF headers.

```sh
make                                     # headers via pkg-config (deadbeef installed)
make DDB_INCLUDE=-I/path/to/deadbeef/include   # explicit headers
```

The header-only source tree works too, e.g.:

```sh
git clone --depth 1 https://github.com/DeaDBeeF-Player/deadbeef
make DDB_INCLUDE=-Ideadbeef/include
```

Install:

```sh
make install PREFIX=~/.local   # -> ~/.local/lib/deadbeef/mmcp.so
```

For distribution through the
[deadbeef-plugin-builder](https://github.com/DeaDBeeF-Player/deadbeef-plugin-builder),
a `manifest.json` is included.

## Testing

An end-to-end test suite lives in `tests/`. It compiles the plugin against a
mock `DB_functions_t` (canned playback state, event recording) and drives a
full MMCP conversation over a real WebSocket relay: initial announce,
broadcast/targeted INFO, SEEK (with event + position verification), provider
arbitration, PLAYPAUSE, and malformed-input tolerance.

```sh
make test DDB_HEADERS=/path/to/deadbeef/include   # or rely on pkg-config
```

Prints `RESULT PASS` on success.

## Releases

Pushing a tag `provider-deadbeef/v*` (e.g. `provider-deadbeef/v1.0.0`) runs the GitHub Actions
workflow in `.github/workflows/release-provider-deadbeef.yml`, which builds the plugin
for Linux x86-64 (`mmcp.so`) and Windows x86-64 (`mmcp.dll`) and attaches the
packaged zips to a GitHub Release.

## Configuration

The relay endpoint is configurable in DeaDBeeF's plugin preferences
(`mmcp.relay`, default `ws://localhost:9994`). Changing it reconnects the
provider to the new relay.

## Behavior

- Generates a random 8-character instance ID at startup.
- Announces `CAPABILITIES`, `TRACK`, and `POS` on every (re)connect.
- Emits `TRACK` with state `P`/`S` whenever playback state changes and
  whenever the track changes (driven by player events plus a 100 ms poll).
- Sends `POS` roughly once per second while playing.
- Answers broadcast `* INFO` and targeted `INFO` with `TRACK`, `POS`,
  `CAPABILITIES` (in that order).
- Handles `PLAYPAUSE`, `PLAY`, `PAUSE`, `NEXT`, `PREV`, `SEEK`
  (SEEK is ignored while stopped).
- Implements provider arbitration: pauses DeaDBeeF when another provider
  announces `TRACK` with state `P`, then emits its own `TRACK` with `S`.
- Silently ignores malformed, unknown, and out-of-order messages; invalid
  input never causes a disconnect.
- Reconnects to the relay automatically with exponential backoff.
