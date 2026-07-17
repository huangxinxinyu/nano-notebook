package memory_test

import (
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/exportertest"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/memory"
)

func TestExporterConformance(t *testing.T) {
	exportertest.Run(t, exportertest.Harness{
		New: func(t *testing.T) agentobs.Exporter {
			t.Helper()
			return memory.New()
		},
		Records: func(t *testing.T, exporter agentobs.Exporter, traceID agentobs.TraceID) []agentobs.Record {
			t.Helper()
			memoryExporter := exporter.(*memory.Exporter)
			var records []agentobs.Record
			for _, record := range memoryExporter.Records() {
				if record.TraceID == traceID {
					records = append(records, record)
				}
			}
			return records
		},
	})
}
