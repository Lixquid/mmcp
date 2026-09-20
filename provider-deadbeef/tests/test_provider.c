/*
    Test harness for the MMCP provider plugin: implements a mock
    DB_functions_t with canned playback state, loads the plugin, and runs
    until killed. Player events sent by the plugin are appended to the file
    given in argv[1] (one "EVENT <id> [arg]" line each).

    Mock behavior:
      - initial output state: PLAYING
      - track: title "Song A", artist "Artist A", album "Album A",
        duration 213s, position advancing in real time from 37.5s
      - sendmessage() mutates the mock state like the real player would
        (DB_EV_PAUSE pauses, DB_EV_PLAY_CURRENT plays, DB_EV_SEEK seeks)
*/
#include <deadbeef/deadbeef.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

static FILE *event_log;
static struct timespec start_clock;

static volatile int mock_state = DDB_PLAYBACK_STATE_PLAYING; // 1 = playing
static volatile double mock_seek_offset = 0;

static double
elapsed_seconds (void) {
    struct timespec now;
    clock_gettime (CLOCK_MONOTONIC, &now);
    return (double)(now.tv_sec - start_clock.tv_sec)
         + (double)(now.tv_nsec - start_clock.tv_nsec) / 1e9;
}

static DB_playItem_t *
mock_get_playing_track (void) {
    // non-NULL while playing or paused
    return mock_state == DDB_PLAYBACK_STATE_STOPPED ? NULL : (DB_playItem_t *)0x1;
}

static int
mock_pl_get_meta (DB_playItem_t *it, const char *key, char *val, int size) {
    (void)it;
    const char *v = "";
    if (!strcmp (key, "title")) {
        v = "Song A";
    }
    else if (!strcmp (key, "artist")) {
        v = "Artist A";
    }
    else if (!strcmp (key, "album")) {
        v = "Album A";
    }
    snprintf (val, size, "%s", v);
    return 0;
}

static float
mock_streamer_get_playpos (void) {
    double pos = 37.5 + elapsed_seconds () + mock_seek_offset;
    if (pos < 0) {
        pos = 0;
    }
    return (float)pos;
}

static int
mock_sendmessage (uint32_t id, uintptr_t ctx, uint32_t p1, uint32_t p2) {
    (void)ctx;
    (void)p2;

    switch (id) {
    case DB_EV_PAUSE:
        mock_state = DDB_PLAYBACK_STATE_PAUSED;
        break;
    case DB_EV_PLAY_CURRENT:
        mock_state = DDB_PLAYBACK_STATE_PLAYING;
        break;
    case DB_EV_STOP:
        mock_state = DDB_PLAYBACK_STATE_STOPPED;
        break;
    case DB_EV_SEEK:
        // rebase the position clock so playpos reports the seek target
        mock_seek_offset += (double)p1 / 1000.0 - (37.5 + elapsed_seconds () + mock_seek_offset);
        break;
    default:
        break;
    }

    fprintf (event_log, "EVENT %u %u\n", id, p1);
    fflush (event_log);
    return 0;
}

// ---- mock threading --------------------------------------------------------

typedef struct {
    void (*fn) (void *);
    void *ctx;
} mock_thread_arg_t;

static void *
mock_thread_trampoline (void *p) {
    mock_thread_arg_t *a = p;
    a->fn (a->ctx);
    free (a);
    return NULL;
}

static intptr_t
mock_thread_start (void (*fn) (void *ctx), void *ctx) {
    mock_thread_arg_t *a = malloc (sizeof (mock_thread_arg_t));
    a->fn = fn;
    a->ctx = ctx;
    pthread_t tid;
    pthread_create (&tid, NULL, mock_thread_trampoline, a);
    return (intptr_t)tid;
}

static int
mock_thread_join (intptr_t tid) {
    pthread_join ((pthread_t)tid, NULL);
    return 0;
}

static uintptr_t
mock_mutex_create (void) {
    pthread_mutex_t *m = malloc (sizeof (pthread_mutex_t));
    pthread_mutex_init (m, NULL);
    return (uintptr_t)m;
}

static void
mock_mutex_free (uintptr_t m) {
    pthread_mutex_destroy ((pthread_mutex_t *)m);
    free ((void *)m);
}

static int
mock_mutex_lock (uintptr_t m) {
    return pthread_mutex_lock ((pthread_mutex_t *)m);
}

static int
mock_mutex_unlock (uintptr_t m) {
    return pthread_mutex_unlock ((pthread_mutex_t *)m);
}

static volatile int mock_enable = 1; // runtime-mutable mmcp.enable (e2e control)
static void
mock_conf_get_str (const char *key, const char *def, char *buffer, int buffer_size) {
    (void)def;
    if (key && strcmp (key, "mmcp.enable") == 0) {
        snprintf (buffer, buffer_size, "%d", mock_enable);
        return;
    }
    // point the provider at the test relay
    snprintf (buffer, buffer_size, "ws://127.0.0.1:9995");
}

// ---- mock api --------------------------------------------------------------

static int
mock_conf_get_int (const char *key, int def) {
    if (key && strcmp (key, "mmcp.enable") == 0) {
        return mock_enable;
    }
    (void)def;
    return 0;
}

static DB_output_t mock_output = {
    .state = NULL, // set at runtime
};

static ddb_playback_state_t
mock_output_state (void) {
    return (ddb_playback_state_t)mock_state;
}

static DB_functions_t api;

// implemented in mmcp.c
DB_plugin_t *mmcp_load (DB_functions_t *ddb);

static void
init_mock_api (void) {
    memset (&api, 0, sizeof (api));

    api.conf_get_str = mock_conf_get_str;
    api.conf_get_int = mock_conf_get_int;
    api.get_output = NULL; // patched below to return a non-NULL output
    api.pl_get_meta = mock_pl_get_meta;
    api.pl_get_item_duration = NULL; // patched below
    api.pl_item_unref = NULL;        // noop patch below
    api.streamer_get_playpos = mock_streamer_get_playpos;
    api.streamer_get_playing_track_safe = mock_get_playing_track;
    api.sendmessage = mock_sendmessage;
    api.thread_start = mock_thread_start;
    api.thread_join = mock_thread_join;
    api.mutex_create_nonrecursive = mock_mutex_create;
    api.mutex_free = mock_mutex_free;
    api.mutex_lock = mock_mutex_lock;
    api.mutex_unlock = mock_mutex_unlock;
}

// stubs for api members accessed via function pointers that the mock
// implements as plain wrappers
static DB_output_t *
mock_get_output (void) {
    return &mock_output;
}

static float
mock_pl_get_item_duration (DB_playItem_t *it) {
    (void)it;
    return 213.0f;
}

static void
mock_pl_item_unref (DB_playItem_t *it) {
    (void)it;
}

int
main (int argc, char *argv[]) {
    if (argc < 2) {
        fprintf (stderr, "usage: %s <event-log-file>\n", argv[0]);
        return 1;
    }

    event_log = fopen (argv[1], "w");
    if (!event_log) {
        perror ("fopen");
        return 1;
    }

    clock_gettime (CLOCK_MONOTONIC, &start_clock);

    init_mock_api ();
    mock_output.state = mock_output_state;
    api.get_output = mock_get_output;
    api.pl_get_item_duration = mock_pl_get_item_duration;
    api.pl_item_unref = mock_pl_item_unref;

    DB_plugin_t *p = mmcp_load (&api);
    if (!p) {
        fprintf (stderr, "mmcp_load failed\n");
        return 1;
    }
    printf ("provider started\n");
    fflush (stdout);

    if (p->start () != 0) {
        fprintf (stderr, "start failed\n");
        return 1;
    }

    // the real player pushes DB_EV_PLUGINSLOADED after all plugins have
    // started and the streamer is initialized; the plugin defers spawning
    // its threads until then
    p->message (DB_EV_PLUGINSLOADED, 0, 0, 0);

    // run until killed; also accept simple commands on stdin so the e2e can
    // mutate the mock configuration at runtime:
    //   enable 0|1   -> set mmcp.enable and deliver DB_EV_CONFIGCHANGED
    char line[256];
    while (fgets (line, sizeof (line), stdin)) {
        int value;
        if (sscanf (line, "enable %d", &value) == 1) {
            mock_enable = value ? 1 : 0;
            p->message (DB_EV_CONFIGCHANGED, 0, 0, 0);
            printf ("CMD enable=%d\n", mock_enable);
            fflush (stdout);
        }
    }
    return 0;
}
