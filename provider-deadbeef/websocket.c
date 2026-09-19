/*
    Minimal WebSocket (RFC 6455) client for the MMCP DeaDBeeF plugin.
    See websocket.h for the supported subset.
*/
#include "websocket.h"

#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#ifdef _WIN32
#include <winsock2.h>
#include <ws2tcpip.h>
typedef SOCKET mmcp_socket_t;
#define MMCP_INVALID_SOCKET INVALID_SOCKET
#define mmcp_close_socket(s) closesocket (s)
#define mmcp_send(s, buf, len) send (s, (const char *)(buf), (int)(len), 0)
#define mmcp_recv(s, buf, len) recv (s, (char *)(buf), (int)(len), 0)
#define MMCP_SOCKET_ERROR SOCKET_ERROR
#define MMCP_MUTEX_LOCK() EnterCriticalSection (&ws_mutex)
#define MMCP_MUTEX_UNLOCK() LeaveCriticalSection (&ws_mutex)
#else
#include <errno.h>
#include <fcntl.h>
#include <netdb.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <sys/socket.h>
#include <sys/types.h>
#include <unistd.h>
typedef int mmcp_socket_t;
#define MMCP_INVALID_SOCKET (-1)
#define mmcp_close_socket(s) close (s)
#define mmcp_send(s, buf, len) send (s, buf, len, MSG_NOSIGNAL)
#define mmcp_recv(s, buf, len) recv (s, buf, len, 0)
#define MMCP_SOCKET_ERROR (-1)
#define MMCP_MUTEX_LOCK() pthread_mutex_lock (&ws_mutex)
#define MMCP_MUTEX_UNLOCK() pthread_mutex_unlock (&ws_mutex)
#endif

#ifndef _WIN32
#include <pthread.h>
#endif

#define WS_OPCODE_CONT  0x0
#define WS_OPCODE_TEXT  0x1
#define WS_OPCODE_CLOSE 0x8
#define WS_OPCODE_PING  0x9
#define WS_OPCODE_PONG  0xa

#define MAX_MESSAGE_SIZE MMCP_MAX_MESSAGE_SIZE

static mmcp_socket_t ws_fd = MMCP_INVALID_SOCKET;

#ifndef _WIN32
static pthread_mutex_t ws_mutex = PTHREAD_MUTEX_INITIALIZER;
#else
// one-time critical section initialization, safe from any thread
static INIT_ONCE ws_mutex_once = INIT_ONCE_STATIC_INIT;
static CRITICAL_SECTION ws_mutex;

static BOOL CALLBACK
ws_mutex_init (PINIT_ONCE once, PVOID param, PVOID *ctx) {
    (void)once;
    (void)param;
    (void)ctx;
    InitializeCriticalSection (&ws_mutex);
    return TRUE;
}

static void
ws_mutex_ensure (void) {
    InitOnceExecuteOnce (&ws_mutex_once, ws_mutex_init, NULL, NULL);
}
#endif

// reassembly buffer for fragmented messages
static unsigned char *cont_buf = NULL;
static size_t cont_len = 0;

static uint32_t
random_u32 (void) {
#ifdef _WIN32
    return (uint32_t)rand () << 16 ^ (uint32_t)rand ();
#else
    uint32_t v = 0;
    FILE *f = fopen ("/dev/urandom", "rb");
    if (f) {
        if (fread (&v, 1, sizeof (v), f) != sizeof (v)) {
            v = (uint32_t)rand () << 16 ^ (uint32_t)getpid ();
        }
        fclose (f);
    }
    else {
        v = (uint32_t)rand () << 16 ^ (uint32_t)getpid ();
    }
    return v;
#endif
}

static void
mmcp_b64_encode (const unsigned char *in, size_t inlen, char *out, size_t outcap) {
    static const char tbl[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    size_t o = 0;
    for (size_t i = 0; i + 2 < inlen && o + 4 < outcap; i += 3) {
        uint32_t v = ((uint32_t)in[i] << 16) | ((uint32_t)in[i + 1] << 8) | in[i + 2];
        out[o++] = tbl[(v >> 18) & 63];
        out[o++] = tbl[(v >> 12) & 63];
        out[o++] = tbl[(v >> 6) & 63];
        out[o++] = tbl[v & 63];
    }
    size_t rem = inlen % 3;
    if (rem == 1 && o + 4 < outcap) {
        uint32_t v = (uint32_t)in[inlen - 1] << 16;
        out[o++] = tbl[(v >> 18) & 63];
        out[o++] = tbl[(v >> 12) & 63];
        out[o++] = '=';
        out[o++] = '=';
    }
    else if (rem == 2 && o + 4 < outcap) {
        uint32_t v = ((uint32_t)in[inlen - 2] << 16) | ((uint32_t)in[inlen - 1] << 8);
        out[o++] = tbl[(v >> 18) & 63];
        out[o++] = tbl[(v >> 12) & 63];
        out[o++] = tbl[(v >> 6) & 63];
        out[o++] = '=';
    }
    out[o] = '\0';
}

static int
parse_ws_url (const char *url, char *host, size_t hostcap, int *port, char *path, size_t pathcap) {
    if (strncmp (url, "ws://", 5) != 0) {
        return -1; // only unencrypted relays are supported
    }
    const char *p = url + 5;
    const char *slash = strchr (p, '/');
    const char *hostport_end = slash ? slash : p + strlen (p);

    const char *colon = memchr (p, ':', (size_t)(hostport_end - p));
    size_t hostlen = colon ? (size_t)(colon - p) : (size_t)(hostport_end - p);
    if (hostlen == 0 || hostlen >= hostcap) {
        return -1;
    }
    memcpy (host, p, hostlen);
    host[hostlen] = '\0';

    *port = 80;
    if (colon) {
        *port = atoi (colon + 1);
        if (*port <= 0 || *port > 65535) {
            return -1;
        }
    }

    snprintf (path, pathcap, "%s", slash ? slash : "/");
    return 0;
}

static int
read_http_response_headers (mmcp_socket_t fd) {
    // Read byte-by-byte until the end of the headers; the response to an
    // upgrade contains no body, so nothing is lost.
    char line[1024];
    size_t len = 0;
    int status = 0;
    for (;;) {
        char c;
        ssize_t r = mmcp_recv (fd, &c, 1);
        if (r != 1) {
            return -1;
        }
        if (c != '\n') {
            if (len + 1 < sizeof (line)) {
                line[len++] = c;
            }
            continue;
        }
        line[len] = '\0';
        len = 0;

        size_t l = strlen (line);
        if (l > 0 && line[l - 1] == '\r') {
            line[l - 1] = '\0';
        }
        if (line[0] == '\0') {
            break; // end of headers
        }
        if (strncmp (line, "HTTP/", 5) == 0) {
            status = atoi (strstr (line, " ") ? strstr (line, " ") + 1 : "0");
        }
    }
    return status == 101 ? 0 : -1;
}

int
mmcp_ws_connect (const char *url, char *err, size_t errsize) {
    err[0] = '\0';

    char host[256], path[256];
    int port;
    if (parse_ws_url (url, host, sizeof (host), &port, path, sizeof (path)) != 0) {
        snprintf (err, errsize, "invalid relay url (only ws:// supported): %s", url);
        return -1;
    }

#ifdef _WIN32
    static BOOL wsa_init = FALSE;
    if (!wsa_init) {
        WSADATA wsa;
        if (WSAStartup (MAKEWORD (2, 2), &wsa) != 0) {
            snprintf (err, errsize, "WSAStartup failed");
            return -1;
        }
        wsa_init = TRUE;
    }
#endif

    char portstr[8];
    snprintf (portstr, sizeof (portstr), "%d", port);

    struct addrinfo hints;
    memset (&hints, 0, sizeof (hints));
    hints.ai_family = AF_UNSPEC;
    hints.ai_socktype = SOCK_STREAM;

    struct addrinfo *res = NULL;
    if (getaddrinfo (host, portstr, &hints, &res) != 0 || !res) {
        snprintf (err, errsize, "cannot resolve relay host: %s", host);
        return -1;
    }

    mmcp_socket_t fd = MMCP_INVALID_SOCKET;
    for (struct addrinfo *ai = res; ai; ai = ai->ai_next) {
        fd = socket (ai->ai_family, ai->ai_socktype, ai->ai_protocol);
        if (fd == MMCP_INVALID_SOCKET) {
            continue;
        }
        if (connect (fd, ai->ai_addr, ai->ai_addrlen) != MMCP_SOCKET_ERROR) {
            break;
        }
        mmcp_close_socket (fd);
        fd = MMCP_INVALID_SOCKET;
    }
    freeaddrinfo (res);

    if (fd == MMCP_INVALID_SOCKET) {
        snprintf (err, errsize, "cannot connect to %s:%d", host, port);
        return -1;
    }

    // opening handshake
    unsigned char key[16];
    for (size_t i = 0; i < sizeof (key); i++) {
        key[i] = (unsigned char)(random_u32 () >> ((i % 4) * 8));
    }
    char key_b64[32];
    mmcp_b64_encode (key, sizeof (key), key_b64, sizeof (key_b64));

    char req[1024];
    int reqlen = snprintf (req, sizeof (req),
                           "GET %s HTTP/1.1\r\n"
                           "Host: %s:%d\r\n"
                           "Upgrade: websocket\r\n"
                           "Connection: Upgrade\r\n"
                           "Sec-WebSocket-Key: %s\r\n"
                           "Sec-WebSocket-Version: 13\r\n"
                           "\r\n",
                           path, host, port, key_b64);

    MMCP_MUTEX_LOCK ();
    if (mmcp_send (fd, req, (size_t)reqlen) == MMCP_SOCKET_ERROR) {
        MMCP_MUTEX_UNLOCK ();
        mmcp_close_socket (fd);
        snprintf (err, errsize, "handshake send failed");
        return -1;
    }
    ws_fd = fd;
    int hs = read_http_response_headers (fd);
    if (hs != 0) {
        ws_fd = MMCP_INVALID_SOCKET;
        MMCP_MUTEX_UNLOCK ();
        mmcp_close_socket (fd);
        snprintf (err, errsize, "relay did not accept websocket upgrade");
        return -1;
    }
    MMCP_MUTEX_UNLOCK ();

    cont_len = 0;
    return 0;
}

void
mmcp_ws_disconnect (void) {
#ifdef _WIN32
    ws_mutex_ensure ();
#endif
    MMCP_MUTEX_LOCK ();
    if (ws_fd != MMCP_INVALID_SOCKET) {
        // best-effort close frame, then hard reset so a blocked reader wakes up
        unsigned char frame[2] = {0x88, 0x00};
        mmcp_send (ws_fd, frame, sizeof (frame));
        mmcp_close_socket (ws_fd);
        ws_fd = MMCP_INVALID_SOCKET;
    }
    cont_len = 0;
    MMCP_MUTEX_UNLOCK ();
}

static int
send_all (mmcp_socket_t fd, const unsigned char *buf, size_t len) {
    size_t off = 0;
    while (off < len) {
        ssize_t n = mmcp_send (fd, buf + off, len - off);
        if (n <= 0) {
            return -1;
        }
        off += (size_t)n;
    }
    return 0;
}

int
mmcp_ws_send_text (const char *data, size_t len) {
    if (len > MAX_MESSAGE_SIZE) {
        return -1;
    }

    MMCP_MUTEX_LOCK ();
    if (ws_fd == MMCP_INVALID_SOCKET) {
        MMCP_MUTEX_UNLOCK ();
        return -1;
    }

    // single masked text frame; client-to-server frames must be masked
    unsigned char hdr[10];
    size_t hdrlen = 0;
    hdr[hdrlen++] = 0x81; // FIN + text
    if (len < 126) {
        hdr[hdrlen++] = (unsigned char)(0x80 | len);
    }
    else if (len <= 0xffff) {
        hdr[hdrlen++] = (unsigned char)(0x80 | 126);
        hdr[hdrlen++] = (unsigned char)(len >> 8);
        hdr[hdrlen++] = (unsigned char)(len & 0xff);
    }
    else {
        hdr[hdrlen++] = (unsigned char)(0x80 | 127);
        for (int i = 7; i >= 0; i--) {
            hdr[hdrlen++] = (unsigned char)(((uint64_t)len >> (i * 8)) & 0xff);
        }
    }

    unsigned char mask[4];
    for (size_t i = 0; i < 4; i++) {
        mask[i] = (unsigned char)(random_u32 () >> ((i % 4) * 8));
    }
    memcpy (hdr + hdrlen, mask, 4);
    hdrlen += 4;

    unsigned char *frame = malloc (hdrlen + len);
    if (!frame) {
        MMCP_MUTEX_UNLOCK ();
        return -1;
    }
    memcpy (frame, hdr, hdrlen);
    for (size_t i = 0; i < len; i++) {
        frame[hdrlen + i] = (unsigned char)data[i] ^ mask[i % 4];
    }

    int res = send_all (ws_fd, frame, hdrlen + len);
    free (frame);

    if (res != 0) {
        // the connection is broken; reset it so the reader unblocks
        mmcp_close_socket (ws_fd);
        ws_fd = MMCP_INVALID_SOCKET;
    }
    MMCP_MUTEX_UNLOCK ();
    return res == 0 ? 0 : -1;
}

static int
read_exact (mmcp_socket_t fd, unsigned char *buf, size_t len) {
    size_t off = 0;
    while (off < len) {
        ssize_t n = mmcp_recv (fd, buf + off, len - off);
        if (n <= 0) {
            return -1;
        }
        off += (size_t)n;
    }
    return 0;
}

// Reads a single frame; returns payload opcode, or -1 on error / close.
// payload is malloc'd and returned through out/outlen.
static int
read_frame (mmcp_socket_t fd, int *opcode, unsigned char **out, size_t *outlen) {
    unsigned char hdr[2];
    if (read_exact (fd, hdr, 2) != 0) {
        return -1;
    }

    int fin = hdr[0] & 0x80;
    int op = hdr[0] & 0x0f;
    int masked = hdr[1] & 0x80;
    uint64_t len = hdr[1] & 0x7f;

    if (len == 126) {
        unsigned char ext[2];
        if (read_exact (fd, ext, 2) != 0) {
            return -1;
        }
        len = ((uint64_t)ext[0] << 8) | ext[1];
    }
    else if (len == 127) {
        unsigned char ext[8];
        if (read_exact (fd, ext, 8) != 0) {
            return -1;
        }
        len = 0;
        for (int i = 0; i < 8; i++) {
            len = (len << 8) | ext[i];
        }
    }

    if (len > MAX_MESSAGE_SIZE) {
        return -1;
    }

    unsigned char mask[4] = {0, 0, 0, 0};
    if (masked) {
        if (read_exact (fd, mask, 4) != 0) {
            return -1;
        }
    }

    unsigned char *payload = malloc (len ? len : 1);
    if (!payload) {
        return -1;
    }
    if (len > 0 && read_exact (fd, payload, (size_t)len) != 0) {
        free (payload);
        return -1;
    }
    if (masked) {
        for (uint64_t i = 0; i < len; i++) {
            payload[i] ^= mask[i % 4];
        }
    }

    if (op == WS_OPCODE_PING) {
        // reply with a pong carrying the same payload
        unsigned char reply[10] = {0};
        size_t rl = 0;
        reply[rl++] = 0x8a; // FIN + pong
        if (len < 126) {
            reply[rl++] = (unsigned char)len;
        }
        else {
            reply[rl++] = 126;
            reply[rl++] = (unsigned char)((len >> 8) & 0xff);
            reply[rl++] = (unsigned char)(len & 0xff);
        }
        // server-to-client pings are unmasked per RFC 6455
        if (len > 0) {
            unsigned char *full = malloc (rl + (size_t)len);
            if (!full) {
                free (payload);
                return -1;
            }
            memcpy (full, reply, rl);
            memcpy (full + rl, payload, (size_t)len);
            int res = send_all (fd, full, rl + (size_t)len);
            free (full);
            if (res != 0) {
                free (payload);
                return -1;
            }
        }
        else {
            if (send_all (fd, reply, rl) != 0) {
                free (payload);
                return -1;
            }
        }
        free (payload);
        return read_frame (fd, opcode, out, outlen);
    }

    if (op == WS_OPCODE_CLOSE) {
        free (payload);
        return -1;
    }

    if (op == WS_OPCODE_TEXT || op == WS_OPCODE_CONT) {
        if (!fin) {
            // fragment: buffer it and wait for the continuation chain
            unsigned char *nb = realloc (cont_buf, cont_len + (size_t)len);
            if (!nb) {
                free (payload);
                return -1;
            }
            cont_buf = nb;
            memcpy (cont_buf + cont_len, payload, (size_t)len);
            cont_len += (size_t)len;
            free (payload);
            return read_frame (fd, opcode, out, outlen);
        }

        if (cont_len > 0) {
            // final fragment of a fragmented message
            size_t total = cont_len + (size_t)len;
            unsigned char *msg = malloc (total ? total : 1);
            if (!msg) {
                free (payload);
                return -1;
            }
            memcpy (msg, cont_buf, cont_len);
            memcpy (msg + cont_len, payload, (size_t)len);
            free (payload);
            free (cont_buf);
            cont_buf = NULL;
            cont_len = 0;
            *opcode = WS_OPCODE_TEXT;
            *out = msg;
            *outlen = total;
            return 0;
        }

        *opcode = op;
        *out = payload;
        *outlen = (size_t)len;
        return 0;
    }

    // binary and other opcodes are not used by MMCP; drop them
    free (payload);
    return read_frame (fd, opcode, out, outlen);
}

int
mmcp_ws_read_text (char *buf, size_t cap) {
    for (;;) {
        int opcode;
        unsigned char *payload = NULL;
        size_t len = 0;

        MMCP_MUTEX_LOCK ();
        mmcp_socket_t fd = ws_fd;
        MMCP_MUTEX_UNLOCK ();

        if (fd == MMCP_INVALID_SOCKET) {
            return -1;
        }

        // the reader owns the socket while blocked; sends happen from other
        // threads and only close the socket on failure, which unblocks us
        if (read_frame (fd, &opcode, &payload, &len) != 0) {
            return -1;
        }

        if (opcode != WS_OPCODE_TEXT || len == 0 || len >= cap) {
            free (payload);
            continue;
        }

        memcpy (buf, payload, len);
        buf[len] = '\0';
        free (payload);
        return (int)len;
    }
}
