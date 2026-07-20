package retrieval

import (
	"errors"
	"math"
	"sort"
	"strings"
)

type Document struct {
	ID   string
	Text string
}

type Candidate struct {
	ID    string
	Score float64
}

type SparseVector struct {
	Indices []uint32  `json:"indices"`
	Values  []float32 `json:"values"`
}

type sparseItem struct {
	index uint32
	value float32
}

type bm25Document struct {
	id     string
	length int
	terms  map[string]int
}

type BM25Model struct {
	analyzer     MixedAnalyzer
	documents    []bm25Document
	byID         map[string]bm25Document
	vocabulary   map[string]uint32
	orderedVocab []string
	idf          map[string]float64
	averageDL    float64
	k1           float64
	b            float64
}

func BuildBM25(analyzer MixedAnalyzer, documents []Document, k1, b float64) (*BM25Model, error) {
	if analyzer.ID() == "" || len(documents) == 0 || k1 <= 0 || math.IsNaN(k1) || b < 0 || b > 1 || math.IsNaN(b) {
		return nil, errors.New("invalid BM25 configuration")
	}
	ordered := append([]Document(nil), documents...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	model := &BM25Model{
		analyzer: analyzer, documents: make([]bm25Document, 0, len(ordered)), byID: make(map[string]bm25Document),
		vocabulary: make(map[string]uint32), idf: make(map[string]float64), k1: k1, b: b,
	}
	documentFrequency := make(map[string]int)
	totalLength := 0
	for index, document := range ordered {
		if strings.TrimSpace(document.ID) == "" || strings.TrimSpace(document.Text) == "" ||
			(index > 0 && document.ID == ordered[index-1].ID) {
			return nil, errors.New("invalid BM25 document")
		}
		tokens := analyzer.Analyze(document.Text)
		if len(tokens) == 0 {
			return nil, errors.New("BM25 document has no analyzable terms")
		}
		terms := make(map[string]int)
		for _, token := range tokens {
			terms[token]++
		}
		for term := range terms {
			documentFrequency[term]++
		}
		item := bm25Document{id: document.ID, length: len(tokens), terms: terms}
		model.documents = append(model.documents, item)
		model.byID[item.id] = item
		totalLength += len(tokens)
	}
	model.averageDL = float64(totalLength) / float64(len(model.documents))
	model.orderedVocab = make([]string, 0, len(documentFrequency))
	for term := range documentFrequency {
		model.orderedVocab = append(model.orderedVocab, term)
	}
	sort.Strings(model.orderedVocab)
	for index, term := range model.orderedVocab {
		model.vocabulary[term] = uint32(index)
		df := float64(documentFrequency[term])
		n := float64(len(model.documents))
		model.idf[term] = math.Log(1 + (n-df+0.5)/(df+0.5))
	}
	return model, nil
}

func (m *BM25Model) Vocabulary() []string {
	return append([]string(nil), m.orderedVocab...)
}

func (m *BM25Model) Search(query string, limit int) []Candidate {
	if m == nil || limit <= 0 {
		return nil
	}
	queryTerms := uniqueTerms(m.analyzer.Analyze(query))
	results := make([]Candidate, 0, len(m.documents))
	for _, document := range m.documents {
		score := 0.0
		for term := range queryTerms {
			tf := document.terms[term]
			if tf == 0 {
				continue
			}
			score += m.idf[term] * m.termFrequencyWeight(tf, document.length)
		}
		results = append(results, Candidate{ID: document.id, Score: score})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].ID < results[j].ID
		}
		return results[i].Score > results[j].Score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

func (m *BM25Model) QuerySparseVector(query string) SparseVector {
	terms := uniqueTerms(m.analyzer.Analyze(query))
	items := make([]sparseItem, 0, len(terms))
	for term := range terms {
		index, ok := m.vocabulary[term]
		if !ok {
			continue
		}
		items = append(items, sparseItem{index: index, value: float32(m.idf[term])})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].index < items[j].index })
	return sparseItems(items)
}

func (m *BM25Model) DocumentSparseVector(id string) SparseVector {
	document, ok := m.byID[id]
	if !ok {
		return SparseVector{}
	}
	items := make([]sparseItem, 0, len(document.terms))
	for term, tf := range document.terms {
		items = append(items, sparseItem{index: m.vocabulary[term], value: float32(m.termFrequencyWeight(tf, document.length))})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].index < items[j].index })
	return sparseItems(items)
}

func (m *BM25Model) termFrequencyWeight(tf, documentLength int) float64 {
	numerator := float64(tf) * (m.k1 + 1)
	denominator := float64(tf) + m.k1*(1-m.b+m.b*float64(documentLength)/m.averageDL)
	return numerator / denominator
}

func uniqueTerms(tokens []string) map[string]struct{} {
	result := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		result[token] = struct{}{}
	}
	return result
}

func sparseItems(items []sparseItem) SparseVector {
	vector := SparseVector{Indices: make([]uint32, 0, len(items)), Values: make([]float32, 0, len(items))}
	for _, item := range items {
		vector.Indices = append(vector.Indices, item.index)
		vector.Values = append(vector.Values, item.value)
	}
	return vector
}
