package fetcher_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/fetcher"
)

func TestRemoteClientRoundTripsOneFetcherSnapshot(t *testing.T) {
	stub := &stubSnapshotFetcher{snapshot: fetcher.Snapshot{
		FinalURL: "https://example.com/final", MediaType: "text/html",
		Payload: []byte("<main>snapshot</main>"), ContentSHA256: "1d8b6439fa5f755c18eae3bbb5cb4d188fa6bd6af5e13c34b9075bc1eec59800",
	}}
	server := httptest.NewServer(fetcher.NewHTTPHandler(stub))
	defer server.Close()
	client, err := fetcher.NewRemoteClient(server.URL, http.DefaultClient, 1024)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.Fetch(context.Background(), "https://example.com/start")
	if err != nil {
		t.Fatalf("remote Fetch: %v", err)
	}
	if stub.rawURL != "https://example.com/start" || snapshot.FinalURL != stub.snapshot.FinalURL ||
		snapshot.MediaType != stub.snapshot.MediaType || string(snapshot.Payload) != string(stub.snapshot.Payload) ||
		snapshot.ContentSHA256 != stub.snapshot.ContentSHA256 {
		t.Fatalf("snapshot=%+v stub=%+v requested=%q", snapshot, stub.snapshot, stub.rawURL)
	}
}

func TestRemoteClientPreservesTypedFetcherRejections(t *testing.T) {
	server := httptest.NewServer(fetcher.NewHTTPHandler(&stubSnapshotFetcher{err: fetcher.ErrUnsafeDestination}))
	defer server.Close()
	client, err := fetcher.NewRemoteClient(server.URL, http.DefaultClient, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Fetch(context.Background(), "http://127.0.0.1/"); !errors.Is(err, fetcher.ErrUnsafeDestination) {
		t.Fatalf("remote rejection = %v, want unsafe destination", err)
	}
}

type stubSnapshotFetcher struct {
	snapshot fetcher.Snapshot
	err      error
	rawURL   string
}

func (s *stubSnapshotFetcher) Fetch(_ context.Context, rawURL string) (fetcher.Snapshot, error) {
	s.rawURL = rawURL
	return s.snapshot, s.err
}
