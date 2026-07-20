package fetcher

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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
	if videoID, ok := youtubeVideoID(parsed); ok {
		return f.fetchYouTubeCaptions(ctx, parsed, videoID)
	}
	return f.fetchURL(ctx, parsed, supportedMediaType)
}

func (f *Fetcher) fetchURL(ctx context.Context, parsed *url.URL, allowedType func(string) bool) (Snapshot, error) {
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
	if err != nil || !allowedType(mediaType) {
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

type youtubeCaptionSegment struct {
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Text    string `json:"text"`
}

func (f *Fetcher) fetchYouTubeCaptions(ctx context.Context, parsed *url.URL, videoID string) (Snapshot, error) {
	watch, err := f.fetchURL(ctx, parsed, func(mediaType string) bool { return strings.EqualFold(mediaType, "text/html") })
	if err != nil {
		return Snapshot{}, err
	}
	watchFinal, err := url.Parse(watch.FinalURL)
	finalVideoID, finalIsYouTube := youtubeVideoID(watchFinal)
	if err != nil || !finalIsYouTube || finalVideoID != videoID {
		return Snapshot{}, ErrUnsafeDestination
	}
	baseURL, language, err := youtubeCaptionTrack(watch.Payload)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: %v", ErrUnsupportedType, err)
	}
	captionURL, err := url.Parse(baseURL)
	if err != nil || validateURL(captionURL) != nil || !youtubeCaptionHost(captionURL.Hostname()) {
		return Snapshot{}, ErrUnsafeDestination
	}
	query := captionURL.Query()
	query.Set("fmt", "json3")
	captionURL.RawQuery = query.Encode()
	caption, err := f.fetchURL(ctx, captionURL, func(mediaType string) bool {
		return strings.EqualFold(mediaType, "application/json") || strings.EqualFold(mediaType, "text/plain")
	})
	if err != nil {
		return Snapshot{}, err
	}
	captionFinal, err := url.Parse(caption.FinalURL)
	if err != nil || !youtubeMediaHost(captionFinal.Hostname()) {
		return Snapshot{}, ErrUnsafeDestination
	}
	segments, err := parseYouTubeCaptionEvents(caption.Payload)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: %v", ErrUnsupportedType, err)
	}
	payload, err := json.Marshal(struct {
		SchemaVersion string                  `json:"schema_version"`
		VideoID       string                  `json:"video_id"`
		Language      string                  `json:"language"`
		Segments      []youtubeCaptionSegment `json:"segments"`
	}{"nano.youtube-captions.v1", videoID, language, segments})
	if err != nil {
		return Snapshot{}, err
	}
	digest := sha256.Sum256(payload)
	return Snapshot{
		FinalURL: watch.FinalURL, MediaType: "application/vnd.nano.youtube-captions+json", Payload: payload,
		ContentSHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func youtubeVideoID(candidate *url.URL) (string, bool) {
	if candidate == nil {
		return "", false
	}
	host := strings.ToLower(candidate.Hostname())
	var id string
	switch host {
	case "youtube.com", "www.youtube.com", "m.youtube.com":
		if candidate.Path == "/watch" {
			id = candidate.Query().Get("v")
		} else if strings.HasPrefix(candidate.Path, "/shorts/") {
			id = strings.TrimPrefix(candidate.Path, "/shorts/")
		}
	case "youtu.be", "www.youtu.be":
		id = strings.TrimPrefix(candidate.Path, "/")
	}
	if len(id) != 11 || strings.Contains(id, "/") {
		return "", false
	}
	for _, character := range id {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-') {
			return "", false
		}
	}
	return id, true
}

func youtubeCaptionTrack(payload []byte) (string, string, error) {
	marker := []byte(`"captionTracks":`)
	index := bytes.Index(payload, marker)
	if index < 0 {
		return "", "", errors.New("YouTube video has no usable captions")
	}
	var tracks []struct {
		BaseURL      string `json:"baseUrl"`
		LanguageCode string `json:"languageCode"`
		Kind         string `json:"kind"`
	}
	if err := json.NewDecoder(bytes.NewReader(payload[index+len(marker):])).Decode(&tracks); err != nil || len(tracks) == 0 {
		return "", "", errors.New("invalid YouTube caption tracks")
	}
	selected := tracks[0]
	for _, track := range tracks {
		if strings.TrimSpace(track.Kind) != "asr" {
			selected = track
			break
		}
	}
	if strings.TrimSpace(selected.BaseURL) == "" || strings.TrimSpace(selected.LanguageCode) == "" ||
		len(selected.LanguageCode) > 35 || !utf8.ValidString(selected.LanguageCode) {
		return "", "", errors.New("invalid YouTube caption track")
	}
	return selected.BaseURL, selected.LanguageCode, nil
}

func parseYouTubeCaptionEvents(payload []byte) ([]youtubeCaptionSegment, error) {
	var decoded struct {
		Events []struct {
			StartMS    int64 `json:"tStartMs"`
			DurationMS int64 `json:"dDurationMs"`
			Segments   []struct {
				Text string `json:"utf8"`
			} `json:"segs"`
		} `json:"events"`
	}
	if !utf8.Valid(payload) || json.Unmarshal(payload, &decoded) != nil || len(decoded.Events) == 0 || len(decoded.Events) > 10_000 {
		return nil, errors.New("invalid YouTube caption response")
	}
	segments := make([]youtubeCaptionSegment, 0, len(decoded.Events))
	for _, event := range decoded.Events {
		var text strings.Builder
		for _, segment := range event.Segments {
			text.WriteString(segment.Text)
		}
		value := strings.Join(strings.Fields(text.String()), " ")
		if value == "" {
			continue
		}
		if utf8.RuneCountInString(value) > 8_000 {
			return nil, errors.New("YouTube caption event exceeds processing budget")
		}
		if event.StartMS < 0 || event.DurationMS <= 0 || event.StartMS > math.MaxInt64-event.DurationMS {
			return nil, errors.New("invalid YouTube caption interval")
		}
		segments = append(segments, youtubeCaptionSegment{StartMS: event.StartMS, EndMS: event.StartMS + event.DurationMS, Text: value})
	}
	sort.SliceStable(segments, func(left, right int) bool {
		if segments[left].StartMS != segments[right].StartMS {
			return segments[left].StartMS < segments[right].StartMS
		}
		return segments[left].Text < segments[right].Text
	})
	if len(segments) == 0 {
		return nil, errors.New("YouTube video has no usable caption events")
	}
	return segments, nil
}

func youtubeCaptionHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "youtube.com" || strings.HasSuffix(host, ".youtube.com")
}

func youtubeMediaHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return youtubeCaptionHost(host) || host == "googlevideo.com" || strings.HasSuffix(host, ".googlevideo.com")
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
	"application/vnd.nano.youtube-captions+json",
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
