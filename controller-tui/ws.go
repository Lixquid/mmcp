package main

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/charmbracelet/bubbletea"
	"github.com/gorilla/websocket"
)

const (
	reconnectDelay = 2 * time.Second
	sendQueueSize  = 32
)

// Messages the WebSocket client delivers into the Bubble Tea event loop.
type (
	wsConnectedMsg    struct{}
	wsDisconnectedMsg struct{}
	wsErrorMsg        struct{ err error }
	wsDataMsg         struct{ data []byte }
)

// wsClient maintains a connection to the stateless relay, reconnecting
// indefinitely. All interaction with the UI happens through program.Send.
type wsClient struct {
	relay  string
	sendCh chan []byte
	ctx    context.Context
	cancel context.CancelFunc
}

func newWSClient(relay string) *wsClient {
	ctx, cancel := context.WithCancel(context.Background())
	return &wsClient{
		relay:  relay,
		sendCh: make(chan []byte, sendQueueSize),
		ctx:    ctx,
		cancel: cancel,
	}
}

func (c *wsClient) start(program *tea.Program) {
	go c.loop(program)
}

func (c *wsClient) stop() {
	c.cancel()
}

// send queues a raw message for transmission. It returns false if the
// client is stopping or the queue is full; either way the message is simply
// dropped, since MMCP has no acknowledgements.
func (c *wsClient) send(data []byte) bool {
	if data == nil {
		return false
	}
	select {
	case <-c.ctx.Done():
		return false
	case c.sendCh <- data:
		return true
	}
}

func (c *wsClient) loop(program *tea.Program) {
	for {
		if err := c.serve(program); err != nil {
			select {
			case <-c.ctx.Done():
			default:
				program.Send(wsErrorMsg{err: err})
			}
		}

		program.Send(wsDisconnectedMsg{})

		select {
		case <-c.ctx.Done():
			return
		case <-time.After(reconnectDelay):
		}
	}
}

func (c *wsClient) serve(program *tea.Program) error {
	u, err := url.Parse(c.relay)
	if err != nil {
		return fmt.Errorf("invalid relay URL: %w", err)
	}

	conn, _, err := websocket.DefaultDialer.DialContext(c.ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	program.Send(wsConnectedMsg{})

	// Discover all currently connected providers with a broadcast INFO.
	// Responses from every provider are expected and tolerated.
	if data, ok := encodeControl("*", "INFO"); ok {
		_ = conn.WriteMessage(websocket.TextMessage, data)
	}

	errCh := make(chan error, 2)

	// Writer.
	go func() {
		for {
			select {
			case <-c.ctx.Done():
				errCh <- nil
				return
			case data := <-c.sendCh:
				if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
					errCh <- err
					return
				}
			}
		}
	}()

	// Reader.
	go func() {
		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			if msgType != websocket.TextMessage {
				continue
			}
			program.Send(wsDataMsg{data: append([]byte(nil), data...)})
		}
	}()

	return <-errCh
}
