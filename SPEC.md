# Multicast Media Control Protocol (MMCP)

## 1. Overview

MMCP coordinates media playback between browser-based **providers** and
**controllers** over a stateless multicast WebSocket relay.

- **Providers** control a media player and announce their current playback
  state, track metadata, and supported capabilities.
- **Controllers** display provider state and send playback commands.
- The relay rebroadcasts each message to all other connected clients.
- The protocol assumes a trusted local environment; authentication is out of
  scope.
- Normally, at most one provider is actively playing.

The default relay endpoint is `ws://localhost:9994`, but deployments may use
another endpoint.

Providers advertise **capabilities** describing the operations and features they
support. Controllers can use these capabilities when deciding which controls to
offer, but capabilities are advisory rather than authorization information.

The core protocol consists of the `TRACK`, `POS`, `CAPABILITIES`, and `CONTROL`
messages. Extensions may define additional message types for optional or
non-core behaviour.

## 2. Transport

### 2.1 WebSocket

MMCP uses a WebSocket connection to a multicast relay.

The relay is stateless. Its required behaviour is to:

1.  accept WebSocket connections;
2.  receive client messages;
3.  rebroadcast each message to all other connected clients.

The relay does not maintain media state or require knowledge of message
semantics.

Message ordering between different senders is not guaranteed.

### 2.2 Message Format

Messages are UTF-8 WebSocket text with fields separated by ASCII Record
Separator (`RS`, `U+001E`):

    MESSAGE_TYPE<RS>ARGUMENT<RS>ARGUMENT...

There is no trailing `RS`.

Message types and other identifiers are case-sensitive.

Arguments may be empty but must not contain `RS`. Providers must replace `RS`
occurring in source text with a single space. No other escaping is defined.

### 2.3 Versioning

The version prefix is part of the message type:

    1/TRACK
    1/POS
    1/CAPABILITIES
    1/CONTROL

The prefix allows incompatible future versions such as `2/TRACK`.

### 2.4 Invalid and Unknown Messages

Clients must silently ignore unknown or malformed messages, including invalid
argument counts, invalid numeric values, unknown commands, and invalid
capabilities.

Invalid input must not by itself cause disconnection.

Clients must tolerate duplicate, delayed, stale, and out-of-order messages.

## 3. Clients

### 3.1 Providers

A Provider controls a media player and announces its state and capabilities.

A Provider:

- generates an instance ID;
- announces its capabilities with `CAPABILITIES`;
- announces its playback state and current track with `TRACK`;
- reports playback position with `POS`;
- handles `CONTROL` messages addressed to it;
- responds to `INFO` with `TRACK`, `POS`, and `CAPABILITIES`;
- responds to broadcast `* INFO` with `TRACK`, `POS`, and `CAPABILITIES`;
- stops or pauses when another Provider announces active playback with `TRACK`
  state `P`.

See Core Messages for the message definitions.

### 3.2 Controllers

A Controller observes Providers and may request playback operations.

A Controller:

- uses `TRACK` to determine provider playback state and track metadata;
- uses `POS` for playback position and length;
- uses `CAPABILITIES` to determine supported operations and features;
- sends `CONTROL` messages to request operations;
- may use broadcast `* INFO` for discovery;
- must tolerate multiple Providers responding to discovery;
- must tolerate missing, duplicated, stale, malformed, unknown, and out-of-order
  messages.

See Core Messages for the message definitions.

## 4. Core Messages

### 4.1 TRACK

`TRACK` announces a Provider's current playback state and track:

    1/TRACK<RS>instance-id<RS>state<RS>source-type<RS>track<RS>artist<RS>album<RS>art

The arguments are:

1.  Provider instance ID
2.  Playback state
3.  Source type
4.  Track name
5.  Artist name
6.  Album name
7.  Album-art value

Example:

    1/TRACK<RS>a7Kx92Qm<RS>P<RS>YTM<RS>Never Gonna Give You Up<RS>Rick Astley<RS>Whenever You Need Somebody<RS>https://example.invalid/art.jpg

#### Playback state

The playback state is a single ASCII letter:

- `P` — actively playing
- `S` — not currently playing, including paused, stopped, or suspended

Unknown states must be ignored.

`TRACK` is the **authoritative protocol message for playback state**.

A Provider may send `TRACK` whether it is playing or not.

A Provider must emit a `TRACK` message whenever its playback state changes. In
particular:

- when playback starts or resumes, it must emit `TRACK` with state `P`;
- when playback pauses or stops, it must emit `TRACK` with state `S`;
- when it changes tracks while playing, it must emit `TRACK` with state `P`
  describing the new track;
- when it changes tracks while not playing, it should emit `TRACK` with state
  `S` describing the new track.

A Provider may emit additional `TRACK` messages at any time to reannounce its
current state.

Temporary interruptions such as buffering do not constitute a playback-state
change unless the Provider actually transitions to its `S` state.

#### Provider arbitration

When a Provider receives a `TRACK` message for another instance with state `P`,
it must stop or pause its own playback if it is currently playing.

A Provider must not stop merely because another Provider announces a `TRACK`
with state `S`.

If a Provider stops or pauses because another Provider announced active
playback, it must emit its own `TRACK` with state `S`.

This provides the protocol's expected single-active-player behaviour, but is not
a distributed lock.

#### Source type

Source type identifies the media source type, for example:

    YTM
    SOUNDCLOUD

It is presentation metadata, not an instance or track identifier. It normally
remains unchanged when the track changes.

Controllers must tolerate unknown source types.

#### Album art

The `art` argument is an optional URL-like artwork value.

Use an empty string when no artwork is available:

    1/TRACK<RS>abc12345<RS>S<RS>YTM<RS>Track<RS>Artist<RS>Album<RS>

The value is not required to be a valid or reachable HTTP(S) URL. Controllers
may display, download, proxy, cache, or ignore it.

A Provider advertising `ART` can provide artwork where available; `ART` does not
guarantee artwork for every track.

### 4.2 POS

`POS` reports playback position:

    1/POS<RS>instance-id<RS>position<RS>length

The arguments are:

1.  Provider instance ID
2.  Position in seconds
3.  Track length in seconds

Example:

    1/POS<RS>a7Kx92Qm<RS>37.4<RS>213.0

Position and length are decimal seconds using `.` as the decimal separator.

Negative values, `NaN`, and `Infinity` are invalid. Length may be empty when
unknown.

`POS` does **not** contain playback state and does not establish or change
playback state. Playback state is communicated exclusively through `TRACK`.

Providers should send `POS` roughly once per second while playing, but the
frequency is not strict. `POS` is telemetry rather than a precision timing
protocol.

Providers may send `POS` while not playing, particularly in response to `INFO`,
to report the current position.

Controllers should normally ignore `POS` for an instance that is no longer
current. Newer `POS` messages supersede older ones, but Controllers must
tolerate stale and reordered messages.

### 4.3 CAPABILITIES

`CAPABILITIES` announces the operations and features supported by a Provider:

    1/CAPABILITIES<RS>instance-id<RS>capability<RS>capability...

The arguments are:

1.  Provider instance ID
2.  One or more capabilities

Example:

    1/CAPABILITIES<RS>a7Kx92Qm<RS>PLAY<RS>PAUSE<RS>NEXT<RS>PREV<RS>SEEK<RS>ART


Capabilities are a case-sensitive set; order and duplicates have no meaning.

Defined capabilities are:

| Capability | Meaning                             |
| ---------- | ----------------------------------- |
| PLAY       | Supports PLAY                       |
| PAUSE      | Supports PAUSE                      |
| NEXT       | Supports NEXT                       |
| PREV       | Supports PREV                       |
| SEEK       | Supports SEEK                       |
| ART        | Can provide artwork where available |

`PLAYPAUSE` and `INFO` do not have separate capabilities.

Capability names contain at least one ASCII alphanumeric character and may also
contain `-`, `_`, or `/`.

Providers may advertise additional capabilities. Controllers must ignore unknown
capabilities.

Providers should send `CAPABILITIES` when connecting or becoming available and
may reannounce them at any time. The latest announcement for an instance
replaces its previous capability set.

Capabilities are advisory and are not authorization information. Providers may
receive commands they have not advertised and are not required to execute them.

### 4.4 CONTROL

`CONTROL` requests an operation on a specific Provider:

    1/CONTROL<RS>instance-id<RS>command

Commands with arguments use:

    1/CONTROL<RS>instance-id<RS>command<RS>argument...

The arguments are:

1.  Provider instance ID
2.  Command
3.  Zero or more command arguments

Example:

    1/CONTROL<RS>a7Kx92Qm<RS>SEEK<RS>92.5

Defined commands are:

| Command   | Arguments | Meaning                                     |
| --------- | --------- | ------------------------------------------- |
| PLAYPAUSE | none      | Toggle playback                             |
| PLAY      | none      | Begin/resume playback                       |
| PAUSE     | none      | Pause playback                              |
| NEXT      | none      | Advance according to source behaviour       |
| PREV      | none      | Move backward according to source behaviour |
| SEEK      | position  | Seek to position in seconds                 |
| INFO      | none      | Reannounce current state                    |

Providers must ignore `CONTROL` messages addressed to another instance and
unknown commands.

`CONTROL` is a request, not an acknowledgement. Successful execution is reported
through normal state messages.

#### PLAYPAUSE

Toggles playback.

If it starts playback, the Provider must emit `TRACK` with state `P`.

If it pauses or stops playback, the Provider must emit `TRACK` with state `S`.

#### PLAY

Begins or resumes playback.

If this changes the state from not playing to playing, the Provider must emit
`TRACK` with state `P`.

If the Provider was already playing, no `TRACK` is required solely because of
the `PLAY` command.

#### PAUSE

Pauses playback.

If this changes the state from playing to not playing, the Provider must emit
`TRACK` with state `S`.

#### NEXT / PREV

The underlying media source determines what "next" and "previous" mean. This may
involve changing tracks, restarting a track, or other source-specific behaviour.

If the resulting track or playback state changes, the Provider must emit an
appropriate `TRACK` describing the resulting state.

If the resulting state is active playback, `TRACK` uses state `P`.

If the resulting state is not playing, `TRACK` uses state `S`.

#### SEEK

Format:

    1/CONTROL<RS>instance-id<RS>SEEK<RS>position

`position` is a non-negative decimal number of seconds. Values outside the
available range are handled by the Provider.

The Provider should report the resulting position through `POS`.

`SEEK` does not change playback state.

#### INFO

A targeted INFO request asks a Provider to reannounce its complete current
state:

    1/CONTROL<RS>instance-id<RS>INFO

The addressed Provider must respond with:

1.  `TRACK`
2.  `POS`
3.  `CAPABILITIES`

`TRACK` must be sent regardless of whether the Provider is playing.

`INFO` does not change playback state.

#### Broadcast INFO

The reserved instance ID `*` is used only for broadcast `INFO` requests:

    1/CONTROL<RS>*<RS>INFO

Every connected Provider must respond, regardless of whether it is currently
playing.

Each Provider responds with:

- `TRACK`
- `POS`
- `CAPABILITIES`

A playing Provider sends `TRACK` with state `P`. A non-playing Provider sends
`TRACK` with state `S`.

Controllers can therefore discover all Providers and determine which Provider is
playing from the returned `TRACK` messages.

Discovery is subject to normal relay races and message reordering. Controllers
must tolerate responses that become stale before they arrive.

## 5. Extensions

Extensions add additional message types for optional or non-core behaviour. Each
extension defines its own message types and semantics.

An implementation that does not understand an extension message must ignore it.
Extensions do not change the semantics of core messages unless they explicitly
specify otherwise.

### 5.1 Asynchronous Album Art

The **Asynchronous Album Art** extension defines `TRACK.ART`.

`TRACK.ART` allows a Provider to update the current track's album art
independently of the normal `TRACK` announcement. This is useful when artwork is
fetched asynchronously and is not available when the Provider first announces
the track.

The message format is:

    1/TRACK.ART<RS>instance-id<RS>art

The arguments are:

1.  Provider instance ID
2.  Album-art value

Example:

    1/TRACK.ART<RS>a7Kx92Qm<RS>https://example.invalid/art.jpg

An empty `art` argument indicates that no artwork is currently available:

    1/TRACK.ART<RS>a7Kx92Qm<RS>

`TRACK.ART` applies to the Provider's current track. It does not establish or
change playback state, track identity, or any other `TRACK` fields.

The extension does not change the meaning of the `art` argument in `TRACK`. A
Provider may use `TRACK.ART` to provide or replace artwork after the
corresponding `TRACK` message.

## 6. Quick Reference

### 6.1 Message Types

| Message      | Arguments                                                  | Source                 | Purpose                                            |
| ------------ | ---------------------------------------------------------- | ---------------------- | -------------------------------------------------- |
| TRACK        | instance-id, state, source-type, track, artist, album, art | Core                   | Announce Provider playback state and current track |
| POS          | instance-id, position, length                              | Core                   | Report playback position                           |
| CAPABILITIES | instance-id, capability...                                 | Core                   | Announce Provider capabilities                     |
| CONTROL      | instance-id, command, argument...                          | Core                   | Request an operation from a Provider               |
| TRACK.ART    | instance-id, art                                           | Asynchronous Album Art | Update album art independently of TRACK            |

### 6.2 Control Messages

| Command   | Arguments | Purpose                                     |
| --------- | --------- | ------------------------------------------- |
| PLAYPAUSE | none      | Toggle playback                             |
| PLAY      | none      | Begin/resume playback                       |
| PAUSE     | none      | Pause playback                              |
| NEXT      | none      | Advance according to source behaviour       |
| PREV      | none      | Move backward according to source behaviour |
| SEEK      | position  | Seek to a position in seconds               |
| INFO      | none      | Reannounce current state                    |

`INFO` may be addressed to a Provider instance or broadcast to all Providers
using the reserved instance ID `*`.

## 7. Examples

### 7.1 Basic Playback

A Provider connects and announces capabilities:

    1/CAPABILITIES<RS>a7Kx92Qm<RS>PLAY<RS>PAUSE<RS>NEXT<RS>PREV<RS>SEEK<RS>ART

It starts playing:

    1/TRACK<RS>a7Kx92Qm<RS>P<RS>YTM<RS>Song Title<RS>Artist<RS>Album<RS>https://example.invalid/art.jpg
    1/POS<RS>a7Kx92Qm<RS>0.9<RS>215.0

A Controller pauses it:

    1/CONTROL<RS>a7Kx92Qm<RS>PAUSE

The Provider emits:

    1/TRACK<RS>a7Kx92Qm<RS>S<RS>YTM<RS>Song Title<RS>Artist<RS>Album<RS>https://example.invalid/art.jpg

It may also report its current position:

    1/POS<RS>a7Kx92Qm<RS>2.9<RS>215.0

The Controller resumes it:

    1/CONTROL<RS>a7Kx92Qm<RS>PLAY

The Provider emits:

    1/TRACK<RS>a7Kx92Qm<RS>P<RS>YTM<RS>Song Title<RS>Artist<RS>Album<RS>https://example.invalid/art.jpg

and continues with `POS`:

    1/POS<RS>a7Kx92Qm<RS>3.4<RS>215.0

A Controller can seek with:

    1/CONTROL<RS>a7Kx92Qm<RS>SEEK<RS>92.5

The Provider reports the resulting position through `POS`:

    1/POS<RS>a7Kx92Qm<RS>92.5<RS>215.0

### 7.2 Provider Arbitration

If another Provider starts playback, it announces itself with `TRACK`:

    1/TRACK<RS>q4Mn8Z2x<RS>P<RS>SOUNDCLOUD<RS>Another Song<RS>Another Artist<RS>Another Album<RS>https://example.invalid/art2.jpg

The first Provider sees the `P` state and stops or pauses. It then emits:

    1/TRACK<RS>a7Kx92Qm<RS>S<RS>YTM<RS>Song Title<RS>Artist<RS>Album<RS>https://example.invalid/art.jpg

### 7.3 Discovery

A newly connected Controller can discover all Providers with:

    1/CONTROL<RS>*<RS>INFO

Every connected Provider responds with `TRACK`, `POS`, and `CAPABILITIES`.

For example:

    1/TRACK<RS>a7Kx92Qm<RS>P<RS>YTM<RS>Song A<RS>Artist A<RS>Album A<RS>...
    1/POS<RS>a7Kx92Qm<RS>37.4<RS>213.0
    1/CAPABILITIES<RS>a7Kx92Qm<RS>PLAY<RS>PAUSE<RS>NEXT<RS>PREV<RS>SEEK<RS>ART
    1/TRACK<RS>q4Mn8Z2x<RS>S<RS>SOUNDCLOUD<RS>Song B<RS>Artist B<RS>Album B<RS>...
    1/POS<RS>q4Mn8Z2x<RS>0.0<RS>180.0
    1/CAPABILITIES<RS>q4Mn8Z2x<RS>PLAY<RS>PAUSE<RS>NEXT

The Controller can determine the active Provider from the `TRACK` messages.

## Appendix A. Default Relay Endpoint

The multicast WebSocket relay is usually available at:

    ws://localhost:9994

Clients SHOULD use this endpoint when running in the standard local
configuration.

Deployments may expose the relay at a different WebSocket endpoint as
appropriate. The relay endpoint is therefore a deployment configuration rather
than a requirement of the message protocol itself.
