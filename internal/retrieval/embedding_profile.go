package retrieval

import (
	"errors"
	"strings"
)

const EmbeddingProfileGeminiRetrievalV1 = "gemini-retrieval-v1"

var ErrEmbeddingProfileInvalid = errors.New("embedding profile is invalid")

func IsEmbeddingProfileID(profileID string) bool {
	return strings.TrimSpace(profileID) == EmbeddingProfileGeminiRetrievalV1
}

func FormatEmbeddingQuery(profileID, query string) (string, error) {
	query = strings.TrimSpace(query)
	if !IsEmbeddingProfileID(profileID) || query == "" {
		return "", ErrEmbeddingProfileInvalid
	}
	return "task: search result | query: " + query, nil
}

func FormatEmbeddingDocument(profileID, title, text string) (string, error) {
	text = strings.TrimSpace(text)
	if !IsEmbeddingProfileID(profileID) || text == "" {
		return "", ErrEmbeddingProfileInvalid
	}
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		title = "none"
	}
	return "title: " + title + " | text: " + text, nil
}
