package normalize_test

import (
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/normalize"
)

func TestYouTubeAdapterUsesOnlyImmutableCaptionSnapshotIntervals(t *testing.T) {
	payload := []byte(`{"schema_version":"nano.youtube-captions.v1","video_id":"dQw4w9WgXcQ","language":"en","segments":[{"start_ms":0,"end_ms":1250,"text":"First caption."},{"start_ms":1500,"end_ms":2500,"text":"Second caption."}]}`)
	artifact, err := normalize.YouTube(normalize.Input{
		SourceID: "src_youtube", ExtractionConfigID: "youtube-captions-v1", Format: "youtube", Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Text != "First caption.\n\nSecond caption." || len(artifact.Blocks) != 2 ||
		artifact.Blocks[1].Coordinate == nil || artifact.Blocks[1].Coordinate.Kind != "time_interval" ||
		artifact.Blocks[1].Coordinate.StartMS != 1500 {
		t.Fatalf("YouTube artifact=%+v", artifact)
	}
}

func TestYouTubeAdapterRejectsUnknownSnapshotAndMissingCaptions(t *testing.T) {
	for _, payload := range [][]byte{
		[]byte(`{"schema_version":"unknown","video_id":"dQw4w9WgXcQ","language":"en","segments":[{"start_ms":0,"end_ms":1,"text":"x"}]}`),
		[]byte(`{"schema_version":"nano.youtube-captions.v1","video_id":"dQw4w9WgXcQ","language":"en","segments":[]}`),
	} {
		if _, err := normalize.YouTube(normalize.Input{SourceID: "bad", ExtractionConfigID: "youtube-v1", Format: "youtube", Payload: payload}); err == nil {
			t.Fatal("YouTube accepted invalid caption snapshot")
		}
	}
}
