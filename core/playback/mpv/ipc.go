package mpv

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

const (
	ipcCallTimeout = 2 * time.Second
	maxIPCMessage  = 32 * 1024 * 1024
)

type ipcEvent struct {
	Name   string
	Reason string
	ID     int64
	Data   any
}

type ipcReply struct {
	status string
	data   any
}

type ipcMessage struct {
	RequestID *int64 `json:"request_id"`
	Status    string `json:"error"`
	Data      any    `json:"data"`
	Event     string `json:"event"`
	Reason    string `json:"reason"`
	ID        int64  `json:"id"`
}

type ipcClient struct {
	conn    net.Conn
	write   sync.Mutex
	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan ipcReply
	events  chan ipcEvent
	done    chan struct{}
	err     error
	closed  bool
}

func dialIPC(ctx context.Context, socket string) (*ipcClient, error) {
	dialer := net.Dialer{Timeout: ipcCallTimeout}
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("connect to mpv IPC: %w", err)
	}
	client := &ipcClient{
		conn:    conn,
		pending: make(map[int64]chan ipcReply),
		events:  make(chan ipcEvent, 256),
		done:    make(chan struct{}),
	}
	go client.readLoop()
	return client, nil
}

func (c *ipcClient) Call(ctx context.Context, command ...any) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, ipcCallTimeout)
	defer cancel()

	select {
	case <-c.done:
		return nil, c.connectionError()
	default:
	}

	c.mu.Lock()
	c.nextID++
	id := c.nextID
	result := make(chan ipcReply, 1)
	c.pending[id] = result
	c.mu.Unlock()

	if err := c.writeCommand(ctx, id, command); err != nil {
		c.removePending(id)
		return nil, err
	}

	select {
	case reply := <-result:
		return decodeIPCReply(reply)
	case <-ctx.Done():
		c.removePending(id)
		return nil, ctx.Err()
	case <-c.done:
		select {
		case reply := <-result:
			return decodeIPCReply(reply)
		default:
			c.removePending(id)
			return nil, c.connectionError()
		}
	}
}

func decodeIPCReply(reply ipcReply) (any, error) {
	if reply.status != "success" {
		return nil, fmt.Errorf("mpv error: %s", reply.status)
	}
	return reply.data, nil
}

func (c *ipcClient) writeCommand(ctx context.Context, id int64, command []any) error {
	c.write.Lock()
	defer c.write.Unlock()

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(ipcCallTimeout)
	}
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	defer c.conn.SetWriteDeadline(time.Time{})

	request := struct {
		Command   []any `json:"command"`
		RequestID int64 `json:"request_id"`
	}{Command: command, RequestID: id}
	if err := json.NewEncoder(c.conn).Encode(request); err != nil {
		return fmt.Errorf("write mpv IPC command: %w", err)
	}
	return nil
}

func (c *ipcClient) readLoop() {
	scanner := bufio.NewScanner(c.conn)
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), maxIPCMessage)
	for scanner.Scan() {
		var message ipcMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			c.finish(fmt.Errorf("decode mpv IPC message: %w", err))
			return
		}
		if message.Event != "" {
			select {
			case c.events <- ipcEvent{Name: message.Event, Reason: message.Reason, ID: message.ID, Data: message.Data}:
			default:
				c.finish(errors.New("mpv IPC event queue exhausted"))
				return
			}
			continue
		}
		if message.RequestID == nil {
			continue
		}
		c.mu.Lock()
		result := c.pending[*message.RequestID]
		delete(c.pending, *message.RequestID)
		c.mu.Unlock()
		if result != nil {
			result <- ipcReply{status: message.Status, data: message.Data}
		}
	}

	err := scanner.Err()
	if err == nil {
		err = net.ErrClosed
	}
	c.finish(fmt.Errorf("mpv IPC transport closed: %w", err))
}

func (c *ipcClient) finish(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.err = err
	close(c.done)
	close(c.events)
	c.mu.Unlock()
	_ = c.conn.Close()
}

func (c *ipcClient) removePending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *ipcClient) connectionError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	return net.ErrClosed
}

func (c *ipcClient) Events() <-chan ipcEvent {
	return c.events
}

func (c *ipcClient) Done() <-chan struct{} {
	return c.done
}

func (c *ipcClient) Err() error {
	select {
	case <-c.done:
		return c.connectionError()
	default:
		return nil
	}
}

func (c *ipcClient) Close() error {
	return c.conn.Close()
}
