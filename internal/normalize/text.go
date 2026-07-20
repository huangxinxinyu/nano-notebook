package normalize

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

type Input struct {
	SourceID           string
	ExtractionConfigID string
	Format             string
	Payload            []byte
}

type Artifact struct {
	SchemaVersion      string   `json:"schema_version"`
	SourceID           string   `json:"source_id"`
	ExtractionConfigID string   `json:"extraction_config_id"`
	Format             string   `json:"format"`
	Text               string   `json:"text"`
	Blocks             []Block  `json:"blocks"`
	Coverage           Coverage `json:"coverage"`
	SHA256             string   `json:"sha256"`
	CanonicalJSON      []byte   `json:"-"`
}

type Block struct {
	ID           string `json:"id"`
	Ordinal      int    `json:"ordinal"`
	Kind         string `json:"kind"`
	Text         string `json:"text"`
	StartRune    int    `json:"start_rune"`
	EndRune      int    `json:"end_rune"`
	HeadingLevel int    `json:"heading_level,omitempty"`
}

type Coverage struct {
	Status     string `json:"status"`
	TotalRunes int    `json:"total_runes"`
	Gaps       []Gap  `json:"gaps"`
}

type Gap struct {
	StartRune int    `json:"start_rune"`
	EndRune   int    `json:"end_rune"`
	Reason    string `json:"reason"`
}

func Text(input Input) (Artifact, error) {
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.ExtractionConfigID = strings.TrimSpace(input.ExtractionConfigID)
	input.Format = strings.TrimSpace(input.Format)
	if input.SourceID == "" || input.ExtractionConfigID == "" ||
		(input.Format != "txt" && input.Format != "markdown") || !utf8.Valid(input.Payload) {
		return Artifact{}, errors.New("invalid text normalization input")
	}
	text := string(input.Payload)
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if strings.TrimSpace(text) == "" {
		return Artifact{}, errors.New("text Source has no usable primary content")
	}
	blocks := splitBlocks(text, input.Format)
	if len(blocks) == 0 {
		return Artifact{}, errors.New("text Source has no evidence blocks")
	}
	artifact := Artifact{
		SchemaVersion: "nano.normalized-source.v1", SourceID: input.SourceID,
		ExtractionConfigID: input.ExtractionConfigID, Format: input.Format, Text: text, Blocks: blocks,
		Coverage: Coverage{Status: "complete", TotalRunes: utf8.RuneCountInString(text), Gaps: make([]Gap, 0)},
	}
	canonical, err := canonicalArtifact(artifact)
	if err != nil {
		return Artifact{}, err
	}
	digest := sha256.Sum256(canonical)
	artifact.SHA256 = hex.EncodeToString(digest[:])
	artifact.CanonicalJSON = canonical
	return artifact, nil
}

func canonicalArtifact(artifact Artifact) ([]byte, error) {
	return json.Marshal(struct {
		SchemaVersion      string   `json:"schema_version"`
		SourceID           string   `json:"source_id"`
		ExtractionConfigID string   `json:"extraction_config_id"`
		Format             string   `json:"format"`
		Text               string   `json:"text"`
		Blocks             []Block  `json:"blocks"`
		Coverage           Coverage `json:"coverage"`
	}{artifact.SchemaVersion, artifact.SourceID, artifact.ExtractionConfigID, artifact.Format, artifact.Text, artifact.Blocks, artifact.Coverage})
}

func Validate(artifact Artifact) error {
	if artifact.SchemaVersion != "nano.normalized-source.v1" || strings.TrimSpace(artifact.SourceID) == "" ||
		strings.TrimSpace(artifact.ExtractionConfigID) == "" || (artifact.Format != "txt" && artifact.Format != "markdown") ||
		!utf8.ValidString(artifact.Text) || len(artifact.Blocks) == 0 {
		return errors.New("invalid normalized artifact identity or primary content")
	}
	textRunes := []rune(artifact.Text)
	if artifact.Coverage.TotalRunes != len(textRunes) ||
		(artifact.Coverage.Status != "complete" && artifact.Coverage.Status != "partial") ||
		(artifact.Coverage.Status == "complete" && len(artifact.Coverage.Gaps) != 0) {
		return errors.New("invalid normalized artifact coverage")
	}
	previousEnd := 0
	for index, block := range artifact.Blocks {
		if block.Ordinal != index || block.ID != fmt.Sprintf("block_%06d", index+1) ||
			block.StartRune < previousEnd || block.EndRune <= block.StartRune || block.EndRune > len(textRunes) ||
			!utf8.ValidString(block.Text) || block.Text != string(textRunes[block.StartRune:block.EndRune]) ||
			!validBlockKind(block.Kind) ||
			(block.Kind == "heading" && (block.HeadingLevel < 1 || block.HeadingLevel > 6)) ||
			(block.Kind != "heading" && block.HeadingLevel != 0) {
			return errors.New("invalid normalized artifact block")
		}
		previousEnd = block.EndRune
	}
	previousEnd = 0
	for _, gap := range artifact.Coverage.Gaps {
		if gap.StartRune < previousEnd || gap.EndRune <= gap.StartRune || gap.EndRune > len(textRunes) || strings.TrimSpace(gap.Reason) == "" {
			return errors.New("invalid normalized artifact coverage gap")
		}
		previousEnd = gap.EndRune
	}
	canonical, err := canonicalArtifact(artifact)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canonical)
	if artifact.SHA256 != hex.EncodeToString(digest[:]) ||
		(len(artifact.CanonicalJSON) > 0 && !bytes.Equal(artifact.CanonicalJSON, canonical)) {
		return errors.New("normalized artifact checksum mismatch")
	}
	return nil
}

func validBlockKind(kind string) bool {
	switch kind {
	case "heading", "paragraph", "list", "code", "table":
		return true
	default:
		return false
	}
}

func splitBlocks(text, format string) []Block {
	runes := []rune(text)
	type line struct{ start, contentEnd, next int }
	lines := make([]line, 0)
	for start := 0; start < len(runes); {
		end := start
		for end < len(runes) && runes[end] != '\n' {
			end++
		}
		next := end
		if next < len(runes) {
			next++
		}
		lines = append(lines, line{start: start, contentEnd: end, next: next})
		start = next
	}
	blocks := make([]Block, 0)
	for index := 0; index < len(lines); {
		if strings.TrimSpace(string(runes[lines[index].start:lines[index].contentEnd])) == "" {
			index++
			continue
		}
		start := lines[index].start
		end := lines[index].contentEnd
		cursor := index + 1
		for cursor < len(lines) {
			lineText := string(runes[lines[cursor].start:lines[cursor].contentEnd])
			if strings.TrimSpace(lineText) == "" {
				break
			}
			end = lines[cursor].contentEnd
			cursor++
		}
		blockText := string(runes[start:end])
		kind, level := classifyBlock(blockText, format)
		blocks = append(blocks, Block{
			ID: fmt.Sprintf("block_%06d", len(blocks)+1), Ordinal: len(blocks), Kind: kind,
			Text: blockText, StartRune: start, EndRune: end, HeadingLevel: level,
		})
		index = cursor
	}
	return blocks
}

func classifyBlock(text, format string) (string, int) {
	if format != "markdown" {
		return "paragraph", 0
	}
	firstLine := text
	if newline := strings.IndexByte(firstLine, '\n'); newline >= 0 {
		firstLine = firstLine[:newline]
	}
	trimmed := strings.TrimSpace(firstLine)
	for level := 6; level >= 1; level-- {
		if strings.HasPrefix(trimmed, strings.Repeat("#", level)+" ") {
			return "heading", level
		}
	}
	lines := strings.Split(text, "\n")
	allList := len(lines) > 0
	for _, candidate := range lines {
		candidate = strings.TrimSpace(candidate)
		if !(strings.HasPrefix(candidate, "- ") || strings.HasPrefix(candidate, "* ") || strings.HasPrefix(candidate, "+ ") || numberedListItem(candidate)) {
			allList = false
			break
		}
	}
	if allList {
		return "list", 0
	}
	if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
		return "code", 0
	}
	if len(lines) >= 2 && strings.Contains(lines[0], "|") && strings.Contains(lines[1], "---") {
		return "table", 0
	}
	return "paragraph", 0
}

func numberedListItem(value string) bool {
	dot := strings.IndexByte(value, '.')
	if dot < 1 || dot+1 >= len(value) || value[dot+1] != ' ' {
		return false
	}
	for _, character := range value[:dot] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
