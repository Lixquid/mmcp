/*
    MMCP provider plugin for DeaDBeeF Player.

    Exposes the current DeaDBeeF playback as a Multicast Media Control
    Protocol (MMCP) provider: it connects to a stateless multicast
    WebSocket relay, announces the playback state with TRACK, streams
    position telemetry with POS, and reacts to CONTROL commands from
    controllers.

    Advertised capabilities: PLAY, PAUSE, NEXT, PREV, SEEK.
    Album art is deliberately NOT advertised: the protocol expects
    globally accessible URLs for the art field, which a local player
    cannot guarantee, so the art value is always empty.

    Copyright (C) MMCP contributors.
    License: MIT.
*/
#include <deadbeef/deadbeef.h>
#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#ifdef _WIN32
#include <process.h>
#include <windows.h>
#define mmcp_getpid _getpid
#define mmcp_sleep_ms Sleep
#else
#include <unistd.h>
#define mmcp_getpid getpid
#endif

#include "websocket.h"

#define MMCP_TRACE(...) \
    do { fprintf (stderr, "[MMCP/DDB] " __VA_ARGS__); } while (0)

#define RS "\x1e"
#define RS_CHAR '\x1e'

#define SOURCE_TYPE "DDB"

// capabilities advertised on connect; ART is intentionally absent
#define MMCP_CAPABILITIES "PLAY", "PAUSE", "NEXT", "PREV", "SEEK"

#define DEFAULT_RELAY "ws://localhost:9994"
#define CONF_RELAY "mmcp.relay"

#define RECONNECT_MIN_MS 1000
#define RECONNECT_MAX_MS 15000
#define WORKER_POLL_MS 100
#define POS_INTERVAL_MS 1000

#define META_MAX 1024

static DB_functions_t *deadbeef;
static DB_misc_t plugin;

// instance id (8 ASCII alphanumeric characters), generated at startup
static char instance_id[9];

static char relay_url[512];

static volatile int terminate;
static intptr_t net_tid;
static intptr_t worker_tid;
static uintptr_t send_mutex;

// set once the threads have been spawned; deferred until DB_EV_PLUGINSLOADED,
// because the streamer (and its locks) is only initialized after all plugin
// start() methods have run — touching it earlier crashes the player
static volatile int threads_started;

// set by the event handler when playback state or track info may have changed
static volatile int track_dirty;

// worker thread bookkeeping
static char last_track_key[4096];
static int64_t last_pos_time;

// read loop bookkeeping (net thread only)
static int ws_connected;

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

// MMCP arguments must not contain RS; source text with RS becomes a space.
static void
clean_field (char *s) {
    for (; *s; s++) {
        if (*s == RS_CHAR) {
            *s = ' ';
        }
    }
}

static void
generate_instance_id (void) {
    static const char chars[] =
        "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
        "abcdefghijklmnopqrstuvwxyz"
        "0123456789";

    for (int i = 0; i < 8; i++) {
        instance_id[i] = chars[rand () % 62];
    }
    instance_id[8] = '\0';
}

static void
sleep_ms (int ms) {
#ifdef _WIN32
    Sleep (ms);
#else
    struct timespec ts;
    ts.tv_sec = ms / 1000;
    ts.tv_nsec = (long)(ms % 1000) * 1000000L;
    nanosleep (&ts, NULL);
#endif
}

// ----------------------------------------------------------------------------
// playback snapshot
// ----------------------------------------------------------------------------

typedef struct {
    char state; // 'P' or 'S'
    char title[META_MAX];
    char artist[META_MAX];
    char album[META_MAX];
    float position;
    float length;
    int has_length;
} playback_snapshot_t;

// Fills a consistent snapshot of the player state. The caller owns the
// reference to *out_it (may be NULL) and must pl_item_unref it.
static void
get_snapshot (playback_snapshot_t *snap) {
    memset (snap, 0, sizeof (*snap));

    DB_playItem_t *it = deadbeef->streamer_get_playing_track_safe ();

    ddb_playback_state_t out_state = DDB_PLAYBACK_STATE_STOPPED;
    DB_output_t *output = deadbeef->get_output ();
    if (output) {
        out_state = output->state ();
    }

    snap->state = (it && out_state == DDB_PLAYBACK_STATE_PLAYING) ? 'P' : 'S';

    if (it) {
        deadbeef->pl_get_meta (it, "title", snap->title, sizeof (snap->title));
        deadbeef->pl_get_meta (it, "artist", snap->artist, sizeof (snap->artist));
        deadbeef->pl_get_meta (it, "album", snap->album, sizeof (snap->album));

        float dur = deadbeef->pl_get_item_duration (it);
        if (dur > 0) {
            snap->length = dur;
            snap->has_length = 1;
        }
        deadbeef->pl_item_unref (it);
        it = NULL;
    }

    snap->position = deadbeef->streamer_get_playpos ();
    if (snap->position < 0) {
        snap->position = 0;
    }
    if (!snap->has_length) {
        snap->length = 0;
    }

    clean_field (snap->title);
    clean_field (snap->artist);
    clean_field (snap->album);
}

// ----------------------------------------------------------------------------
// message emission
// ----------------------------------------------------------------------------

// All sends share one lock. Internal helpers expect it to be held; the
// exported emit functions acquire it, so multi-message responses (INFO)
// stay grouped while single messages interleave safely.

// internal helper: send_mutex must be held
static void
send_raw_internal (const char *msg) {
    if (!ws_connected) {
        return;
    }
    if (mmcp_ws_send_text (msg, strlen (msg)) != 0) {
        MMCP_TRACE ("send failed\n");
    }
}

static void
send_capabilities_internal (void) {
    char msg[128];
    int n = snprintf (msg, sizeof (msg),
                      "1/CAPABILITIES" RS "%s" RS "PLAY" RS "PAUSE" RS "NEXT" RS "PREV" RS "SEEK",
                      instance_id);
    if (n > 0 && (size_t)n < sizeof (msg)) {
        send_raw_internal (msg);
    }
}

static void
send_track_internal (const playback_snapshot_t *snap) {
    char art[1] = "";
    char msg[META_MAX * 3 + 256];
    int n = snprintf (msg, sizeof (msg),
                      "1/TRACK" RS "%s" RS "%c" RS SOURCE_TYPE RS "%s" RS "%s" RS "%s" RS "%s",
                      instance_id, snap->state, snap->title, snap->artist, snap->album, art);
    if (n > 0 && (size_t)n < sizeof (msg)) {
        send_raw_internal (msg);
    }
}

static void
send_pos_internal (const playback_snapshot_t *snap) {
    char msg[128];
    char length[32] = "";
    if (snap->has_length) {
        snprintf (length, sizeof (length), "%.2f", snap->length);
    }
    int n = snprintf (msg, sizeof (msg),
                      "1/POS" RS "%s" RS "%.2f" RS "%s",
                      instance_id, snap->position, length);
    if (n > 0 && (size_t)n < sizeof (msg)) {
        send_raw_internal (msg);
    }
}

// TRACK + POS + CAPABILITIES, in the order INFO requires, as one group.
static void
send_info_internal (const playback_snapshot_t *snap) {
    send_track_internal (snap);
    send_pos_internal (snap);
    send_capabilities_internal ();
}

static void
with_send_lock (void (*fn) (const playback_snapshot_t *), const playback_snapshot_t *snap) {
    deadbeef->mutex_lock (send_mutex);
    fn (snap);
    deadbeef->mutex_unlock (send_mutex);
}

// Builds a dedup key for the snapshot; returns 1 when it differs from the
// last announced one.
static int
track_key_changed (const playback_snapshot_t *snap) {
    char key[4096];
    snprintf (key, sizeof (key), "%c\x1f%s\x1f%s\x1f%s",
              snap->state, snap->title, snap->artist, snap->album);
    if (strcmp (key, last_track_key) == 0) {
        return 0;
    }
    snprintf (last_track_key, sizeof (last_track_key), "%s", key);
    return 1;
}

// ----------------------------------------------------------------------------
// incoming messages
// ----------------------------------------------------------------------------

static int
valid_instance_id (const char *id) {
    if (strlen (id) != 8) {
        return 0;
    }
    for (const char *p = id; *p; p++) {
        if (!((*p >= 'a' && *p <= 'z') || (*p >= 'A' && *p <= 'Z') || (*p >= '0' && *p <= '9'))) {
            return 0;
        }
    }
    return 1;
}

static void
arbitrate_against_other_provider (void) {
    DB_output_t *output = deadbeef->get_output ();
    if (!output || output->state () != DDB_PLAYBACK_STATE_PLAYING) {
        return;
    }

    MMCP_TRACE ("another provider is playing; pausing DeaDBeeF\n");
    deadbeef->sendmessage (DB_EV_PAUSE, 0, 0, 0);

    // MMCP requires a TRACK with state S when we stop because another
    // provider became active. DB_EV_PAUSED will also refresh, but emit
    // immediately so controllers see the transition promptly.
    playback_snapshot_t snap;
    get_snapshot (&snap);
    snap.state = 'S';
    with_send_lock (send_track_internal, &snap);
    track_key_changed (&snap); // update the dedup key
}

static void
handle_control (char **parts, int nparts) {
    if (nparts < 3) {
        return;
    }

    const char *target = parts[1];
    const char *command = parts[2];

    // ignore commands addressed to another provider
    if (strcmp (target, instance_id) != 0 && strcmp (target, "*") != 0) {
        return;
    }

    if (strcmp (target, "*") == 0) {
        // broadcast INFO is the only command addressed to all providers
        if (strcmp (command, "INFO") == 0 && nparts == 3) {
            playback_snapshot_t snap;
            get_snapshot (&snap);
            with_send_lock (send_info_internal, &snap);
        }
        return;
    }

    if (strcmp (command, "PLAYPAUSE") == 0 && nparts == 3) {
        DB_output_t *output = deadbeef->get_output ();
        ddb_playback_state_t st = output ? output->state () : DDB_PLAYBACK_STATE_STOPPED;
        if (st == DDB_PLAYBACK_STATE_PLAYING) {
            deadbeef->sendmessage (DB_EV_PAUSE, 0, 0, 0);
        }
        else {
            deadbeef->sendmessage (DB_EV_PLAY_CURRENT, 0, 0, 0);
        }
        track_dirty = 1;
    }
    else if (strcmp (command, "PLAY") == 0 && nparts == 3) {
        deadbeef->sendmessage (DB_EV_PLAY_CURRENT, 0, 0, 0);
        track_dirty = 1;
    }
    else if (strcmp (command, "PAUSE") == 0 && nparts == 3) {
        deadbeef->sendmessage (DB_EV_PAUSE, 0, 0, 0);
        track_dirty = 1;
    }
    else if (strcmp (command, "NEXT") == 0 && nparts == 3) {
        deadbeef->sendmessage (DB_EV_NEXT, 0, 0, 0);
        track_dirty = 1;
    }
    else if (strcmp (command, "PREV") == 0 && nparts == 3) {
        deadbeef->sendmessage (DB_EV_PREV, 0, 0, 0);
        track_dirty = 1;
    }
    else if (strcmp (command, "SEEK") == 0 && nparts == 4) {
        char *end = NULL;
        double position = strtod (parts[3], &end);
        if (end == parts[3] || *end != '\0' || !isfinite (position) || position < 0) {
            return; // malformed seek values are ignored
        }
        DB_output_t *output = deadbeef->get_output ();
        ddb_playback_state_t st = output ? output->state () : DDB_PLAYBACK_STATE_STOPPED;
        if (st == DDB_PLAYBACK_STATE_STOPPED) {
            return; // nothing to seek
        }
        deadbeef->sendmessage (DB_EV_SEEK, 0, (uint32_t)(position * 1000.0), 0);
    }
    else if (strcmp (command, "INFO") == 0 && nparts == 3) {
        playback_snapshot_t snap;
        get_snapshot (&snap);
        with_send_lock (send_info_internal, &snap);
    }
    // unknown commands are ignored
}

// Splits data on RS in place, preserving empty fields (strtok would drop
// them, but empty arguments are legal in MMCP — e.g. the art field).
// Returns the number of fields.
static int
split_rs (char *data, char **parts, int maxparts) {
    int n = 0;
    char *p = data;
    parts[n++] = p;
    while (*p) {
        if (*p == RS_CHAR) {
            *p = '\0';
            if (n < maxparts) {
                parts[n++] = p + 1;
            }
        }
        p++;
    }
    return n;
}

static void
handle_message (char *data) {
    char *parts[64];
    int nparts = split_rs (data, parts, 64);

    if (nparts == 0) {
        return;
    }

    // Unknown types and versions are silently ignored.
    if (strcmp (parts[0], "1/TRACK") == 0 && nparts == 8) {
        const char *id = parts[1];
        const char *state = parts[2];
        if (!valid_instance_id (id) || strcmp (id, instance_id) == 0) {
            return;
        }
        if (strcmp (state, "P") == 0) {
            arbitrate_against_other_provider ();
        }
        // state S from another provider does not affect our playback
    }
    else if (strcmp (parts[0], "1/CONTROL") == 0) {
        handle_control (parts, nparts);
    }
    // 1/POS and 1/CAPABILITIES are not relevant to a provider
}

// ----------------------------------------------------------------------------
// threads
// ----------------------------------------------------------------------------

static void
net_thread (void *ctx) {
    (void)ctx;

    int reconnect_delay = RECONNECT_MIN_MS;

    while (!terminate) {
        // (re)read the relay url in case it changed
        char url[512];
        deadbeef->conf_get_str (CONF_RELAY, DEFAULT_RELAY, url, sizeof (url));

        char err[256];
        if (mmcp_ws_connect (url, err, sizeof (err)) != 0) {
            MMCP_TRACE ("connect to %s failed: %s\n", url, err);
        }
        else {
            MMCP_TRACE ("connected to %s (instance %s)\n", url, instance_id);
            reconnect_delay = RECONNECT_MIN_MS;
            ws_connected = 1;

            // announce ourselves: capabilities plus the current state, so
            // controllers connected before us learn about this provider
            playback_snapshot_t snap;
            get_snapshot (&snap);
            deadbeef->mutex_lock (send_mutex);
            send_capabilities_internal ();
            send_track_internal (&snap);
            send_pos_internal (&snap);
            deadbeef->mutex_unlock (send_mutex);
            snprintf (last_track_key, sizeof (last_track_key), "%c\x1f%s\x1f%s\x1f%s",
                      snap.state, snap.title, snap.artist, snap.album);

            char buf[MMCP_MAX_MESSAGE_SIZE];
            for (;;) {
                int n = mmcp_ws_read_text (buf, sizeof (buf));
                if (n <= 0) {
                    break;
                }
                handle_message (buf);
            }

            ws_connected = 0;
            MMCP_TRACE ("relay connection lost\n");
        }

        if (terminate) {
            break;
        }

        // interruptible backoff
        int slept = 0;
        while (!terminate && slept < reconnect_delay) {
            sleep_ms (WORKER_POLL_MS);
            slept += WORKER_POLL_MS;
        }
        reconnect_delay *= 2;
        if (reconnect_delay > RECONNECT_MAX_MS) {
            reconnect_delay = RECONNECT_MAX_MS;
        }
    }
}

static void
worker_thread (void *ctx) {
    (void)ctx;

    while (!terminate) {
        if (ws_connected) {
            int64_t now = (int64_t)time (NULL) * 1000;

            playback_snapshot_t snap;
            get_snapshot (&snap);

            // the send lock also guards last_track_key, which the net thread
            // updates when announcing after a (re)connect
            deadbeef->mutex_lock (send_mutex);

            if (track_dirty || track_key_changed (&snap)) {
                track_dirty = 0;
                send_track_internal (&snap);
            }

            // POS roughly once per second while playing
            if (snap.state == 'P' && now - last_pos_time >= POS_INTERVAL_MS) {
                last_pos_time = now;
                send_pos_internal (&snap);
            }

            deadbeef->mutex_unlock (send_mutex);
        }

        sleep_ms (WORKER_POLL_MS);
    }
}

// ----------------------------------------------------------------------------
// plugin lifecycle
// ----------------------------------------------------------------------------

// Spawns the network and worker threads. Called once, on DB_EV_PLUGINSLOADED.
static void
start_threads (void) {
    if (threads_started) {
        return;
    }
    threads_started = 1;

    send_mutex = deadbeef->mutex_create_nonrecursive ();
    net_tid = deadbeef->thread_start (net_thread, NULL);
    worker_tid = deadbeef->thread_start (worker_thread, NULL);
}

static int
mmcp_start (void) {
    terminate = 0;
    track_dirty = 1;
    threads_started = 0;
    last_track_key[0] = '\0';
    last_pos_time = 0;
    ws_connected = 0;

    deadbeef->conf_get_str (CONF_RELAY, DEFAULT_RELAY, relay_url, sizeof (relay_url));

    srand ((unsigned)(time (NULL) ^ mmcp_getpid ()));
    generate_instance_id ();
    MMCP_TRACE ("starting, instance %s, relay %s\n", instance_id, relay_url);

    // NOTE: threads are deliberately NOT started here. start() runs while
    // the player is still initializing; the streamer and its locks only
    // become valid after DB_EV_PLUGINSLOADED.
    return 0;
}

static int
mmcp_stop (void) {
    terminate = 1;
    mmcp_ws_disconnect ();

    if (net_tid) {
        deadbeef->thread_join (net_tid);
        net_tid = 0;
    }
    if (worker_tid) {
        deadbeef->thread_join (worker_tid);
        worker_tid = 0;
    }
    if (send_mutex) {
        deadbeef->mutex_free (send_mutex);
        send_mutex = 0;
    }

    MMCP_TRACE ("stopped\n");
    return 0;
}

static int
mmcp_message (uint32_t id, uintptr_t ctx, uint32_t p1, uint32_t p2) {
    (void)ctx;
    (void)p1;
    (void)p2;

    switch (id) {
    case DB_EV_PLUGINSLOADED:
        // the player is fully initialized (streamer included) — safe to
        // touch it from our threads now
        start_threads ();
        track_dirty = 1;
        break;
    case DB_EV_SONGSTARTED:
    case DB_EV_SONGCHANGED:
    case DB_EV_SONGFINISHED:
    case DB_EV_PAUSED:
    case DB_EV_SEEKED:
    case DB_EV_TRACKINFOCHANGED:
    case DB_EV_STOP:
        // mark dirty; the worker thread sends the TRACK/POS promptly
        track_dirty = 1;
        break;
    case DB_EV_CONFIGCHANGED: {
        char url[512];
        deadbeef->conf_get_str (CONF_RELAY, DEFAULT_RELAY, url, sizeof (url));
        if (strcmp (url, relay_url) != 0) {
            snprintf (relay_url, sizeof (relay_url), "%s", url);
            // drop the current connection; the net thread reconnects to the
            // new relay
            mmcp_ws_disconnect ();
        }
        break;
    }
    default:
        break;
    }

    return 0;
}

static const char settings_dlg[] =
    "property \"MMCP relay URL\" entry mmcp.relay \"" DEFAULT_RELAY "\";\n";

static DB_misc_t plugin = {
    DDB_PLUGIN_SET_API_VERSION.plugin.type = DB_PLUGIN_MISC,
    .plugin.version_major = 1,
    .plugin.version_minor = 0,
    .plugin.id = "mmcp",
    .plugin.name = "MMCP Provider",
    .plugin.descr =
        "Exposes DeaDBeeF playback to MMCP controllers over a multicast "
        "WebSocket relay.\n"
        "Advertised capabilities: PLAY, PAUSE, NEXT, PREV, SEEK.\n"
        "Album art is not advertised: the protocol expects globally "
        "accessible art URLs, which a local player cannot provide.\n",
    .plugin.copyright = "MIT licensed. See LICENSE.\n",
    .plugin.website = "https://github.com/DeaDBeeF-Player/deadbeef",
    .plugin.start = mmcp_start,
    .plugin.stop = mmcp_stop,
    .plugin.message = mmcp_message,
    .plugin.configdialog = settings_dlg,
};

DB_plugin_t *
mmcp_load (DB_functions_t *ddb) {
    deadbeef = ddb;
    return &plugin.plugin;
}
