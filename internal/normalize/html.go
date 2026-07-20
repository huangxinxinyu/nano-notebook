package normalize

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"
)

const (
	maxHTMLNodes = 100_000
	maxHTMLDepth = 256
)

// HTML extracts an immutable primary-content snapshot. It intentionally drops
// live behavior and navigation chrome instead of trying to preserve a web app.
func HTML(input Input) (Artifact, error) {
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.ExtractionConfigID = strings.TrimSpace(input.ExtractionConfigID)
	input.Format = strings.TrimSpace(input.Format)
	if input.SourceID == "" || input.ExtractionConfigID == "" || input.Format != "html" ||
		len(input.Payload) == 0 || !utf8.Valid(input.Payload) {
		return Artifact{}, errors.New("invalid HTML normalization input")
	}
	document, err := xhtml.Parse(bytes.NewReader(input.Payload))
	if err != nil {
		return Artifact{}, fmt.Errorf("parse HTML: %w", err)
	}
	if err := validateHTMLTree(document); err != nil {
		return Artifact{}, err
	}
	root := firstHTMLElement(document, "main")
	if root == nil {
		root = firstHTMLElement(document, "article")
	}
	if root == nil {
		root = firstHTMLElement(document, "body")
	}
	if root == nil {
		return Artifact{}, errors.New("HTML Source has no primary document")
	}

	sourceBlocks := make([]officeBlock, 0)
	collectHTMLBlocks(root, &sourceBlocks)
	if len(sourceBlocks) == 0 {
		if fallback := htmlNodeText(root); fallback != "" {
			sourceBlocks = append(sourceBlocks, officeBlock{kind: "paragraph", text: fallback})
		}
	}
	if len(sourceBlocks) == 0 {
		return Artifact{}, errors.New("HTML Source has no usable primary content")
	}
	for index := range sourceBlocks {
		sourceBlocks[index].coordinate = SourceCoordinate{Kind: "html_block", Block: index + 1}
	}
	return finalizeOfficeArtifact(input, sourceBlocks)
}

func validateHTMLTree(root *xhtml.Node) error {
	type frame struct {
		node  *xhtml.Node
		depth int
	}
	count := 0
	stack := []frame{{node: root, depth: 1}}
	for len(stack) > 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		count++
		if count > maxHTMLNodes || current.depth > maxHTMLDepth {
			return errors.New("HTML DOM exceeds processing budget")
		}
		for child := current.node.FirstChild; child != nil; child = child.NextSibling {
			stack = append(stack, frame{node: child, depth: current.depth + 1})
		}
	}
	return nil
}

func firstHTMLElement(root *xhtml.Node, name string) *xhtml.Node {
	if root.Type == xhtml.ElementNode && strings.EqualFold(root.Data, name) {
		return root
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if found := firstHTMLElement(child, name); found != nil {
			return found
		}
	}
	return nil
}

func collectHTMLBlocks(root *xhtml.Node, blocks *[]officeBlock) {
	for node := root.FirstChild; node != nil; node = node.NextSibling {
		if node.Type != xhtml.ElementNode {
			continue
		}
		name := strings.ToLower(node.Data)
		if excludedHTMLElement(node, name) {
			continue
		}
		kind, level := htmlBlockKind(name)
		if kind != "" {
			text := htmlNodeText(node)
			if name == "table" {
				text = htmlTableText(node)
			}
			if text != "" {
				*blocks = append(*blocks, officeBlock{kind: kind, text: text, headingLevel: level})
			}
			continue
		}
		collectHTMLBlocks(node, blocks)
	}
}

func excludedHTMLElement(node *xhtml.Node, name string) bool {
	switch name {
	case "script", "style", "noscript", "template", "svg", "canvas", "nav", "aside", "footer", "form", "button", "input", "select", "textarea":
		return true
	}
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, "hidden") ||
			(strings.EqualFold(attribute.Key, "aria-hidden") && strings.EqualFold(strings.TrimSpace(attribute.Val), "true")) {
			return true
		}
	}
	return false
}

func htmlBlockKind(name string) (string, int) {
	if len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6' {
		return "heading", int(name[1] - '0')
	}
	switch name {
	case "p", "blockquote", "figcaption":
		return "paragraph", 0
	case "li":
		return "list", 0
	case "pre":
		return "code", 0
	case "table":
		return "table", 0
	default:
		return "", 0
	}
}

func htmlNodeText(root *xhtml.Node) string {
	values := make([]string, 0)
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode && excludedHTMLElement(node, strings.ToLower(node.Data)) {
			return
		}
		if node.Type == xhtml.TextNode {
			values = append(values, node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return strings.Join(strings.Fields(strings.Join(values, "")), " ")
}

func htmlTableText(table *xhtml.Node) string {
	rows := make([]string, 0)
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode && strings.EqualFold(node.Data, "tr") {
			cells := make([]string, 0)
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				if child.Type == xhtml.ElementNode && (strings.EqualFold(child.Data, "td") || strings.EqualFold(child.Data, "th")) {
					if text := htmlNodeText(child); text != "" {
						cells = append(cells, text)
					}
				}
			}
			if len(cells) > 0 {
				rows = append(rows, strings.Join(cells, " | "))
			}
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(table)
	return strings.Join(rows, "\n")
}
