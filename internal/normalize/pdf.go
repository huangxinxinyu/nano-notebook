package normalize

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	pdfreader "github.com/ledongthuc/pdf"
)

const maxNativePDFPages = 500

type pdfLine struct {
	text                string
	x, y, width, height float64
}

type VisualPage struct {
	Ordinal int
	Width   int
	Height  int
	Regions []ImageRegion
}

// PDF extracts the native text layer from a PDF into the normalized evidence
// contract. A PDF without usable native text is rejected so callers can route
// it to a vision extraction adapter without silently publishing empty evidence.
func PDF(input Input) (Artifact, error) {
	return PDFWithVisualPages(input, nil)
}

func PDFPagesRequiringVision(payload []byte) ([]int, error) {
	reader, err := pdfreader.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return nil, fmt.Errorf("open PDF: %w", err)
	}
	pageCount := reader.NumPage()
	if pageCount < 1 {
		return nil, fmt.Errorf("PDF page count %d is outside supported range", pageCount)
	}
	if pageCount > maxNativePDFPages {
		return nil, fmt.Errorf("%w: PDF page count %d", ErrProcessingBudget, pageCount)
	}
	missing := make([]int, 0)
	for pageNumber := 1; pageNumber <= pageCount; pageNumber++ {
		content, err := safePDFContent(reader.Page(pageNumber))
		if err != nil {
			return nil, fmt.Errorf("extract PDF page %d: %w", pageNumber, err)
		}
		if len(groupPDFLines(content.Text)) == 0 {
			missing = append(missing, pageNumber)
		}
	}
	return missing, nil
}

func PDFWithVisualPages(input Input, visualPages []VisualPage) (Artifact, error) {
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.ExtractionConfigID = strings.TrimSpace(input.ExtractionConfigID)
	input.Format = strings.TrimSpace(input.Format)
	if input.SourceID == "" || input.ExtractionConfigID == "" || input.Format != "pdf" || len(input.Payload) == 0 {
		return Artifact{}, errors.New("invalid PDF normalization input")
	}

	reader, err := pdfreader.NewReader(bytes.NewReader(input.Payload), int64(len(input.Payload)))
	if err != nil {
		return Artifact{}, fmt.Errorf("open PDF: %w", err)
	}
	pageCount := reader.NumPage()
	if pageCount < 1 {
		return Artifact{}, fmt.Errorf("PDF page count %d is outside supported range", pageCount)
	}
	if pageCount > maxNativePDFPages {
		return Artifact{}, fmt.Errorf("%w: PDF page count %d", ErrProcessingBudget, pageCount)
	}

	visualByPage := make(map[int]VisualPage, len(visualPages))
	totalVisualRegions := 0
	for _, visual := range visualPages {
		if visual.Ordinal < 1 || visual.Ordinal > pageCount || visual.Width < 1 || visual.Height < 1 ||
			len(visual.Regions) < 1 || len(visual.Regions) > 256 {
			return Artifact{}, errors.New("invalid PDF visual page")
		}
		if _, duplicate := visualByPage[visual.Ordinal]; duplicate {
			return Artifact{}, errors.New("duplicate PDF visual page")
		}
		totalVisualRegions += len(visual.Regions)
		if totalVisualRegions > 4096 {
			return Artifact{}, fmt.Errorf("%w: PDF visual Evidence regions", ErrProcessingBudget)
		}
		visualByPage[visual.Ordinal] = visual
	}
	blocks := make([]officeBlock, 0)
	for pageNumber := 1; pageNumber <= pageCount; pageNumber++ {
		content, err := safePDFContent(reader.Page(pageNumber))
		if err != nil {
			return Artifact{}, fmt.Errorf("extract PDF page %d: %w", pageNumber, err)
		}
		lines := groupPDFLines(content.Text)
		if len(lines) > 0 {
			if _, exists := visualByPage[pageNumber]; exists {
				return Artifact{}, errors.New("visual Evidence supplied for PDF page with usable native text")
			}
			for _, line := range lines {
				blocks = append(blocks, officeBlock{kind: "paragraph", text: line.text, coordinate: SourceCoordinate{
					Kind: "pdf_region", Page: pageNumber, X: line.x, Y: line.y, Width: line.width, Height: line.height,
				}})
			}
			continue
		}
		visual, exists := visualByPage[pageNumber]
		if !exists {
			return Artifact{}, fmt.Errorf("PDF page %d has no usable native text; vision extraction required", pageNumber)
		}
		regions := append([]ImageRegion(nil), visual.Regions...)
		for index := range regions {
			region := &regions[index]
			region.Text = strings.TrimSpace(region.Text)
			if region.Text == "" || !utf8.ValidString(region.Text) || utf8.RuneCountInString(region.Text) > 8_000 ||
				invalidMediaNumber(region.X) || invalidMediaNumber(region.Y) || invalidMediaNumber(region.Width) || invalidMediaNumber(region.Height) ||
				region.X < 0 || region.Y < 0 || region.Width <= 0 || region.Height <= 0 ||
				region.X+region.Width > float64(visual.Width) || region.Y+region.Height > float64(visual.Height) {
				return Artifact{}, errors.New("invalid PDF visual Evidence region")
			}
		}
		sort.SliceStable(regions, func(left, right int) bool {
			if regions[left].Y != regions[right].Y {
				return regions[left].Y < regions[right].Y
			}
			if regions[left].X != regions[right].X {
				return regions[left].X < regions[right].X
			}
			return regions[left].Text < regions[right].Text
		})
		for _, region := range regions {
			blocks = append(blocks, officeBlock{kind: "paragraph", text: region.Text, coordinate: SourceCoordinate{
				Kind: "pdf_region", Page: pageNumber, X: region.X, Y: region.Y, Width: region.Width, Height: region.Height,
			}})
		}
		delete(visualByPage, pageNumber)
	}
	if len(visualByPage) != 0 {
		return Artifact{}, errors.New("unused PDF visual Evidence")
	}
	return finalizeOfficeArtifact(input, blocks)
}

func safePDFContent(page pdfreader.Page) (content pdfreader.Content, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("PDF parser panic: %v", recovered)
		}
	}()
	return page.Content(), nil
}

func groupPDFLines(fragments []pdfreader.Text) []pdfLine {
	usable := make([]pdfreader.Text, 0, len(fragments))
	for _, fragment := range fragments {
		if fragment.S == "" || !utf8.ValidString(fragment.S) || invalidPDFNumber(fragment.X) || invalidPDFNumber(fragment.Y) ||
			invalidPDFNumber(fragment.W) || invalidPDFNumber(fragment.FontSize) || fragment.W < 0 || fragment.FontSize <= 0 {
			continue
		}
		usable = append(usable, fragment)
	}
	sort.SliceStable(usable, func(left, right int) bool {
		if usable[left].Y != usable[right].Y {
			return usable[left].Y > usable[right].Y
		}
		if usable[left].X != usable[right].X {
			return usable[left].X < usable[right].X
		}
		return false
	})

	lines := make([]pdfLine, 0)
	for cursor := 0; cursor < len(usable); {
		baseline := usable[cursor].Y
		end := cursor + 1
		for end < len(usable) && math.Abs(usable[end].Y-baseline) <= 1.5 {
			end++
		}
		lineFragments := usable[cursor:end]
		sort.SliceStable(lineFragments, func(left, right int) bool { return lineFragments[left].X < lineFragments[right].X })
		var value strings.Builder
		minX, maxX := lineFragments[0].X, lineFragments[0].X+effectivePDFWidth(lineFragments[0])
		minY, maxY := lineFragments[0].Y, lineFragments[0].Y+lineFragments[0].FontSize
		previousEnd := minX
		for index, fragment := range lineFragments {
			if index > 0 && fragment.X-previousEnd > math.Max(1, fragment.FontSize*0.2) &&
				!strings.HasSuffix(value.String(), " ") && !strings.HasPrefix(fragment.S, " ") {
				value.WriteByte(' ')
			}
			value.WriteString(fragment.S)
			fragmentEnd := fragment.X + effectivePDFWidth(fragment)
			previousEnd = math.Max(previousEnd, fragmentEnd)
			minX = math.Min(minX, fragment.X)
			maxX = math.Max(maxX, fragmentEnd)
			minY = math.Min(minY, fragment.Y)
			maxY = math.Max(maxY, fragment.Y+fragment.FontSize)
		}
		lineText := strings.TrimSpace(value.String())
		if lineText != "" && maxX > minX && maxY > minY {
			lines = append(lines, pdfLine{text: lineText, x: minX, y: minY, width: maxX - minX, height: maxY - minY})
		}
		cursor = end
	}
	return lines
}

func invalidPDFNumber(value float64) bool {
	return math.IsNaN(value) || math.IsInf(value, 0)
}

func effectivePDFWidth(fragment pdfreader.Text) float64 {
	if fragment.W > 0 {
		return fragment.W
	}
	return fragment.FontSize * 0.5 * float64(utf8.RuneCountInString(fragment.S))
}
