package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// Stop the listener and maintenance, then wait for admitted HTTP requests and
// the store's bounded writer queue. Forced termination can still lose queued
// work; no success is sent before SQLite commits it.
func serveUntilStopped(ctx context.Context, listener net.Listener, server *http.Server, maintenance func(), closeStore func() error, timeout time.Duration) error {
	maintenanceCtx, stopMaintenance := context.WithCancel(context.Background())
	maintenanceDone := make(chan struct{})
	go func() {
		defer close(maintenanceDone)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-maintenanceCtx.Done():
				return
			case <-ticker.C:
				maintenance()
			}
		}
	}()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-serverDone:
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
	}
	stopMaintenance()
	drainCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	shutdownErr := server.Shutdown(drainCtx)
	if shutdownErr != nil {
		_ = server.Close()
	}
	select {
	case <-maintenanceDone:
	case <-drainCtx.Done():
		return errors.Join(serveErr, shutdownErr, drainCtx.Err())
	}
	if closeStore != nil {
		closed := make(chan error, 1)
		go func() { closed <- closeStore() }()
		select {
		case err := <-closed:
			return errors.Join(serveErr, shutdownErr, err)
		case <-drainCtx.Done():
			return errors.Join(serveErr, shutdownErr, drainCtx.Err())
		}
	}
	return errors.Join(serveErr, shutdownErr)
}
