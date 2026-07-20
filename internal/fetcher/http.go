package fetcher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const fetchPath = "/internal/source-fetcher/v1/fetch"

type SnapshotFetcher interface {
	Fetch(context.Context, string) (Snapshot, error)
}

func NewHTTPHandler(fetcher SnapshotFetcher) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeFetcherJSON(w, http.StatusOK, map[string]any{"status": "live", "service": "source-fetcher"})
	})
	mux.HandleFunc(fetchPath, func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writeFetcherError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if fetcher == nil {
			writeFetcherError(w, http.StatusServiceUnavailable, "fetcher_unavailable")
			return
		}
		var body struct {
			URL string `json:"url"`
		}
		decoder := json.NewDecoder(io.LimitReader(request.Body, 16*1024))
		if err := decoder.Decode(&body); err != nil || strings.TrimSpace(body.URL) == "" {
			writeFetcherError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		snapshot, err := fetcher.Fetch(request.Context(), body.URL)
		if err != nil {
			writeFetcherFetchError(w, err)
			return
		}
		digest := sha256.Sum256(snapshot.Payload)
		if snapshot.FinalURL == "" || snapshot.MediaType == "" ||
			!strings.EqualFold(snapshot.ContentSHA256, hex.EncodeToString(digest[:])) {
			writeFetcherError(w, http.StatusInternalServerError, "invalid_snapshot")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Nano-Final-URL", snapshot.FinalURL)
		w.Header().Set("X-Nano-Media-Type", snapshot.MediaType)
		w.Header().Set("X-Nano-Content-SHA256", strings.ToLower(snapshot.ContentSHA256))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(snapshot.Payload)
	})
	return mux
}

type RemoteClient struct {
	endpoint         string
	httpClient       *http.Client
	maxResponseBytes int64
}

func NewRemoteClient(endpoint string, httpClient *http.Client, maxResponseBytes int64) (*RemoteClient, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(endpoint), "/"))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || httpClient == nil || maxResponseBytes <= 0 {
		return nil, errors.New("invalid remote Fetcher configuration")
	}
	return &RemoteClient{endpoint: parsed.String(), httpClient: httpClient, maxResponseBytes: maxResponseBytes}, nil
}

func (c *RemoteClient) Fetch(ctx context.Context, rawURL string) (Snapshot, error) {
	payload, err := json.Marshal(struct {
		URL string `json:"url"`
	}{URL: rawURL})
	if err != nil {
		return Snapshot{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+fetchPath, bytes.NewReader(payload))
	if err != nil {
		return Snapshot{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return Snapshot{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Snapshot{}, mapRemoteError(response)
	}
	body, err := readBounded(response.Body, c.maxResponseBytes)
	if err != nil {
		return Snapshot{}, err
	}
	finalURL := response.Header.Get("X-Nano-Final-URL")
	mediaType := response.Header.Get("X-Nano-Media-Type")
	wantHash := strings.ToLower(strings.TrimSpace(response.Header.Get("X-Nano-Content-SHA256")))
	digest := sha256.Sum256(body)
	gotHash := hex.EncodeToString(digest[:])
	if finalURL == "" || !supportedMediaType(mediaType) || wantHash != gotHash {
		return Snapshot{}, errors.New("remote Fetcher returned invalid snapshot proof")
	}
	return Snapshot{FinalURL: finalURL, MediaType: mediaType, Payload: body, ContentSHA256: gotHash}, nil
}

func mapRemoteError(response *http.Response) error {
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 16*1024)).Decode(&body)
	switch body.Error.Code {
	case "unsafe_destination":
		return ErrUnsafeDestination
	case "response_too_large":
		return ErrResponseTooLarge
	case "unsupported_type":
		return ErrUnsupportedType
	default:
		return errors.New("remote Fetcher unavailable")
	}
}

func writeFetcherFetchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrUnsafeDestination):
		writeFetcherError(w, http.StatusUnprocessableEntity, "unsafe_destination")
	case errors.Is(err, ErrResponseTooLarge):
		writeFetcherError(w, http.StatusRequestEntityTooLarge, "response_too_large")
	case errors.Is(err, ErrUnsupportedType):
		writeFetcherError(w, http.StatusUnsupportedMediaType, "unsupported_type")
	default:
		writeFetcherError(w, http.StatusBadGateway, "upstream_failed")
	}
}

func writeFetcherError(w http.ResponseWriter, status int, code string) {
	writeFetcherJSON(w, status, map[string]any{"error": map[string]string{"code": code}})
}

func writeFetcherJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
