package normalize_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/normalize"
)

func TestHTMLAdapterExtractsStablePrimaryStructureWithoutLivePageBehavior(t *testing.T) {
	payload := []byte(`<!doctype html><html><head><title>Ignored title</title><style>.x{}</style></head><body>
		<nav>Navigation noise</nav><main>
			<h1>Research &amp; Results</h1>
			<p>Primary <strong>paragraph</strong>.</p>
			<ul><li>First item</li><li>Second item</li></ul>
			<table><tr><th>Metric</th><th>Value</th></tr><tr><td>Recall</td><td>0.91</td></tr></table>
			<script>steal()</script><aside>Related noise</aside>
		</main><footer>Footer noise</footer></body></html>`)
	input := normalize.Input{SourceID: "src_html", ExtractionConfigID: "extract-html-primary-v1", Format: "html", Payload: payload}
	first, err := normalize.HTML(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := normalize.HTML(input)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []string{"heading", "paragraph", "list", "list", "table"}
	wantText := []string{"Research & Results", "Primary paragraph.", "First item", "Second item", "Metric | Value\nRecall | 0.91"}
	if len(first.Blocks) != len(wantKinds) || first.SHA256 != second.SHA256 || !bytes.Equal(first.CanonicalJSON, second.CanonicalJSON) {
		t.Fatalf("HTML artifact=%+v", first)
	}
	for index, block := range first.Blocks {
		if block.Kind != wantKinds[index] || block.Text != wantText[index] || block.Coordinate == nil ||
			block.Coordinate.Kind != "html_block" || block.Coordinate.Block != index+1 {
			t.Fatalf("block %d=%+v", index, block)
		}
	}
	for _, forbidden := range []string{"Navigation noise", "steal", "Related noise", "Footer noise", "Ignored title"} {
		if bytes.Contains(first.CanonicalJSON, []byte(forbidden)) {
			t.Fatalf("HTML artifact retained excluded content %q", forbidden)
		}
	}
}

func TestHTMLAdapterUsesArticleBeforeBodyFallback(t *testing.T) {
	artifact, err := normalize.HTML(normalize.Input{
		SourceID: "src_article", ExtractionConfigID: "extract-html-primary-v1", Format: "html",
		Payload: []byte(`<html><body><p>Body noise.</p><article><h2>Article</h2><p>Kept.</p></article></body></html>`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Text != "Article\n\nKept." || artifact.Blocks[0].HeadingLevel != 2 {
		t.Fatalf("article artifact=%+v", artifact)
	}
}

func TestHTMLAdapterRejectsInvalidIdentityEncodingEmptyAndExcessiveDOM(t *testing.T) {
	tooManyNodes := bytes.Repeat([]byte("<span>x</span>"), 100_001)
	tooDeep := strings.Repeat("<div>", 300) + "<p>x</p>" + strings.Repeat("</div>", 300)
	tests := []normalize.Input{
		{SourceID: "", ExtractionConfigID: "extract-html-primary-v1", Format: "html", Payload: []byte("<p>x</p>")},
		{SourceID: "bad_utf8", ExtractionConfigID: "extract-html-primary-v1", Format: "html", Payload: []byte{'<', 'p', '>', 0xff}},
		{SourceID: "empty", ExtractionConfigID: "extract-html-primary-v1", Format: "html", Payload: []byte("<html><body><nav>Only navigation.</nav></body></html>")},
		{SourceID: "large_dom", ExtractionConfigID: "extract-html-primary-v1", Format: "html", Payload: append([]byte("<html><body><main><p>"), tooManyNodes...)},
		{SourceID: "deep_dom", ExtractionConfigID: "extract-html-primary-v1", Format: "html", Payload: []byte(tooDeep)},
	}
	for _, input := range tests {
		if _, err := normalize.HTML(input); err == nil {
			t.Fatalf("HTML accepted invalid %s input", input.SourceID)
		}
	}
}

func TestHTMLAdapterClassifiesDOMBudgetSeparatelyFromMalformedContent(t *testing.T) {
	payload := append([]byte("<html><body><main>"), bytes.Repeat([]byte("<span>x</span>"), 100_001)...)
	_, err := normalize.HTML(normalize.Input{SourceID: "budget", ExtractionConfigID: "html-v1", Format: "html", Payload: payload})
	if !errors.Is(err, normalize.ErrProcessingBudget) {
		t.Fatalf("HTML budget error=%v", err)
	}
}
