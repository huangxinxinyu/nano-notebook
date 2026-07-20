package fetcher

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrUnsafeDestination = errors.New("unsafe fetch destination")
	ErrResponseTooLarge  = errors.New("fetch response exceeds budget")
	ErrUnsupportedType   = errors.New("unsupported fetch response type")
)

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type Config struct {
	Resolver           Resolver
	DialContext        func(context.Context, string, string) (net.Conn, error)
	MaxRedirects       int
	MaxCompressedBytes int64
	MaxExpandedBytes   int64
	Timeout            time.Duration
}

type Snapshot struct {
	FinalURL      string
	MediaType     string
	Payload       []byte
	ContentSHA256 string
}

type Fetcher struct {
	client             *http.Client
	resolver           Resolver
	dialContext        func(context.Context, string, string) (net.Conn, error)
	maxCompressedBytes int64
	maxExpandedBytes   int64
	maxRedirects       int
}

func New(config Config) *Fetcher {
	if config.Resolver == nil {
		config.Resolver = net.DefaultResolver
	}
	if config.DialContext == nil {
		dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
		config.DialContext = dialer.DialContext
	}
	if config.MaxRedirects <= 0 {
		config.MaxRedirects = 5
	}
	if config.MaxCompressedBytes <= 0 {
		config.MaxCompressedBytes = 20 * 1024 * 1024
	}
	if config.MaxExpandedBytes <= 0 {
		config.MaxExpandedBytes = 50 * 1024 * 1024
	}
	if config.Timeout <= 0 {
		config.Timeout = 20 * time.Second
	}
	fetcher := &Fetcher{
		resolver: config.Resolver, dialContext: config.DialContext,
		maxCompressedBytes: config.MaxCompressedBytes, maxExpandedBytes: config.MaxExpandedBytes,
		maxRedirects: config.MaxRedirects,
	}
	transport := &http.Transport{
		Proxy: nil, DisableCompression: true, ForceAttemptHTTP2: true,
		DialContext:         fetcher.dialValidated,
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConns: 8, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second,
	}
	fetcher.client = &http.Client{
		Transport: transport, Timeout: config.Timeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= fetcher.maxRedirects {
				return errors.New("fetch redirect limit reached")
			}
			return validateURL(request.URL)
		},
	}
	return fetcher
}

func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (Snapshot, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || validateURL(parsed) != nil {
		return Snapshot{}, ErrUnsafeDestination
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Snapshot{}, err
	}
	request.Header.Set("Accept", strings.Join(supportedMediaTypes, ", "))
	request.Header.Set("Accept-Encoding", "gzip")
	request.Header.Set("User-Agent", "Nano-Notebook-Fetcher/1")
	response, err := f.client.Do(request)
	if err != nil {
		return Snapshot{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Snapshot{}, fmt.Errorf("fetch response status %d", response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !supportedMediaType(mediaType) {
		return Snapshot{}, ErrUnsupportedType
	}
	compressed, err := readBounded(response.Body, f.maxCompressedBytes)
	if err != nil {
		return Snapshot{}, err
	}
	expanded := compressed
	switch strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			return Snapshot{}, errors.New("invalid gzip response")
		}
		expanded, err = readBounded(reader, f.maxExpandedBytes)
		_ = reader.Close()
		if err != nil {
			return Snapshot{}, err
		}
	default:
		return Snapshot{}, errors.New("unsupported content encoding")
	}
	if int64(len(expanded)) > f.maxExpandedBytes {
		return Snapshot{}, ErrResponseTooLarge
	}
	digest := sha256.Sum256(expanded)
	return Snapshot{
		FinalURL: response.Request.URL.String(), MediaType: strings.ToLower(mediaType), Payload: expanded,
		ContentSHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func (f *Fetcher) dialValidated(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrUnsafeDestination
	}
	addresses, err := f.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, fmt.Errorf("resolve fetch destination: %w", err)
	}
	for _, candidate := range addresses {
		if !IsPublicAddress(candidate) {
			return nil, ErrUnsafeDestination
		}
	}
	selected := net.JoinHostPort(addresses[0].Unmap().String(), port)
	return f.dialContext(ctx, network, selected)
}

func validateURL(candidate *url.URL) error {
	if candidate == nil || (candidate.Scheme != "http" && candidate.Scheme != "https") ||
		candidate.Hostname() == "" || candidate.User != nil || candidate.Fragment != "" {
		return ErrUnsafeDestination
	}
	port := candidate.Port()
	if port != "" {
		parsed, err := strconv.Atoi(port)
		if err != nil || parsed < 1 || parsed > 65535 {
			return ErrUnsafeDestination
		}
	}
	return nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > limit {
		return nil, ErrResponseTooLarge
	}
	return payload, nil
}

var supportedMediaTypes = []string{
	"text/html", "text/plain", "text/markdown", "application/pdf",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"audio/mpeg", "audio/wav", "audio/x-wav", "audio/mp4", "audio/x-m4a",
	"image/png", "image/jpeg", "image/webp",
}

func supportedMediaType(candidate string) bool {
	for _, mediaType := range supportedMediaTypes {
		if strings.EqualFold(candidate, mediaType) {
			return true
		}
	}
	return false
}

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"), netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("ff00::/8"),
}

func IsPublicAddress(address netip.Addr) bool {
	if !address.IsValid() {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Addr().BitLen() == address.BitLen() && prefix.Contains(address) {
			return false
		}
	}
	return true
}
