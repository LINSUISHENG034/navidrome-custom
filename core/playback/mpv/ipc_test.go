package mpv

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestIPCClientMatchesOutOfOrderResponsesToRequests(t *testing.T) {
	listener := listenIPC(t)
	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		decoder := json.NewDecoder(conn)
		requests := make([]struct {
			Command   []any `json:"command"`
			RequestID int64 `json:"request_id"`
		}, 2)
		for i := range requests {
			if err := decoder.Decode(&requests[i]); err != nil {
				serverErr <- err
				return
			}
		}
		encoder := json.NewEncoder(conn)
		for i := len(requests) - 1; i >= 0; i-- {
			if len(requests[i].Command) != 2 {
				serverErr <- errors.New("unexpected IPC command shape")
				return
			}
			if err := encoder.Encode(map[string]any{
				"request_id": requests[i].RequestID,
				"error":      "success",
				"data":       requests[i].Command[1],
			}); err != nil {
				serverErr <- err
				return
			}
		}
		serverErr <- nil
	}()

	client, err := dialIPC(context.Background(), listener.Addr().String())
	if err != nil {
		t.Fatalf("dial IPC: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	results := make(chan struct {
		name string
		data any
		err  error
	}, 2)
	for _, name := range []string{"first", "second"} {
		go func(name string) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			data, err := client.Call(ctx, "get_property", name)
			results <- struct {
				name string
				data any
				err  error
			}{name: name, data: data, err: err}
		}(name)
	}

	for range 2 {
		result := <-results
		if result.err != nil || result.data != result.name {
			t.Fatalf("response for %q: data=%v, err=%v", result.name, result.data, result.err)
		}
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("IPC server: %v", err)
	}
}

func TestIPCClientHonorsCallDeadlineWithoutReply(t *testing.T) {
	listener := listenIPC(t)
	serverErr := make(chan error, 1)
	holdConnection := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		var request map[string]any
		if err := json.NewDecoder(conn).Decode(&request); err != nil {
			serverErr <- err
			return
		}
		close(holdConnection)
		_, _ = conn.Read(make([]byte, 1))
		serverErr <- nil
	}()

	client, err := dialIPC(context.Background(), listener.Addr().String())
	if err != nil {
		t.Fatalf("dial IPC: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = client.Call(ctx, "get_property", "pause")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected bounded deadline error, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("IPC call exceeded its deadline: %v", elapsed)
	}
	select {
	case <-holdConnection:
	case <-time.After(time.Second):
		t.Fatal("IPC server did not receive the request")
	}
	_ = client.Close()
	if err := <-serverErr; err != nil {
		t.Fatalf("IPC server: %v", err)
	}
}

func listenIPC(t *testing.T) net.Listener {
	t.Helper()
	return listenIPCAt(t, filepath.Join(t.TempDir(), "mpv.socket"))
}

func listenIPCAt(t *testing.T, socket string) net.Listener {
	t.Helper()
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen IPC: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}
