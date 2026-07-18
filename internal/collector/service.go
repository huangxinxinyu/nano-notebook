package collector

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

type HTTPServiceConfig struct {
	Handler           http.Handler
	ReadHeaderTimeout time.Duration
	ShutdownTimeout   time.Duration
}

type HTTPService struct {
	server          *http.Server
	shutdownTimeout time.Duration
}

func NewHTTPService(config HTTPServiceConfig) (*HTTPService, error) {
	if config.Handler == nil || config.ReadHeaderTimeout <= 0 || config.ShutdownTimeout <= 0 {
		return nil, errors.New("Collector HTTP Service configuration is incomplete")
	}
	return &HTTPService{
		server: &http.Server{
			Handler:           config.Handler,
			ReadHeaderTimeout: config.ReadHeaderTimeout,
		},
		shutdownTimeout: config.ShutdownTimeout,
	}, nil
}

func (s *HTTPService) Run(ctx context.Context, listener net.Listener) error {
	if s == nil || s.server == nil || listener == nil {
		return errors.New("nil Collector HTTP Service")
	}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- s.server.Serve(listener)
	}()
	select {
	case err := <-serveDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer cancel()
	shutdownErr := s.server.Shutdown(shutdownCtx)
	serveErr := <-serveDone
	if shutdownErr != nil {
		_ = s.server.Close()
		return fmt.Errorf("drain Collector HTTP Service: %w", shutdownErr)
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}
