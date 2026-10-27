package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type shutdownStub struct {
	shutdown func(context.Context) error
	close    func() error
}

func (s shutdownStub) Shutdown(ctx context.Context) error { return s.shutdown(ctx) }
func (s shutdownStub) Close() error                       { return s.close() }

func TestStopHTTPDeadlineAndForceClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		closed := false
		closeErr := errors.New("close failed")
		started := time.Now()
		err := stopHTTP(shutdownStub{
			shutdown: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
			close:    func() error { closed = true; return closeErr },
		})
		if !closed || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, closeErr) {
			t.Fatalf("closed %v, error %v", closed, err)
		}
		if elapsed := time.Since(started); elapsed != 10*time.Second {
			t.Errorf("shutdown took %v, want 10s", elapsed)
		}
	})
}
func TestStopHTTPSuccess(t *testing.T) {
	err := stopHTTP(shutdownStub{
		shutdown: func(ctx context.Context) error {
			if ctx.Err() != nil {
				t.Fatal("shutdown context is already canceled")
			}
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 10*time.Second {
				t.Error("missing ten-second deadline")
			}
			return nil
		},
		close: func() error { t.Error("forced close during graceful shutdown"); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
}

type trackedListener struct {
	net.Listener
	closed chan struct{}
	once   sync.Once
}

func (l *trackedListener) Close() error {
	err := l.Listener.Close()
	l.once.Do(func() { close(l.closed) })
	return err
}

func TestServeHTTPFinishesInflightRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tracked := &trackedListener{Listener: listener, closed: make(chan struct{})}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	ctx, cancel := context.WithCancel(context.Background())
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		if r.Context().Err() != nil {
			t.Error("signal canceled an in-flight request")
		}
		_, _ = w.Write([]byte("complete"))
	})}
	t.Cleanup(func() { releaseHandler(); cancel(); _ = server.Close() })
	done := make(chan error, 1)
	go func() { done <- serveHTTP(ctx, server, tracked) }()
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	requestDone := make(chan error, 1)
	go func() {
		reply, err := client.Get("http://" + listener.Addr().String())
		if err != nil {
			requestDone <- err
			return
		}
		defer reply.Body.Close()
		body, err := io.ReadAll(reply.Body)
		if err == nil && (reply.StatusCode != 200 || string(body) != "complete") {
			err = errors.New("in-flight response was incomplete")
		}
		requestDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case <-tracked.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not close listener")
	}
	select {
	case err := <-done:
		t.Fatalf("server returned before request finished: %v", err)
	default:
	}
	connection, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err == nil {
		_ = connection.Close()
		t.Error("new connection accepted during shutdown")
	}
	releaseHandler()
	select {
	case err := <-requestDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request did not finish")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not finish shutdown")
	}
}

func TestServeHTTPReportsListenerFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := serveHTTP(ctx, &http.Server{}, listener); err == nil {
		t.Fatal("listener failure was ignored")
	}
}
