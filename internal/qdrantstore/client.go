package qdrantstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
)

const maxResponseBytes = 4 << 20

type Config struct {
	BaseURL         string
	APIKey          string
	Collection      string
	DenseDimensions int
	RequestTimeout  time.Duration
	HTTPClient      *http.Client
}

type EvidenceRef struct {
	SourceID   string
	RevisionID string
}

type Scope struct {
	NotebookID     string
	IndexVersionID string
	Evidence       []EvidenceRef
}

type Point struct {
	ChunkID        string
	NotebookID     string
	SourceID       string
	RevisionID     string
	IndexVersionID string
	UnitIDs        []string
	Dense          []float32
	Sparse         retrieval.SparseVector
	Checksum       string
}

type CollectionDetails struct {
	DenseDimensions int
	SparseModifier  string
	PayloadIndexes  map[string]string
}

type Client struct {
	baseURL         string
	apiKey          string
	collection      string
	denseDimensions int
	httpClient      *http.Client
}

type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("Qdrant status %d: %s", e.StatusCode, e.Body)
}

func New(config Config) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		strings.TrimSpace(config.Collection) == "" || strings.Contains(config.Collection, "/") || config.DenseDimensions <= 0 {
		return nil, errors.New("invalid Qdrant Store configuration")
	}
	timeout := config.RequestTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	return &Client{
		baseURL: strings.TrimRight(parsed.String(), "/"), apiKey: strings.TrimSpace(config.APIKey),
		collection: config.Collection, denseDimensions: config.DenseDimensions, httpClient: httpClient,
	}, nil
}

func (c *Client) EnsureCollection(ctx context.Context) error {
	_, err := c.CollectionDetails(ctx)
	var httpErr *HTTPError
	if err != nil && (!errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusNotFound) {
		return err
	}
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
		body := map[string]any{
			"vectors":        map[string]any{"dense": map[string]any{"size": c.denseDimensions, "distance": "Cosine"}},
			"sparse_vectors": map[string]any{"bm25": map[string]any{"modifier": "idf", "index": map[string]any{"on_disk": false}}},
			"strict_mode_config": map[string]any{
				"enabled": true, "max_query_limit": 200,
				"unindexed_filtering_retrieve": false, "unindexed_filtering_update": false,
			},
		}
		if err := c.doJSON(ctx, http.MethodPut, c.collectionPath(), body, nil); err != nil {
			return err
		}
	}
	for _, field := range []string{"notebook_id", "source_id", "revision_id", "index_version_id"} {
		if err := c.doJSON(ctx, http.MethodPut, c.collectionPath()+"/index?wait=true", map[string]any{
			"field_name": field, "field_schema": "keyword",
		}, nil); err != nil {
			return err
		}
	}
	details, err := c.CollectionDetails(ctx)
	if err != nil {
		return err
	}
	if details.DenseDimensions != c.denseDimensions {
		return fmt.Errorf("Qdrant dense dimension mismatch: got %d, want %d", details.DenseDimensions, c.denseDimensions)
	}
	if details.SparseModifier != "idf" {
		return errors.New("Qdrant BM25 sparse vector must use the IDF modifier")
	}
	for _, field := range []string{"notebook_id", "source_id", "revision_id", "index_version_id"} {
		if details.PayloadIndexes[field] != "keyword" {
			return fmt.Errorf("Qdrant payload index %q is missing", field)
		}
	}
	return nil
}

func (c *Client) CollectionDetails(ctx context.Context) (CollectionDetails, error) {
	var response struct {
		Result struct {
			Config struct {
				Params struct {
					Vectors map[string]struct {
						Size int `json:"size"`
					} `json:"vectors"`
					SparseVectors map[string]json.RawMessage `json:"sparse_vectors"`
				} `json:"params"`
			} `json:"config"`
			PayloadSchema map[string]json.RawMessage `json:"payload_schema"`
		} `json:"result"`
	}
	if err := c.doJSON(ctx, http.MethodGet, c.collectionPath(), nil, &response); err != nil {
		return CollectionDetails{}, err
	}
	dense, ok := response.Result.Config.Params.Vectors["dense"]
	if !ok {
		return CollectionDetails{}, errors.New("Qdrant dense named vector is missing")
	}
	rawSparse, ok := response.Result.Config.Params.SparseVectors["bm25"]
	if !ok {
		return CollectionDetails{}, errors.New("Qdrant BM25 named sparse vector is missing")
	}
	details := CollectionDetails{DenseDimensions: dense.Size, PayloadIndexes: make(map[string]string)}
	var sparseConfig struct {
		Modifier string `json:"modifier"`
	}
	if err := json.Unmarshal(rawSparse, &sparseConfig); err != nil {
		return CollectionDetails{}, errors.New("invalid Qdrant sparse vector configuration")
	}
	details.SparseModifier = strings.ToLower(sparseConfig.Modifier)
	for field, raw := range response.Result.PayloadSchema {
		var scalar string
		if json.Unmarshal(raw, &scalar) == nil {
			details.PayloadIndexes[field] = strings.ToLower(scalar)
			continue
		}
		var object struct {
			DataType string `json:"data_type"`
		}
		if json.Unmarshal(raw, &object) == nil {
			details.PayloadIndexes[field] = strings.ToLower(object.DataType)
		}
	}
	return details, nil
}

func (c *Client) Upsert(ctx context.Context, points []Point) error {
	if len(points) == 0 || len(points) > 256 {
		return errors.New("invalid Qdrant point batch")
	}
	encoded := make([]map[string]any, 0, len(points))
	seen := make(map[string]struct{}, len(points))
	for _, point := range points {
		if err := c.validatePoint(point); err != nil {
			return err
		}
		identity := point.IndexVersionID + "\x00" + point.ChunkID
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(identity)).String()
		if _, duplicate := seen[id]; duplicate {
			return errors.New("duplicate Qdrant point identity")
		}
		seen[id] = struct{}{}
		encoded = append(encoded, map[string]any{
			"id":     id,
			"vector": map[string]any{"dense": point.Dense, "bm25": point.Sparse},
			"payload": map[string]any{
				"chunk_id": point.ChunkID, "notebook_id": point.NotebookID, "source_id": point.SourceID,
				"revision_id": point.RevisionID, "index_version_id": point.IndexVersionID,
				"unit_ids": point.UnitIDs, "checksum": point.Checksum,
			},
		})
	}
	return c.doJSON(ctx, http.MethodPut, c.collectionPath()+"/points?wait=true", map[string]any{"points": encoded}, nil)
}

func (c *Client) SearchDense(ctx context.Context, vector []float32, scope Scope, limit int) ([]retrieval.Candidate, error) {
	if len(vector) != c.denseDimensions || !finiteVector(vector) {
		return nil, errors.New("invalid dense query vector")
	}
	return c.search(ctx, vector, "dense", scope, limit)
}

func (c *Client) SearchSparse(ctx context.Context, vector retrieval.SparseVector, scope Scope, limit int) ([]retrieval.Candidate, error) {
	if err := validateSparse(vector); err != nil {
		return nil, err
	}
	return c.search(ctx, vector, "bm25", scope, limit)
}

func (c *Client) search(ctx context.Context, query any, vectorName string, scope Scope, limit int) ([]retrieval.Candidate, error) {
	filter, pairs, err := buildFilter(scope)
	if err != nil || limit < 1 || limit > 200 {
		return nil, errors.New("invalid scoped Qdrant search")
	}
	var response queryResponse
	err = c.doJSON(ctx, http.MethodPost, c.collectionPath()+"/points/query", map[string]any{
		"query": query, "using": vectorName, "filter": filter, "limit": limit,
		"with_payload": []string{"chunk_id", "notebook_id", "source_id", "revision_id", "index_version_id"},
		"with_vector":  false,
	}, &response)
	if err != nil {
		return nil, err
	}
	result := make([]retrieval.Candidate, 0, len(response.Result.Points))
	seen := make(map[string]struct{}, len(response.Result.Points))
	for _, point := range response.Result.Points {
		if point.Payload.NotebookID != scope.NotebookID || point.Payload.IndexVersionID != scope.IndexVersionID ||
			!pairs[point.Payload.SourceID+"\x00"+point.Payload.RevisionID] || strings.TrimSpace(point.Payload.ChunkID) == "" ||
			math.IsNaN(point.Score) || math.IsInf(point.Score, 0) {
			return nil, errors.New("Qdrant returned a forged or out-of-scope point")
		}
		if _, duplicate := seen[point.Payload.ChunkID]; duplicate {
			return nil, errors.New("Qdrant returned a duplicate chunk identity")
		}
		seen[point.Payload.ChunkID] = struct{}{}
		result = append(result, retrieval.Candidate{ID: point.Payload.ChunkID, Score: point.Score})
	}
	return result, nil
}

func (c *Client) Count(ctx context.Context, scope Scope) (int, error) {
	filter, _, err := buildFilter(scope)
	if err != nil {
		return 0, err
	}
	var response struct {
		Result struct {
			Count int `json:"count"`
		} `json:"result"`
	}
	if err := c.doJSON(ctx, http.MethodPost, c.collectionPath()+"/points/count", map[string]any{"filter": filter, "exact": true}, &response); err != nil {
		return 0, err
	}
	return response.Result.Count, nil
}

func (c *Client) DeleteScope(ctx context.Context, scope Scope) error {
	filter, _, err := buildFilter(scope)
	if err != nil {
		return err
	}
	return c.doJSON(ctx, http.MethodPost, c.collectionPath()+"/points/delete?wait=true", map[string]any{"filter": filter}, nil)
}

func (c *Client) DeleteCollection(ctx context.Context) error {
	err := c.doJSON(ctx, http.MethodDelete, c.collectionPath(), nil, nil)
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

type queryResponse struct {
	Result struct {
		Points []struct {
			Score   float64 `json:"score"`
			Payload struct {
				ChunkID        string `json:"chunk_id"`
				NotebookID     string `json:"notebook_id"`
				SourceID       string `json:"source_id"`
				RevisionID     string `json:"revision_id"`
				IndexVersionID string `json:"index_version_id"`
			} `json:"payload"`
		} `json:"points"`
	} `json:"result"`
}

func (c *Client) validatePoint(point Point) error {
	if strings.TrimSpace(point.ChunkID) == "" || strings.TrimSpace(point.NotebookID) == "" || strings.TrimSpace(point.SourceID) == "" ||
		strings.TrimSpace(point.RevisionID) == "" || strings.TrimSpace(point.IndexVersionID) == "" || strings.TrimSpace(point.Checksum) == "" ||
		len(point.UnitIDs) == 0 || len(point.Dense) != c.denseDimensions || !finiteVector(point.Dense) {
		return errors.New("invalid Qdrant projection point")
	}
	for _, unitID := range point.UnitIDs {
		if strings.TrimSpace(unitID) == "" {
			return errors.New("invalid Qdrant projection point")
		}
	}
	return validateSparse(point.Sparse)
}

func validateSparse(vector retrieval.SparseVector) error {
	if len(vector.Indices) == 0 || len(vector.Indices) != len(vector.Values) {
		return errors.New("invalid sparse vector")
	}
	for index, value := range vector.Values {
		if value == 0 || math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) ||
			(index > 0 && vector.Indices[index] <= vector.Indices[index-1]) {
			return errors.New("invalid sparse vector")
		}
	}
	return nil
}

func finiteVector(vector []float32) bool {
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
	}
	return true
}

func buildFilter(scope Scope) (map[string]any, map[string]bool, error) {
	if strings.TrimSpace(scope.NotebookID) == "" || strings.TrimSpace(scope.IndexVersionID) == "" || len(scope.Evidence) == 0 {
		return nil, nil, errors.New("invalid Qdrant Retrieval Scope")
	}
	evidence := append([]EvidenceRef(nil), scope.Evidence...)
	sort.Slice(evidence, func(i, j int) bool {
		if evidence[i].SourceID == evidence[j].SourceID {
			return evidence[i].RevisionID < evidence[j].RevisionID
		}
		return evidence[i].SourceID < evidence[j].SourceID
	})
	conditions := make([]any, 0, len(evidence))
	pairs := make(map[string]bool, len(evidence))
	for _, ref := range evidence {
		key := strings.TrimSpace(ref.SourceID) + "\x00" + strings.TrimSpace(ref.RevisionID)
		if strings.TrimSpace(ref.SourceID) == "" || strings.TrimSpace(ref.RevisionID) == "" || pairs[key] {
			return nil, nil, errors.New("invalid Qdrant Retrieval Scope")
		}
		pairs[key] = true
		conditions = append(conditions, map[string]any{"must": []any{
			fieldMatch("source_id", ref.SourceID), fieldMatch("revision_id", ref.RevisionID),
		}})
	}
	return map[string]any{
		"must":       []any{fieldMatch("notebook_id", scope.NotebookID), fieldMatch("index_version_id", scope.IndexVersionID)},
		"min_should": map[string]any{"conditions": conditions, "min_count": 1},
	}, pairs, nil
}

func fieldMatch(key, value string) map[string]any {
	return map[string]any{"key": key, "match": map[string]any{"value": value}}
}

func (c *Client) collectionPath() string {
	return "/collections/" + url.PathEscape(c.collection)
}

func (c *Client) doJSON(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		request.Header.Set("api-key", c.apiKey)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > maxResponseBytes {
		return errors.New("Qdrant response exceeds limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &HTTPError{StatusCode: response.StatusCode, Body: strings.TrimSpace(string(payload))}
	}
	if output != nil {
		if len(payload) == 0 || json.Unmarshal(payload, output) != nil {
			return errors.New("invalid Qdrant response")
		}
	}
	return nil
}
