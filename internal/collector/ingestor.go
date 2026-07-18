package collector

import (
	"context"
	"errors"
	"strings"
)

var ErrInvalidBatch = errors.New("Collector Batch envelope is invalid")

type IngestorConfig struct {
	ProducerID string
	Store      Store
}

type Ingestor struct {
	producerID string
	store      Store
}

func NewIngestor(config IngestorConfig) (*Ingestor, error) {
	if strings.TrimSpace(config.ProducerID) == "" || config.Store == nil {
		return nil, errors.New("Collector Ingestor configuration is incomplete")
	}
	return &Ingestor{producerID: config.ProducerID, store: config.Store}, nil
}

func (i *Ingestor) Ingest(ctx context.Context, batch Batch) (BatchResult, error) {
	if i == nil || i.store == nil {
		return BatchResult{}, errors.New("nil Collector Ingestor")
	}
	if batch.ProtocolVersion != ProtocolVersion || strings.TrimSpace(batch.BatchID) == "" || batch.ProducerID != i.producerID || batch.CreatedAt.IsZero() || len(batch.Chunks) == 0 {
		return BatchResult{}, ErrInvalidBatch
	}
	result := BatchResult{BatchID: batch.BatchID, Chunks: make([]ChunkResult, 0, len(batch.Chunks))}
	for _, chunk := range batch.Chunks {
		if chunk.Trace.SchemaVersion != SupportedRecordSchemaVersion || chunk.Trace.SemanticConventionVersion != SupportedSemanticConvention {
			result.Chunks = append(result.Chunks, ChunkResult{
				TraceID: chunk.Trace.TraceID, Status: ChunkRejected, Code: CodeUnsupportedSchema,
			})
			continue
		}
		committedThrough, err := i.store.CommitTraceChunk(ctx, chunk)
		if err != nil {
			var chunkErr *ChunkError
			if errors.As(err, &chunkErr) {
				status := ChunkRejected
				if chunkErr.Retryable {
					status = ChunkRetryable
				}
				result.Chunks = append(result.Chunks, ChunkResult{
					TraceID: chunk.Trace.TraceID, Status: status,
					CommittedThrough: chunkErr.CommittedThrough, Code: chunkErr.Code,
				})
				continue
			}
			return BatchResult{}, err
		}
		result.Chunks = append(result.Chunks, ChunkResult{
			TraceID: chunk.Trace.TraceID, Status: ChunkCommitted, CommittedThrough: committedThrough,
		})
	}
	return result, nil
}
