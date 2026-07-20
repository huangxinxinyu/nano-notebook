package normalize

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxOOXMLEntries       = 4096
	maxOOXMLExpandedBytes = 128 << 20
	maxOOXMLPartBytes     = 16 << 20
	maxOOXMLSlides        = 500
	emuPerPoint           = 12700
)

type officeBlock struct {
	kind, text   string
	headingLevel int
	coordinate   SourceCoordinate
}

// OOXML extracts native structure from DOCX and PPTX packages without running
// macros, relationships, embedded objects, or active content.
func OOXML(input Input) (Artifact, error) {
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.ExtractionConfigID = strings.TrimSpace(input.ExtractionConfigID)
	input.Format = strings.TrimSpace(input.Format)
	if input.SourceID == "" || input.ExtractionConfigID == "" ||
		(input.Format != "docx" && input.Format != "pptx") || len(input.Payload) == 0 {
		return Artifact{}, errors.New("invalid OOXML normalization input")
	}
	archive, err := zip.NewReader(bytes.NewReader(input.Payload), int64(len(input.Payload)))
	if err != nil {
		return Artifact{}, fmt.Errorf("open OOXML package: %w", err)
	}
	parts, err := boundedOOXMLParts(archive.File)
	if err != nil {
		return Artifact{}, err
	}
	contentTypes, ok := parts["[Content_Types].xml"]
	if !ok {
		return Artifact{}, errors.New("OOXML content types are missing")
	}
	contentTypeXML, err := readOOXMLPart(contentTypes)
	if err != nil {
		return Artifact{}, err
	}
	var blocks []officeBlock
	if input.Format == "docx" {
		if !bytes.Contains(contentTypeXML, []byte("wordprocessingml.document.main+xml")) {
			return Artifact{}, errors.New("OOXML package is not a DOCX document")
		}
		document, ok := parts["word/document.xml"]
		if !ok {
			return Artifact{}, errors.New("DOCX primary document is missing")
		}
		payload, err := readOOXMLPart(document)
		if err != nil {
			return Artifact{}, err
		}
		blocks, err = parseDOCX(payload)
		if err != nil {
			return Artifact{}, err
		}
	} else {
		if !bytes.Contains(contentTypeXML, []byte("presentationml.slide+xml")) {
			return Artifact{}, errors.New("OOXML package is not a PPTX presentation")
		}
		blocks, err = parsePPTX(parts)
		if err != nil {
			return Artifact{}, err
		}
	}
	if len(blocks) == 0 {
		return Artifact{}, errors.New("OOXML Source has no usable primary content")
	}
	return finalizeOfficeArtifact(input, blocks)
}

func PPTXSlidesRequiringVision(payload []byte) ([]int, error) {
	parts, slideNumbers, err := pptxPackage(payload)
	if err != nil {
		return nil, err
	}
	missing := make([]int, 0)
	for _, slide := range slideNumbers {
		content, err := readOOXMLPart(parts[fmt.Sprintf("ppt/slides/slide%d.xml", slide)])
		if err != nil {
			return nil, err
		}
		blocks, err := parsePPTXSlide(content, slide)
		if err != nil {
			return nil, err
		}
		if len(blocks) == 0 {
			missing = append(missing, slide)
		}
	}
	return missing, nil
}

func PPTXWithVisualSlides(input Input, visualSlides []VisualPage) (Artifact, error) {
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.ExtractionConfigID = strings.TrimSpace(input.ExtractionConfigID)
	input.Format = strings.TrimSpace(input.Format)
	if input.SourceID == "" || input.ExtractionConfigID == "" || input.Format != "pptx" || len(input.Payload) == 0 {
		return Artifact{}, errors.New("invalid PPTX normalization input")
	}
	parts, slideNumbers, err := pptxPackage(input.Payload)
	if err != nil {
		return Artifact{}, err
	}
	visualBySlide := make(map[int]VisualPage, len(visualSlides))
	for _, visual := range visualSlides {
		if visual.Ordinal < 1 || visual.Width < 1 || visual.Height < 1 || len(visual.Regions) < 1 || len(visual.Regions) > 256 {
			return Artifact{}, errors.New("invalid PPTX visual slide")
		}
		if _, duplicate := visualBySlide[visual.Ordinal]; duplicate {
			return Artifact{}, errors.New("duplicate PPTX visual slide")
		}
		visualBySlide[visual.Ordinal] = visual
	}
	blocks := make([]officeBlock, 0)
	for _, slide := range slideNumbers {
		content, err := readOOXMLPart(parts[fmt.Sprintf("ppt/slides/slide%d.xml", slide)])
		if err != nil {
			return Artifact{}, err
		}
		native, err := parsePPTXSlide(content, slide)
		if err != nil {
			return Artifact{}, err
		}
		if len(native) > 0 {
			if _, exists := visualBySlide[slide]; exists {
				return Artifact{}, errors.New("visual Evidence supplied for PPTX slide with usable native text")
			}
			blocks = append(blocks, native...)
			continue
		}
		visual, exists := visualBySlide[slide]
		if !exists {
			return Artifact{}, fmt.Errorf("PPTX slide %d has no usable native text; vision extraction required", slide)
		}
		regions := append([]ImageRegion(nil), visual.Regions...)
		for index := range regions {
			region := &regions[index]
			region.Text = strings.TrimSpace(region.Text)
			if region.Text == "" || !utf8.ValidString(region.Text) || utf8.RuneCountInString(region.Text) > 8_000 ||
				invalidMediaNumber(region.X) || invalidMediaNumber(region.Y) || invalidMediaNumber(region.Width) || invalidMediaNumber(region.Height) ||
				region.X < 0 || region.Y < 0 || region.Width <= 0 || region.Height <= 0 ||
				region.X+region.Width > float64(visual.Width) || region.Y+region.Height > float64(visual.Height) {
				return Artifact{}, errors.New("invalid PPTX visual Evidence region")
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
				Kind: "slide_region", Slide: slide, X: region.X, Y: region.Y, Width: region.Width, Height: region.Height,
			}})
		}
		delete(visualBySlide, slide)
	}
	if len(visualBySlide) != 0 {
		return Artifact{}, errors.New("unused PPTX visual Evidence")
	}
	return finalizeOfficeArtifact(input, blocks)
}

func pptxPackage(payload []byte) (map[string]*zip.File, []int, error) {
	archive, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return nil, nil, fmt.Errorf("open OOXML package: %w", err)
	}
	parts, err := boundedOOXMLParts(archive.File)
	if err != nil {
		return nil, nil, err
	}
	contentTypes, ok := parts["[Content_Types].xml"]
	if !ok {
		return nil, nil, errors.New("OOXML content types are missing")
	}
	contentTypeXML, err := readOOXMLPart(contentTypes)
	if err != nil || !bytes.Contains(contentTypeXML, []byte("presentationml.slide+xml")) {
		return nil, nil, errors.New("OOXML package is not a PPTX presentation")
	}
	slideNumbers := make([]int, 0)
	for name := range parts {
		if !strings.HasPrefix(name, "ppt/slides/slide") || !strings.HasSuffix(name, ".xml") {
			continue
		}
		number, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "ppt/slides/slide"), ".xml"))
		if err == nil && number > 0 {
			slideNumbers = append(slideNumbers, number)
		}
	}
	if len(slideNumbers) < 1 || len(slideNumbers) > maxOOXMLSlides {
		return nil, nil, fmt.Errorf("%w: PPTX slide count", ErrProcessingBudget)
	}
	sort.Ints(slideNumbers)
	for index, number := range slideNumbers {
		if number != index+1 {
			return nil, nil, errors.New("PPTX slide numbering is not contiguous")
		}
	}
	return parts, slideNumbers, nil
}

func boundedOOXMLParts(files []*zip.File) (map[string]*zip.File, error) {
	if len(files) == 0 || len(files) > maxOOXMLEntries {
		return nil, fmt.Errorf("%w: OOXML entry count", ErrProcessingBudget)
	}
	parts := make(map[string]*zip.File, len(files))
	var expanded uint64
	for _, file := range files {
		if file == nil || strings.Contains(file.Name, "\\") || strings.HasPrefix(file.Name, "/") ||
			strings.Contains("/"+file.Name+"/", "/../") {
			return nil, errors.New("OOXML package contains an unsafe part name")
		}
		expanded += file.UncompressedSize64
		if file.UncompressedSize64 > maxOOXMLPartBytes || expanded > maxOOXMLExpandedBytes {
			return nil, fmt.Errorf("%w: OOXML expansion", ErrProcessingBudget)
		}
		if _, duplicate := parts[file.Name]; duplicate {
			return nil, errors.New("OOXML package contains duplicate parts")
		}
		parts[file.Name] = file
	}
	return parts, nil
}

func readOOXMLPart(file *zip.File) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	payload, err := io.ReadAll(io.LimitReader(reader, maxOOXMLPartBytes+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > maxOOXMLPartBytes {
		return nil, fmt.Errorf("%w: OOXML part", ErrProcessingBudget)
	}
	return payload, nil
}

func parseDOCX(payload []byte) ([]officeBlock, error) {
	decoder := xml.NewDecoder(bytes.NewReader(payload))
	blocks := make([]officeBlock, 0)
	var paragraph strings.Builder
	paragraphDepth, tableDepth := 0, 0
	style := ""
	inText := false
	rows := make([]string, 0)
	rowCells := make([]string, 0)
	cellParagraphs := make([]string, 0)
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse DOCX XML: %w", err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "tbl":
				tableDepth++
				if tableDepth == 1 {
					rows = rows[:0]
				}
			case "tr":
				if tableDepth == 1 {
					rowCells = rowCells[:0]
				}
			case "tc":
				if tableDepth == 1 {
					cellParagraphs = cellParagraphs[:0]
				}
			case "p":
				paragraphDepth++
				if paragraphDepth == 1 {
					paragraph.Reset()
					style = ""
				}
			case "pStyle":
				for _, attribute := range value.Attr {
					if attribute.Name.Local == "val" {
						style = attribute.Value
					}
				}
			case "tab":
				paragraph.WriteByte('\t')
			case "br":
				paragraph.WriteByte('\n')
			case "t":
				inText = true
			}
		case xml.CharData:
			if paragraphDepth > 0 && inText {
				paragraph.Write([]byte(value))
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "p":
				if paragraphDepth == 1 {
					text := strings.TrimSpace(paragraph.String())
					if text != "" {
						if tableDepth > 0 {
							cellParagraphs = append(cellParagraphs, text)
						} else {
							level := docxHeadingLevel(style)
							kind := "paragraph"
							if level > 0 {
								kind = "heading"
							}
							blocks = append(blocks, officeBlock{kind: kind, text: text, headingLevel: level})
						}
					}
				}
				paragraphDepth--
			case "t":
				inText = false
			case "tc":
				if tableDepth == 1 {
					rowCells = append(rowCells, strings.Join(cellParagraphs, " "))
				}
			case "tr":
				if tableDepth == 1 && len(rowCells) > 0 {
					rows = append(rows, strings.Join(rowCells, " | "))
				}
			case "tbl":
				if tableDepth == 1 && len(rows) > 0 {
					blocks = append(blocks, officeBlock{kind: "table", text: strings.Join(rows, "\n")})
				}
				tableDepth--
			}
		}
	}
	for index := range blocks {
		blocks[index].coordinate = SourceCoordinate{Kind: "document_block", Block: index + 1}
	}
	return blocks, nil
}

func docxHeadingLevel(style string) int {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(style), " ", ""))
	if !strings.HasPrefix(normalized, "heading") {
		return 0
	}
	level, err := strconv.Atoi(strings.TrimPrefix(normalized, "heading"))
	if err != nil || level < 1 || level > 6 {
		return 0
	}
	return level
}

func parsePPTX(parts map[string]*zip.File) ([]officeBlock, error) {
	type slidePart struct {
		number int
		file   *zip.File
	}
	slides := make([]slidePart, 0)
	for name, file := range parts {
		if !strings.HasPrefix(name, "ppt/slides/slide") || !strings.HasSuffix(name, ".xml") {
			continue
		}
		numberText := strings.TrimSuffix(strings.TrimPrefix(name, "ppt/slides/slide"), ".xml")
		number, err := strconv.Atoi(numberText)
		if err != nil || number < 1 {
			continue
		}
		slides = append(slides, slidePart{number: number, file: file})
	}
	if len(slides) == 0 {
		return nil, errors.New("PPTX slide count is outside supported range")
	}
	if len(slides) > maxOOXMLSlides {
		return nil, fmt.Errorf("%w: PPTX slide count", ErrProcessingBudget)
	}
	sort.Slice(slides, func(left, right int) bool { return slides[left].number < slides[right].number })
	blocks := make([]officeBlock, 0)
	for _, slide := range slides {
		payload, err := readOOXMLPart(slide.file)
		if err != nil {
			return nil, err
		}
		slideBlocks, err := parsePPTXSlide(payload, slide.number)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, slideBlocks...)
	}
	return blocks, nil
}

func parsePPTXSlide(payload []byte, slide int) ([]officeBlock, error) {
	decoder := xml.NewDecoder(bytes.NewReader(payload))
	blocks := make([]officeBlock, 0)
	shapeDepth := 0
	kind := "paragraph"
	var text strings.Builder
	var x, y, width, height float64
	inText := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse PPTX slide %d: %w", slide, err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			if shapeDepth > 0 {
				shapeDepth++
			} else if value.Name.Local == "sp" || value.Name.Local == "graphicFrame" {
				shapeDepth = 1
				kind = "paragraph"
				if value.Name.Local == "graphicFrame" {
					kind = "table"
				}
				text.Reset()
				x, y, width, height = 0, 0, 0, 0
			}
			if shapeDepth > 0 {
				switch value.Name.Local {
				case "t":
					inText = true
				case "off":
					x = emuAttribute(value.Attr, "x")
					y = emuAttribute(value.Attr, "y")
				case "ext":
					width = emuAttribute(value.Attr, "cx")
					height = emuAttribute(value.Attr, "cy")
				}
			}
		case xml.CharData:
			if shapeDepth > 0 && inText {
				text.Write([]byte(value))
			}
		case xml.EndElement:
			if shapeDepth > 0 && value.Name.Local == "t" {
				inText = false
			}
			if shapeDepth > 1 && value.Name.Local == "p" && text.Len() > 0 {
				text.WriteByte('\n')
			}
			if shapeDepth > 0 {
				shapeDepth--
				if shapeDepth == 0 {
					value := strings.TrimSpace(text.String())
					if value != "" {
						if width <= 0 || height <= 0 {
							return nil, fmt.Errorf("PPTX slide %d has text without a bounded region", slide)
						}
						blocks = append(blocks, officeBlock{kind: kind, text: value, coordinate: SourceCoordinate{
							Kind: "slide_region", Slide: slide, X: x, Y: y, Width: width, Height: height,
						}})
					}
				}
			}
		}
	}
	return blocks, nil
}

func emuAttribute(attributes []xml.Attr, name string) float64 {
	for _, attribute := range attributes {
		if attribute.Name.Local == name {
			value, err := strconv.ParseFloat(attribute.Value, 64)
			if err == nil {
				return value / emuPerPoint
			}
		}
	}
	return 0
}

func finalizeOfficeArtifact(input Input, sourceBlocks []officeBlock) (Artifact, error) {
	var text strings.Builder
	blocks := make([]Block, 0, len(sourceBlocks))
	runeCursor := 0
	for _, sourceBlock := range sourceBlocks {
		if !utf8.ValidString(sourceBlock.text) || strings.TrimSpace(sourceBlock.text) == "" {
			continue
		}
		if text.Len() > 0 {
			text.WriteString("\n\n")
			runeCursor += 2
		}
		start := runeCursor
		text.WriteString(sourceBlock.text)
		runeCursor += utf8.RuneCountInString(sourceBlock.text)
		coordinate := sourceBlock.coordinate
		blocks = append(blocks, Block{
			ID: fmt.Sprintf("block_%06d", len(blocks)+1), Ordinal: len(blocks), Kind: sourceBlock.kind,
			Text: sourceBlock.text, StartRune: start, EndRune: runeCursor, HeadingLevel: sourceBlock.headingLevel,
			Coordinate: &coordinate,
		})
	}
	if len(blocks) == 0 {
		return Artifact{}, errors.New("OOXML Source has no usable Evidence blocks")
	}
	return Finalize(Artifact{
		SchemaVersion: "nano.normalized-source.v1", SourceID: input.SourceID,
		ExtractionConfigID: input.ExtractionConfigID, Format: input.Format, Text: text.String(), Blocks: blocks,
		Coverage: Coverage{Status: "complete", TotalRunes: runeCursor, Gaps: make([]Gap, 0)},
	})
}
