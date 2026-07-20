package normalize

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

func YouTube(input Input) (Artifact, error) {
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.ExtractionConfigID = strings.TrimSpace(input.ExtractionConfigID)
	input.Format = strings.TrimSpace(input.Format)
	if input.SourceID == "" || input.ExtractionConfigID == "" || input.Format != "youtube" || len(input.Payload) == 0 {
		return Artifact{}, errors.New("invalid YouTube normalization input")
	}
	var snapshot struct {
		SchemaVersion string `json:"schema_version"`
		VideoID       string `json:"video_id"`
		Language      string `json:"language"`
		Segments      []struct {
			StartMS int64  `json:"start_ms"`
			EndMS   int64  `json:"end_ms"`
			Text    string `json:"text"`
		} `json:"segments"`
	}
	decoder := json.NewDecoder(bytes.NewReader(input.Payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&snapshot) != nil || snapshot.SchemaVersion != "nano.youtube-captions.v1" ||
		!validYouTubeVideoID(snapshot.VideoID) || strings.TrimSpace(snapshot.Language) == "" || len(snapshot.Segments) == 0 {
		return Artifact{}, errors.New("invalid immutable YouTube caption snapshot")
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return Artifact{}, errors.New("invalid immutable YouTube caption snapshot")
	}
	segments := make([]TranscriptSegment, 0, len(snapshot.Segments))
	for _, segment := range snapshot.Segments {
		segments = append(segments, TranscriptSegment{StartMS: segment.StartMS, EndMS: segment.EndMS, Text: segment.Text})
	}
	return Transcript(input, segments)
}

func validYouTubeVideoID(value string) bool {
	if len(value) != 11 {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-') {
			return false
		}
	}
	return true
}
