package retrieval

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"sort"
)

type SparseEncoder struct {
	analyzer  MixedAnalyzer
	k1        float64
	b         float64
	averageDL float64
}

func NewSparseEncoder(analyzer MixedAnalyzer, k1, b, averageDocumentLength float64) (*SparseEncoder, error) {
	if analyzer.ID() == "" || k1 <= 0 || math.IsNaN(k1) || math.IsInf(k1, 0) || b < 0 || b > 1 ||
		math.IsNaN(b) || math.IsInf(b, 0) || averageDocumentLength <= 0 || math.IsNaN(averageDocumentLength) || math.IsInf(averageDocumentLength, 0) {
		return nil, errors.New("invalid sparse BM25 encoder configuration")
	}
	return &SparseEncoder{analyzer: analyzer, k1: k1, b: b, averageDL: averageDocumentLength}, nil
}

func (e *SparseEncoder) Document(text string) (SparseVector, error) {
	tokens := e.analyzer.Analyze(text)
	if len(tokens) == 0 {
		return SparseVector{}, errors.New("BM25 document has no analyzable terms")
	}
	frequencies := make(map[string]int)
	for _, token := range tokens {
		frequencies[token]++
	}
	items, err := e.items(frequencies, func(frequency int) float64 {
		numerator := float64(frequency) * (e.k1 + 1)
		denominator := float64(frequency) + e.k1*(1-e.b+e.b*float64(len(tokens))/e.averageDL)
		return numerator / denominator
	})
	if err != nil {
		return SparseVector{}, err
	}
	return sparseItems(items), nil
}

func (e *SparseEncoder) Query(text string) (SparseVector, error) {
	tokens := e.analyzer.Analyze(text)
	if len(tokens) == 0 {
		return SparseVector{}, errors.New("BM25 query has no analyzable terms")
	}
	terms := make(map[string]int)
	for _, token := range tokens {
		terms[token] = 1
	}
	items, err := e.items(terms, func(int) float64 { return 1 })
	if err != nil {
		return SparseVector{}, err
	}
	return sparseItems(items), nil
}

func (e *SparseEncoder) items(terms map[string]int, weight func(int) float64) ([]sparseItem, error) {
	items := make([]sparseItem, 0, len(terms))
	identities := make(map[uint32]string, len(terms))
	for term, frequency := range terms {
		digest := sha256.Sum256([]byte(e.analyzer.ID() + "\x00" + term))
		index := binary.BigEndian.Uint32(digest[:4])
		if existing, collision := identities[index]; collision && existing != term {
			return nil, errors.New("BM25 term identity collision")
		}
		identities[index] = term
		items = append(items, sparseItem{index: index, value: float32(weight(frequency))})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].index < items[j].index })
	return items, nil
}
