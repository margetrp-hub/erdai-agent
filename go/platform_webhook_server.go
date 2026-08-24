package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"
)

type platformWebhookServer struct {
	server   *http.Server
	listener net.Listener
	done     chan struct{}
}

func startPlatformWebhookServer(ctx context.Context, host string, port int, handler http.Handler) (*platformWebhookServer, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10,
	}
	value := &platformWebhookServer{server: server, listener: listener, done: make(chan struct{})}
	go func() {
		defer close(value.done)
		_ = server.Serve(listener)
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	return value, nil
}

func (s *platformWebhookServer) Close() error {
	if s == nil || s.server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.server.Shutdown(ctx)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *platformWebhookServer) Address() string {
	if s == nil || s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}
