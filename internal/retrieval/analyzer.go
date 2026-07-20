package retrieval

import (
	"strings"
	"unicode"
)

type MixedAnalyzer struct {
	id string
}

func NewMixedAnalyzer(id string) MixedAnalyzer {
	return MixedAnalyzer{id: strings.TrimSpace(id)}
}

func (a MixedAnalyzer) ID() string {
	return a.id
}

func (a MixedAnalyzer) Analyze(text string) []string {
	tokens := make([]string, 0)
	word := make([]rune, 0)
	han := make([]rune, 0)
	flushWord := func() {
		if len(word) > 0 {
			tokens = append(tokens, strings.ToLower(string(word)))
			word = word[:0]
		}
	}
	flushHan := func() {
		if len(han) == 0 {
			return
		}
		for _, character := range han {
			tokens = append(tokens, string(character))
		}
		for index := 0; index+1 < len(han); index++ {
			tokens = append(tokens, string(han[index:index+2]))
		}
		han = han[:0]
	}
	for _, character := range text {
		switch {
		case unicode.Is(unicode.Han, character):
			flushWord()
			han = append(han, character)
		case unicode.IsLetter(character) || unicode.IsDigit(character):
			flushHan()
			word = append(word, unicode.ToLower(character))
		default:
			flushWord()
			flushHan()
		}
	}
	flushWord()
	flushHan()
	return tokens
}
