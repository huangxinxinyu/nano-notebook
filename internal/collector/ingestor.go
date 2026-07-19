package collector

import (
	"context"
	"errors"
	"strings"
)

var ErrInvalidBatch = errors.New("Collector Batch envelope is invalid")

type IngestorConfig struct {
	ProducerID       string
	ProducerIDPrefix string
	Store            Store
}

type Ingestor struct {
	producerID       string
	producerIDPrefix string
	store            Store
}

func NewIngestor(config IngestorConfig) (*Ingestor, error) {
	config.ProducerID = strings.TrimSpace(config.ProducerID)
	config.ProducerIDPrefix = strings.TrimSpace(config.ProducerIDPrefix)
	if (config.ProducerID == "" && config.ProducerIDPrefix == "") || config.Store == nil {
		return nil, errors.New("Collector Ingestor configuration is incomplete")
	}
	return &Ingestor{
		producerID: config.ProducerID, producerIDPrefix: config.ProducerIDPrefix, store: config.Store,
	}, nil
}

func (i *Ingestor) Ingest(ctx context.Context, batch Batch) (BatchResult, error) {
	if i == nil || i.store == nil {
		return BatchResult{}, errors.New("nil Collector Ingestor")
	}
	producerAllowed := (i.producerID != "" && batch.ProducerID == i.producerID) ||
		(i.producerIDPrefix != "" && strings.HasPrefix(batch.ProducerID, i.producerIDPrefix))
	if (batch.ProtocolVersion != ProtocolVersion && batch.ProtocolVersion != DirectProtocolVersion) || strings.TrimSpace(batch.BatchID) == "" || !producerAllowed || !validDescriptorText(batch.ProducerID, 160) || batch.CreatedAt.IsZero() || len(batch.Chunks) == 0 {
		return BatchResult{}, ErrInvalidBatch
	}
	result := BatchResult{BatchID: batch.BatchID, Chunks: make([]ChunkResult, 0, len(batch.Chunks))}
	for _, chunk := range batch.Chunks {
		if batch.ProtocolVersion == DirectProtocolVersion && chunk.SequenceAuthority != SequenceAuthorityCollector {
			result.Chunks = append(result.Chunks, ChunkResult{
				TraceID: chunk.Trace.TraceID, Status: ChunkRejected, Code: CodeInvalidChunk,
			})
			continue
		}
		if batch.ProtocolVersion == ProtocolVersion && chunk.SequenceAuthority != "" && chunk.SequenceAuthority != SequenceAuthorityProducer {
			result.Chunks = append(result.Chunks, ChunkResult{
				TraceID: chunk.Trace.TraceID, Status: ChunkRejected, Code: CodeInvalidChunk,
			})
			continue
		}
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
