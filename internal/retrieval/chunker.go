package retrieval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

type ChunkConfig struct {
	MaxRunes               int  `json:"max_runes"`
	OverlapRunes           int  `json:"overlap_runes"`
	PreserveHeadingContext bool `json:"preserve_heading_context"`
}

type Unit struct {
	ID      string
	Ordinal int
	Kind    string
	Text    string
}

type UnitRef struct {
	UnitID    string `json:"unit_id"`
	StartRune int    `json:"start_rune"`
	EndRune   int    `json:"end_rune"`
}

type Chunk struct {
	ID             string    `json:"id"`
	IndexVersionID string    `json:"index_version_id"`
	RevisionID     string    `json:"revision_id"`
	Ordinal        int       `json:"ordinal"`
	Text           string    `json:"text"`
	UnitRefs       []UnitRef `json:"unit_refs"`
}

type segment struct {
	text string
	ref  UnitRef
	kind string
}

func BuildChunks(indexVersionID, revisionID string, units []Unit, config ChunkConfig) ([]Chunk, error) {
	if strings.TrimSpace(indexVersionID) == "" || strings.TrimSpace(revisionID) == "" ||
		config.MaxRunes < 1 || config.OverlapRunes < 0 || config.OverlapRunes >= config.MaxRunes || len(units) == 0 {
		return nil, errors.New("invalid versioned chunk configuration")
	}
	segments := make([]segment, 0, len(units))
	for index, unit := range units {
		if strings.TrimSpace(unit.ID) == "" || unit.Ordinal != index || strings.TrimSpace(unit.Text) == "" || !utf8.ValidString(unit.Text) {
			return nil, errors.New("invalid authoritative Evidence Unit")
		}
		runes := []rune(unit.Text)
		if len(runes) <= config.MaxRunes {
			segments = append(segments, segment{text: unit.Text, kind: unit.Kind, ref: UnitRef{UnitID: unit.ID, EndRune: len(runes)}})
			continue
		}
		step := config.MaxRunes - config.OverlapRunes
		for start := 0; start < len(runes); start += step {
			end := start + config.MaxRunes
			if end > len(runes) {
				end = len(runes)
			}
			segments = append(segments, segment{
				text: string(runes[start:end]), kind: unit.Kind,
				ref: UnitRef{UnitID: unit.ID, StartRune: start, EndRune: end},
			})
			if end == len(runes) {
				break
			}
		}
	}

	chunks := make([]Chunk, 0)
	current := make([]segment, 0)
	var lastHeading *segment
	emit := func() {
		if len(current) == 0 {
			return
		}
		chunk := chunkFromSegments(indexVersionID, revisionID, len(chunks), current)
		chunks = append(chunks, chunk)
	}
	for _, next := range segments {
		if len(current) > 0 && segmentsRunes(appendCopy(current, next)) > config.MaxRunes {
			emit()
			current = overlapTail(current, config.OverlapRunes)
			if config.PreserveHeadingContext && lastHeading != nil && !containsRef(current, lastHeading.ref) {
				candidate := append([]segment{*lastHeading}, current...)
				if segmentsRunes(appendCopy(candidate, next)) <= config.MaxRunes {
					current = candidate
				}
			}
			if segmentsRunes(appendCopy(current, next)) > config.MaxRunes {
				current = nil
			}
		}
		current = append(current, next)
		if next.kind == "heading" {
			copy := next
			lastHeading = &copy
		}
	}
	emit()
	if len(chunks) == 0 {
		return nil, errors.New("chunker produced no Retrieval Chunks")
	}
	return chunks, nil
}

func chunkFromSegments(indexVersionID, revisionID string, ordinal int, segments []segment) Chunk {
	texts := make([]string, 0, len(segments))
	refs := make([]UnitRef, 0, len(segments))
	for _, item := range segments {
		texts = append(texts, item.text)
		refs = append(refs, item.ref)
	}
	text := strings.Join(texts, "\n\n")
	identity, _ := json.Marshal(struct {
		IndexVersionID string    `json:"index_version_id"`
		RevisionID     string    `json:"revision_id"`
		Ordinal        int       `json:"ordinal"`
		Text           string    `json:"text"`
		Refs           []UnitRef `json:"refs"`
	}{indexVersionID, revisionID, ordinal, text, refs})
	digest := sha256.Sum256(identity)
	return Chunk{
		ID: "chunk_" + hex.EncodeToString(digest[:16]), IndexVersionID: indexVersionID,
		RevisionID: revisionID, Ordinal: ordinal, Text: text, UnitRefs: refs,
	}
}

func segmentsRunes(segments []segment) int {
	total := 0
	for index, item := range segments {
		if index > 0 {
			total += 2
		}
		total += utf8.RuneCountInString(item.text)
	}
	return total
}

func overlapTail(segments []segment, maxRunes int) []segment {
	if maxRunes == 0 {
		return nil
	}
	start := len(segments)
	for start > 0 {
		candidate := segments[start-1:]
		if segmentsRunes(candidate) > maxRunes {
			break
		}
		start--
	}
	return append([]segment(nil), segments[start:]...)
}

func appendCopy(segments []segment, next segment) []segment {
	copy := append([]segment(nil), segments...)
	return append(copy, next)
}

func containsRef(segments []segment, ref UnitRef) bool {
	for _, item := range segments {
		if item.ref == ref {
			return true
		}
	}
	return false
}

func (c Chunk) String() string {
	return fmt.Sprintf("%s[%d]", c.ID, c.Ordinal)
}
