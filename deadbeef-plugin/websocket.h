/*
    Minimal WebSocket (RFC 6455) client for the MMCP DeaDBeeF plugin.

    Supports the subset of the protocol needed to talk to a stateless
    MMCP relay over an unencrypted ws:// connection:

      - opening handshake (HTTP Upgrade)
      - sending single-frame masked text messages
      - receiving text messages (with continuation frame reassembly)
      - answering pings with pongs
      - clean close handling

    Deliberately no TLS, no compression, no buffering libraries: the
    plugin links nothing outside libc and the player itself.
*/
#ifndef MMCP_WEBSOCKET_H
#define MMCP_WEBSOCKET_H

#include <stddef.h>

// maximum size of a single MMCP message, in both directions
#define MMCP_MAX_MESSAGE_SIZE (256 * 1024)

// Blocking connect + opening handshake. url is "ws://host[:port][/path]".
// Returns 0 on success, -1 on failure (err is filled with a short message).
int mmcp_ws_connect (const char *url, char *err, size_t errsize);

// Breaks the current connection (safe to call from any thread, and while
// another thread is blocked in mmcp_ws_read).
void mmcp_ws_disconnect (void);

// Sends a single text message. Returns 0 on success, -1 on failure.
int mmcp_ws_send_text (const char *data, size_t len);

// Blocking read of the next text message into buf (0-terminated).
// Continuation frames are reassembled; pings are answered automatically.
// Returns message length (>0), 0 on clean close, -1 on error/timeout.
int mmcp_ws_read_text (char *buf, size_t cap);

#endif // MMCP_WEBSOCKET_H
