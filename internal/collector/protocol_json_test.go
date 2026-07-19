package collector_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/collector"
)

func TestBatchJSONUsesExplicitSnakeCaseCanonicalRecordContract(t *testing.T) {
	batch := validCollectorBatch(t)
	encoded, err := json.Marshal(batch)
	if err != nil {
		t.Fatalf("Marshal Batch: %v", err)
	}
	text := string(encoded)
	for _, required := range []string{
		`"protocol_version":1`, `"batch_id":"batch-1"`, `"producer_id":"nano-worker"`,
		`"first_sequence":1`, `"identity_key":"run/run-1/root/start"`,
		`"canonical_payload":{"semantic_convention_version":1,"attributes":[]}`,
		`"canonical_sha256":`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Batch JSON missing %s: %s", required, text)
		}
	}
	for _, forbidden := range []string{`"ProtocolVersion"`, `"Record"`, `"Attributes"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Batch JSON contains Go-internal field %s: %s", forbidden, text)
		}
	}

	var decoded collector.Batch
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal Batch: %v", err)
	}
	if decoded.BatchID != batch.BatchID || len(decoded.Chunks) != 1 || len(decoded.Chunks[0].Records) != 2 {
		t.Fatalf("decoded Batch = %#v", decoded)
	}
	for index := range batch.Chunks[0].Records {
		want, err := batch.Chunks[0].Records[index].Record.CanonicalHash()
		if err != nil {
			t.Fatalf("want CanonicalHash: %v", err)
		}
		got, err := decoded.Chunks[0].Records[index].Record.CanonicalHash()
		if err != nil {
			t.Fatalf("got CanonicalHash: %v", err)
		}
		if got != want {
			t.Fatalf("record %d hash changed across JSON", index)
		}
	}
}

func TestDirectBatchJSONDeclaresCollectorSequenceAuthority(t *testing.T) {
	batch := directCollectorBatch(t)
	encoded, err := json.Marshal(batch)
	if err != nil {
		t.Fatalf("Marshal Batch: %v", err)
	}
	text := string(encoded)
	for _, required := range []string{
		`"protocol_version":2`, `"sequence_authority":"collector"`, `"first_sequence":0`, `"sequence":0`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("direct Batch JSON missing %s: %s", required, text)
		}
	}

	var decoded collector.Batch
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal Batch: %v", err)
	}
	if decoded.ProtocolVersion != collector.DirectProtocolVersion ||
		decoded.Chunks[0].SequenceAuthority != collector.SequenceAuthorityCollector ||
		decoded.Chunks[0].FirstSequence != 0 || decoded.Chunks[0].Records[0].Sequence != 0 {
		t.Fatalf("decoded direct Batch = %#v", decoded)
	}
}
