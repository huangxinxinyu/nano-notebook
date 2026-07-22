package agent

import (
	"strings"
	"unicode"
)

const maxSourceMarkerOccurrences = 64

const sourceMarkerPrefix = "[source:"

func normalizeSourceMarkers(text string, allowed map[string]struct{}) (string, []string, int) {
	var normalized strings.Builder
	normalized.Grow(len(text))
	references := make([]string, 0)
	seen := make(map[string]struct{})
	discarded := 0
	occurrences := 0

	for len(text) > 0 {
		start := strings.Index(text, sourceMarkerPrefix)
		if start < 0 {
			normalized.WriteString(text)
			break
		}
		normalized.WriteString(text[:start])
		text = text[start:]
		end := strings.IndexByte(text, ']')
		if end < 0 {
			discarded++
			normalized.WriteString(strings.TrimPrefix(text, sourceMarkerPrefix))
			break
		}
		marker := text[:end+1]
		sourceID := text[len(sourceMarkerPrefix):end]
		text = text[end+1:]
		occurrences++
		_, isAllowed := allowed[sourceID]
		if occurrences > maxSourceMarkerOccurrences || !validMarkerSourceID(sourceID) || !isAllowed {
			discarded++
			continue
		}
		normalized.WriteString(marker)
		if _, duplicate := seen[sourceID]; duplicate {
			continue
		}
		seen[sourceID] = struct{}{}
		references = append(references, sourceID)
	}
	return normalized.String(), references, discarded
}

func validMarkerSourceID(sourceID string) bool {
	if sourceID == "" || len(sourceID) > 255 {
		return false
	}
	for _, character := range sourceID {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}
