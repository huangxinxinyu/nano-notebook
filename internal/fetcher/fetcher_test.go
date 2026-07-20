package fetcher_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/fetcher"
)

func TestPublicAddressPolicyBlocksPrivateReservedAndMappedRanges(t *testing.T) {
	tests := []struct {
		address string
		public  bool
	}{
		{"8.8.8.8", true}, {"2606:4700:4700::1111", true},
		{"127.0.0.1", false}, {"10.1.2.3", false}, {"169.254.169.254", false},
		{"100.64.0.1", false}, {"192.0.2.1", false}, {"198.18.0.1", false},
		{"203.0.113.9", false}, {"224.0.0.1", false}, {"240.0.0.1", false},
		{"::1", false}, {"fc00::1", false}, {"fe80::1", false}, {"2001:db8::1", false},
		{"::ffff:127.0.0.1", false}, {"64:ff9b:1::1", false}, {"ff02::1", false},
	}
	for _, test := range tests {
		address := netip.MustParseAddr(test.address)
		if got := fetcher.IsPublicAddress(address); got != test.public {
			t.Errorf("IsPublicAddress(%s)=%v, want %v", address, got, test.public)
		}
	}
}

func TestFetcherRevalidatesRedirectDestinationsBeforeDial(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "http://private.test/secret", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("secret"))
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	resolver := staticResolver{addresses: map[string][]netip.Addr{
		"public.test":  {netip.MustParseAddr("93.184.216.34")},
		"private.test": {netip.MustParseAddr("127.0.0.1")},
	}}
	dialed := make([]string, 0)
	client := fetcher.New(fetcher.Config{
		Resolver: resolver,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, upstreamURL.Host)
		},
		MaxRedirects: 3, MaxCompressedBytes: 1024, MaxExpandedBytes: 2048, Timeout: time.Second,
	})
	_, err = client.Fetch(context.Background(), "http://public.test/start")
	if !errors.Is(err, fetcher.ErrUnsafeDestination) {
		t.Fatalf("Fetch redirect error = %v, want unsafe destination", err)
	}
	if len(dialed) != 1 || strings.Contains(dialed[0], "127.0.0.1") {
		t.Fatalf("dialed destinations = %v; private redirect must not be dialed", dialed)
	}
}

func TestFetcherRejectsMixedDNSAnswersAndExpandedBodies(t *testing.T) {
	resolver := staticResolver{addresses: map[string][]netip.Addr{
		"mixed.test": {netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.8")},
	}}
	dialCalls := 0
	client := fetcher.New(fetcher.Config{
		Resolver: resolver,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			dialCalls++
			return nil, errors.New("must not dial")
		},
		Timeout: time.Second,
	})
	if _, err := client.Fetch(context.Background(), "http://mixed.test/"); !errors.Is(err, fetcher.ErrUnsafeDestination) {
		t.Fatalf("mixed DNS Fetch error = %v, want unsafe destination", err)
	}
	if dialCalls != 0 {
		t.Fatalf("mixed DNS dial calls = %d, want 0", dialCalls)
	}

	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write(bytes.Repeat([]byte("x"), 4096))
	_ = writer.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed.Bytes())
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	client = fetcher.New(fetcher.Config{
		Resolver: staticResolver{addresses: map[string][]netip.Addr{"public.test": {netip.MustParseAddr("93.184.216.34")}}},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, upstreamURL.Host)
		},
		MaxCompressedBytes: 1024, MaxExpandedBytes: 512, Timeout: time.Second,
	})
	if _, err := client.Fetch(context.Background(), "http://public.test/"); !errors.Is(err, fetcher.ErrResponseTooLarge) {
		t.Fatalf("expanded Fetch error = %v, want response too large", err)
	}
}

func TestFetcherReturnsAnImmutableSupportedSnapshot(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<main>public evidence</main>"))
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := fetcher.New(fetcher.Config{
		Resolver: staticResolver{addresses: map[string][]netip.Addr{"public.test": {netip.MustParseAddr("93.184.216.34")}}},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, upstreamURL.Host)
		},
		MaxCompressedBytes: 1024, MaxExpandedBytes: 2048, Timeout: time.Second,
	})
	snapshot, err := client.Fetch(context.Background(), "http://public.test/article")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snapshot.FinalURL != "http://public.test/article" || snapshot.MediaType != "text/html" ||
		string(snapshot.Payload) != "<main>public evidence</main>" || len(snapshot.ContentSHA256) != 64 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	for _, rawURL := range []string{"file:///etc/passwd", "ftp://public.test/file", "http://user:pass@public.test/"} {
		if _, err := client.Fetch(context.Background(), rawURL); !errors.Is(err, fetcher.ErrUnsafeDestination) {
			t.Errorf("Fetch(%q) error = %v, want unsafe destination", rawURL, err)
		}
	}
}

type staticResolver struct {
	addresses map[string][]netip.Addr
}

func (r staticResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	addresses, ok := r.addresses[host]
	if !ok {
		return nil, errors.New("host not found")
	}
	return append([]netip.Addr(nil), addresses...), nil
}
