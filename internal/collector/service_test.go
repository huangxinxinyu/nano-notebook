package collector_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/collector"
)

func TestHTTPServiceStopsAdmissionAndDrainsInflightRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/inflight" {
			close(started)
			<-release
		}
		w.WriteHeader(http.StatusOK)
	})
	service, err := collector.NewHTTPService(collector.HTTPServiceConfig{
		Handler: handler, ReadHeaderTimeout: time.Second, ShutdownTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewHTTPService: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- service.Run(ctx, listener) }()

	client := &http.Client{Timeout: 250 * time.Millisecond}
	inflightDone := make(chan error, 1)
	go func() {
		response, requestErr := client.Get("http://" + listener.Addr().String() + "/inflight")
		if requestErr == nil {
			_, requestErr = io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
		inflightDone <- requestErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("in-flight request did not reach handler")
	}

	cancel()
	admissionDeadline := time.Now().Add(time.Second)
	for {
		probeClient := &http.Client{Timeout: 50 * time.Millisecond}
		response, probeErr := probeClient.Get("http://" + listener.Addr().String() + "/probe")
		if probeErr != nil {
			break
		}
		response.Body.Close()
		if time.Now().After(admissionDeadline) {
			t.Fatal("Collector continued accepting requests after shutdown")
		}
	}
	select {
	case runErr := <-serveDone:
		t.Fatalf("service returned before in-flight request drained: %v", runErr)
	default:
	}

	close(release)
	if requestErr := <-inflightDone; requestErr != nil {
		t.Fatalf("in-flight request failed during drain: %v", requestErr)
	}
	select {
	case runErr := <-serveDone:
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			t.Fatalf("Run: %v", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("service did not stop after in-flight request drained")
	}
}
