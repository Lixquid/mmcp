# Multicast Media Control Protocol (MMCP)

## 1\. Overview

MMCP coordinates media playback between browser-based **providers** and
**controllers** over a stateless multicast WebSocket relay.

- **Providers** control a media player.

- **Controllers** display state and send playback commands.

- The relay rebroadcasts each message to all other connected clients.

- The protocol assumes a trusted local environment; authentication is out of
  scope.

- Normally, at most one provider is actively playing.

The default relay endpoint is `ws://localhost:9994`, but deployments may use
another endpoint.

## 2\. Message Format

Messages are text fields separated by ASCII Record Separator (`RS`, `U+001E`):

```
MESSAGE_TYPE<RS>ARGUMENT<RS>ARGUMENT...
```

There is no trailing `RS`.

Version 1 message types are:

```
1/TRACK
1/POS
1/CAPABILITIES
1/CONTROL
```

The version prefix is part of the message type and allows incompatible future
versions such as `2/TRACK`.

Arguments may be empty but must not contain `RS`. Providers must replace `RS`
occurring in source text with a single space. No other escaping is defined.

Messages are normally UTF-8 WebSocket text. Message types and other identifiers
are case-sensitive.

Clients must silently ignore unknown or malformed messages, including invalid
argument counts, invalid numeric values, unknown commands, and invalid
capabilities. Invalid input must not by itself cause disconnection.

## 3\. Instance IDs

Each provider generates an instance ID identifying that running provider.

Instance IDs are exactly eight ASCII alphanumeric characters and should normally
be random. They are identifiers, not credentials.

Providers should avoid reusing an ID while another instance using it may still
be active.

The reserved ID `*` is used only for broadcast `INFO` requests.

## 4\. TRACK

`TRACK` announces a provider's current playback state and track:

```
1/TRACK<RS>instance-id<RS>state<RS>source-type<RS>track<RS>artist<RS>album<RS>art
```

Example while playing:

```
1/TRACK<RS>a7Kx92Qm<RS>P<RS>YTM<RS>Never Gonna Give You Up<RS>Rick Astley<RS>Whenever You Need Somebody<RS>https://example.invalid/art.jpg
```

Example while not playing:

```
1/TRACK<RS>a7Kx92Qm<RS>S<RS>YTM<RS>Never Gonna Give You Up<RS>Rick Astley<RS>Whenever You Need Somebody<RS>https://example.invalid/art.jpg
```

Fields are:

1.  provider instance ID

2.  playback state

3.  source type

4.  track name

5.  artist name

6.  album name

7.  album-art value

### Playback State

The playback state is a single ASCII letter:

- `P` — actively playing

- `S` — not currently playing, including paused, stopped, or suspended

Unknown states must be ignored.

`TRACK` is the **authoritative protocol message for playback state**.

A provider may send `TRACK` whether it is playing or not.

### Required TRACK Transitions

A provider **must emit a TRACK message whenever its playback state changes**.

In particular:

- When playback starts or resumes, it must emit TRACK with state `P`.

- When playback pauses or stops, it must emit TRACK with state `S`.

- When a provider changes tracks while playing, it must emit TRACK with state
  `P` describing the new track.

- When a provider changes tracks while not playing, it should emit TRACK with
  state `S` describing the new track.

A provider may emit additional TRACK messages at any time to reannounce its
current state.

Temporary interruptions such as buffering do not constitute a playback-state
change unless the provider actually transitions to its `S` state.

### Provider Arbitration

When a provider receives a `TRACK` message for another instance with state `P`,
it must stop or pause its own playback if it is currently playing.

This provides the protocol's single-active-player behavior.

A provider must not stop merely because another provider announces a TRACK with
state `S`.

If a provider stops or pauses because another provider announced active
playback, it must emit its own TRACK with state `S`.

For example, if provider A is playing and provider B sends:

```
1/TRACK<RS>q4Mn8Z2x<RS>P<RS>SOUNDCLOUD<RS>Another Song<RS>Another Artist<RS>Another Album<RS>https://example.invalid/art2.jpg
```

provider A stops or pauses and then emits:

```
1/TRACK<RS>a7Kx92Qm<RS>S<RS>YTM<RS>Song Title<RS>Artist<RS>Album<RS>https://example.invalid/art.jpg
```

### Source Type

Source Type identifies the media source type, for example:

```
YTM
SOUNDCLOUD
```

It is presentation metadata, not an instance or track identifier. It normally
remains unchanged when the track changes.

Controllers must tolerate unknown source types.

### Ordering

There is no global ordering mechanism. Controllers should tolerate duplicates,
delays, and messages arriving out of order.

Controllers should use received TRACK messages to determine the current playback
state and track for each provider.

## 5\. POS

`POS` reports playback position:

```
1/POS<RS>instance-id<RS>position<RS>length
```

Example:

```
1/POS<RS>a7Kx92Qm<RS>37.4<RS>213.0
```

Fields are:

1.  provider instance ID

2.  position in seconds

3.  track length in seconds

Position and length are decimal seconds using `.` as the decimal separator.

Negative values, `NaN`, and `Infinity` are invalid. Length may be empty when
unknown.

`POS` does **not** contain playback state and does not establish or change
playback state.

Playback state is communicated exclusively through `TRACK`.

Controllers should normally ignore POS for an instance that is no longer
current.

Providers should send POS roughly once per second while playing, but the
frequency is not strict. POS is telemetry rather than a precision timing
protocol.

Providers may send POS while not playing, particularly in response to `INFO`, to
report the current position.

Controllers should treat newer POS messages for an instance as superseding older
ones, while tolerating stale or reordered messages.

## 6\. CAPABILITIES

`CAPABILITIES` announces the operations supported by a provider:

```
1/CAPABILITIES<RS>instance-id<RS>capability<RS>capability...
```

Example:

```
1/CAPABILITIES<RS>a7Kx92Qm<RS>PLAY<RS>PAUSE<RS>NEXT<RS>PREV<RS>SEEK<RS>ART
```

Capabilities are a case-sensitive set; order and duplicates have no meaning.

Defined capabilities:

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
ones.

Providers should send CAPABILITIES when connecting or becoming available and may
reannounce them at any time. The latest announcement for an instance replaces
its previous capability set.

Capabilities are advisory and are not authorization information. Providers may
receive commands they have not advertised and are not required to execute them.

## 7\. CONTROL

`CONTROL` requests an operation on a specific provider:

```
1/CONTROL<RS>instance-id<RS>command
```

Commands with arguments use:

```
1/CONTROL<RS>instance-id<RS>command<RS>argument...
```

Defined commands:

| Command   | Arguments | Meaning                                    |
| --------- | --------- | ------------------------------------------ |
| PLAYPAUSE | none      | Toggle playback                            |
| PLAY      | none      | Begin/resume playback                      |
| PAUSE     | none      | Pause playback                             |
| NEXT      | none      | Advance according to source behavior       |
| PREV      | none      | Move backward according to source behavior |
| SEEK      | position  | Seek to position in seconds                |
| INFO      | none      | Reannounce current state                   |

Providers must ignore CONTROL messages addressed to another instance and unknown
commands.

CONTROL is a request, not an acknowledgement; successful execution is reported
through normal state messages.

### PLAYPAUSE

Toggles playback.

If it starts playback, the provider must emit TRACK with state `P`.

If it pauses or stops playback, the provider must emit TRACK with state `S`.

### PLAY

Begins or resumes playback.

If this changes the state from not playing to playing, the provider must emit
TRACK with state `P`.

If the provider was already playing, no TRACK is required solely because of the
PLAY command.

### PAUSE

Pauses playback.

If this changes the state from playing to not playing, the provider must emit
TRACK with state `S`.

### NEXT / PREV

The underlying media source determines what "next" and "previous" mean. This may
involve changing tracks, restarting a track, or other source-specific behavior.

If the resulting track or playback state changes, the provider must emit an
appropriate TRACK describing the resulting state.

If the resulting state is active playback, TRACK uses state `P`.

If the resulting state is not playing, TRACK uses state `S`.

### SEEK

Format:

```
1/CONTROL<RS>instance-id<RS>SEEK<RS>position
```

`position` is a non-negative decimal number of seconds. Values outside the
available range are handled by the provider.

The provider should report the resulting position through POS.

SEEK does not change playback state.

### INFO

A targeted INFO request asks a provider to reannounce its complete current
state.

For:

```
1/CONTROL<RS>instance-id<RS>INFO
```

the addressed provider must respond with:

1.  TRACK

2.  POS

3.  CAPABILITIES

TRACK must be sent regardless of whether the provider is playing.

If the provider is playing, TRACK contains state `P`.

If the provider is not playing, TRACK contains state `S`.

POS contains only position and length.

INFO does not change playback state.

## 8\. Active-Player Discovery

A controller that connects after playback has started can request discovery
with:

```
1/CONTROL<RS>*<RS>INFO
```

This is a broadcast INFO request.

**Every connected provider must respond**, regardless of whether it is currently
playing.

Each provider responds with:

- TRACK

- POS

- CAPABILITIES

A playing provider sends TRACK with state `P`.

A non-playing provider sends TRACK with state `S`.

For example, if two providers are connected:

```
1/CONTROL<RS>*<RS>INFO
```

the active provider might respond:

```
1/TRACK<RS>a7Kx92Qm<RS>P<RS>YTM<RS>Song A<RS>Artist A<RS>Album A<RS>...
1/POS<RS>a7Kx92Qm<RS>37.4<RS>213.0
1/CAPABILITIES<RS>a7Kx92Qm<RS>PLAY<RS>PAUSE<RS>NEXT<RS>PREV<RS>SEEK<RS>ART
```

while an inactive provider responds:

```
1/TRACK<RS>q4Mn8Z2x<RS>S<RS>SOUNDCLOUD<RS>Song B<RS>Artist B<RS>Album B<RS>...
1/POS<RS>q4Mn8Z2x<RS>0.0<RS>180.0
1/CAPABILITIES<RS>q4Mn8Z2x<RS>PLAY<RS>PAUSE<RS>NEXT
```

Controllers can therefore discover all providers and determine which provider is
playing from the TRACK messages.

Discovery is subject to normal relay races and message reordering. Controllers
must tolerate responses that become stale before they arrive.

## 9\. Album Art

The `art` field in TRACK is an optional URL-like artwork value.

Use an empty string when no artwork is available:

```
1/TRACK<RS>abc12345<RS>S<RS>YTM<RS>Track<RS>Artist<RS>Album<RS>
```

The value is not required to be a valid or reachable HTTP(S) URL. Controllers
may display, download, proxy, cache, or ignore it.

`ART` means that the provider can provide artwork when available; it does not
guarantee artwork for every track.

## 10\. Relay

The relay is stateless. Its only required behavior is:

1.  accept WebSocket connections;

2.  receive client messages;

3.  rebroadcast each message to all other connected clients.

It does not maintain media state or require knowledge of message semantics.

Message ordering between different senders is not guaranteed.

## 11\. Client Responsibilities

### Providers

Providers:

- generate an instance ID;

- advertise capabilities;

- maintain their current playback state;

- emit TRACK whenever playback changes between `P` and `S`;

- emit TRACK when the current track changes;

- periodically send POS while playing;

- process CONTROL messages addressed to themselves;

- respond to INFO with TRACK, POS, and CAPABILITIES;

- respond to broadcast `* INFO` with TRACK, POS, and CAPABILITIES;

- stop or pause when another provider announces active playback with TRACK state
  `P`.

The provider's playback state is represented by TRACK. Providers must not rely
on POS to communicate playback state.

### Controllers

Controllers:

- use TRACK to determine provider playback state and track metadata;

- use POS for playback position and length;

- use CAPABILITIES to determine available operations;

- address CONTROL messages to the appropriate provider;

- may use `* INFO` for discovery;

- must tolerate multiple providers responding to `* INFO`;

- must tolerate missing, duplicated, stale, malformed, unknown, and out-of-order
  messages.

## 12\. Design Guarantees and Non-Guarantees

MMCP deliberately provides a simple, eventually-consistent view of playback
rather than strong distributed-state guarantees.

It has:

- a stateless relay;

- no heartbeat;

- no acknowledgements;

- no command-response mechanism beyond state announcements;

- no global sequence numbers;

- no global message ordering;

- independent provider instances;

- an expected single active player.

`TRACK` provides the provider's current playback state and track metadata.

`POS` provides position telemetry only and does not communicate playback state.

A provider seeing another provider's TRACK with state `P` is expected to stop or
pause its own playback. This is an eventual coordination mechanism, not a
distributed lock. Simultaneous starts and message races can still temporarily
result in more than one provider playing.

Controllers are responsible for deriving and presenting the best available state
from the messages they receive.

## 13\. Message Summary

| Message      | Format                                                              | Purpose                           |
| ------------ | ------------------------------------------------------------------- | --------------------------------- |
| TRACK        | 1/TRACK<RS>id<RS>state<RS>source<RS>track<RS>artist<RS>album<RS>art | Announce provider state and track |
| POS          | 1/POS<RS>id<RS>position<RS>length                                   | Report playback position          |
| CAPABILITIES | 1/CAPABILITIES<RS>id<RS>capability...                               | Announce supported features       |
| CONTROL      | 1/CONTROL<RS>id<RS>command[<RS>argument...]                         | Request an operation              |

## 14\. Example

Provider connects and announces capabilities:

```
1/CAPABILITIES<RS>a7Kx92Qm<RS>PLAY<RS>PAUSE<RS>NEXT<RS>PREV<RS>SEEK<RS>ART
```

It starts playing:

```
1/TRACK<RS>a7Kx92Qm<RS>P<RS>YTM<RS>Song Title<RS>Artist<RS>Album<RS>https://example.invalid/art.jpg
1/POS<RS>a7Kx92Qm<RS>0.9<RS>215.0
```

A controller pauses it:

```
1/CONTROL<RS>a7Kx92Qm<RS>PAUSE
```

The provider emits TRACK with the new state:

```
1/TRACK<RS>a7Kx92Qm<RS>S<RS>YTM<RS>Song Title<RS>Artist<RS>Album<RS>https://example.invalid/art.jpg
```

It may also report its current position:

```
1/POS<RS>a7Kx92Qm<RS>2.9<RS>215.0
```

The controller resumes it:

```
1/CONTROL<RS>a7Kx92Qm<RS>PLAY
```

The provider emits:

```
1/TRACK<RS>a7Kx92Qm<RS>P<RS>YTM<RS>Song Title<RS>Artist<RS>Album<RS>https://example.invalid/art.jpg
```

and continues with POS:

```
1/POS<RS>a7Kx92Qm<RS>3.4<RS>215.0
```

A controller can seek with:

```
1/CONTROL<RS>a7Kx92Qm<RS>SEEK<RS>92.5
```

The provider reports the resulting position through POS:

```
1/POS<RS>a7Kx92Qm<RS>92.5<RS>215.0
```

If another provider starts playback, it announces itself with TRACK:

```
1/TRACK<RS>q4Mn8Z2x<RS>P<RS>SOUNDCLOUD<RS>Another Song<RS>Another Artist<RS>Another Album<RS>https://example.invalid/art2.jpg
```

The first provider sees the `P` state and stops or pauses. It then emits:

```
1/TRACK<RS>a7Kx92Qm<RS>S<RS>YTM<RS>Song Title<RS>Artist<RS>Album<RS>https://example.invalid/art.jpg
```

A newly connected controller discovers all providers with:

```
1/CONTROL<RS>*<RS>INFO
```

Every provider responds with TRACK, POS, and CAPABILITIES.

# Appendix: Default Relay Endpoint

The multicast WebSocket relay is usually available at:

```
ws://localhost:9994
```

Clients SHOULD use this endpoint when running in the standard local
configuration.

Deployments may expose the relay at a different WebSocket endpoint as
appropriate. The relay endpoint is therefore a deployment configuration rather
than a requirement of the message protocol itself.
