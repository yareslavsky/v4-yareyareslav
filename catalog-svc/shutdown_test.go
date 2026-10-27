package main

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

func TestStopConsumerWaitsBeforeClosing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		done := make(chan struct{})
		go func() {
			time.Sleep(4 * time.Second)
			close(done)
		}()
		reader := closerFunc(func() error {
			select {
			case <-done:
			default:
				t.Error("reader closed before consumer stopped")
			}
			time.Sleep(2 * time.Second)
			return nil
		})
		writer := closerFunc(func() error {
			select {
			case <-done:
			default:
				t.Error("writer closed before consumer stopped")
			}
			time.Sleep(3 * time.Second)
			return nil
		})
		started := time.Now()
		stopped, err := stopConsumer(ctx, done, reader, writer)
		if !stopped || err != nil {
			t.Fatalf("got (%v, %v), want successful shutdown", stopped, err)
		}
		if elapsed := time.Since(started); elapsed != 7*time.Second {
			t.Fatalf("shutdown took %v, want 7s", elapsed)
		}
	})
}

func TestStopConsumerTimeoutWhileProcessing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		closer := closerFunc(func() error {
			t.Error("resource closed while consumer still processing")
			return nil
		})
		started := time.Now()
		stopped, err := stopConsumer(ctx, make(chan struct{}), closer, closer)
		if stopped || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got (%v, %v), want processing timeout", stopped, err)
		}
		if elapsed := time.Since(started); elapsed != 10*time.Second {
			t.Fatalf("shutdown took %v, want 10s", elapsed)
		}
	})
}

func TestStopConsumerTimeoutIncludesClose(t *testing.T) {
	for _, blocked := range []string{"reader", "writer"} {
		t.Run(blocked, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				done := make(chan struct{})
				go func() {
					time.Sleep(4 * time.Second)
					close(done)
				}()
				release := make(chan struct{})
				readerClosed, writerClosed := make(chan struct{}), make(chan struct{})
				reader := closerFunc(func() error {
					close(readerClosed)
					if blocked == "reader" {
						<-release
					}
					return nil
				})
				writer := closerFunc(func() error {
					close(writerClosed)
					if blocked == "writer" {
						<-release
					}
					return nil
				})
				started := time.Now()
				stopped, err := stopConsumer(ctx, done, reader, writer)
				close(release)
				synctest.Wait()
				if !stopped || !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("got (%v, %v), want closing timeout", stopped, err)
				}
				if elapsed := time.Since(started); elapsed != 10*time.Second {
					t.Fatalf("shutdown took %v, want a shared 10s budget", elapsed)
				}
				select {
				case <-readerClosed:
				default:
					t.Fatal("reader close not attempted")
				}
				select {
				case <-writerClosed:
				default:
					t.Fatal("writer close not attempted")
				}
			})
		})
	}
}

func TestStopConsumerCloseErrors(t *testing.T) {
	done := make(chan struct{})
	close(done)
	readerErr, writerErr := errors.New("reader failed"), errors.New("writer failed")
	stopped, err := stopConsumer(context.Background(), done,
		closerFunc(func() error { return readerErr }),
		closerFunc(func() error { return writerErr }),
	)
	if !stopped || !errors.Is(err, readerErr) || !errors.Is(err, writerErr) {
		t.Fatalf("got (%v, %v), want both closing errors", stopped, err)
	}
}
