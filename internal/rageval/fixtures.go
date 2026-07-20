package rageval

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"sort"
	"strings"
	"time"
)

type ResolvedFixture struct {
	Family    SourceFamily
	Filename  string
	MediaType string
	Payload   []byte
}

func ResolveFixture(uri string) (ResolvedFixture, error) {
	const prefix = "fixture://sprint6/"
	if !strings.HasPrefix(uri, prefix) {
		return ResolvedFixture{}, errors.New("unsupported RAG Eval fixture URI")
	}
	id := strings.TrimPrefix(uri, prefix)
	switch id {
	case "txt-en-v1":
		return textFixture(FamilyTXT, "launch.txt", "text/plain", "The launch date is 20 July.\n"), nil
	case "txt-zh-v1":
		return textFixture(FamilyTXT, "authority-zh.txt", "text/plain", "业务数据权威存放在 PostgreSQL，Qdrant 只保存可重建的检索投影。\n"), nil
	case "markdown-en-v1":
		return textFixture(FamilyMarkdown, "mitigation.md", "text/markdown", "# Recovery\n\nThe required mitigation is lease fencing.\n"), nil
	case "markdown-mixed-v1":
		return textFixture(FamilyMarkdown, "ranking-mixed.md", "text/markdown", "# Hybrid ranking\n\nRRF 融合 Dense 与 BM25 的 rank；rerank 再提高语义相关性。\n"), nil
	case "html-en-v1":
		return textFixture(FamilyHTML, "policy.html", "text/html", `<!doctype html><html><body><article><h1>Source policy</h1><p>Public URL snapshots have no automatic refresh.</p></article></body></html>`), nil
	case "youtube-en-v1":
		return textFixture(FamilyYouTube, "youtube-captions.json", "application/json", `{"schema_version":"nano.youtube-captions.v1","video_id":"evalsprint6","language":"en","segments":[{"start_ms":0,"end_ms":1250,"text":"Context."},{"start_ms":1500,"end_ms":2500,"text":"The decision is stated at one point five seconds."}]}`), nil
	case "pdf-en-v1":
		return ResolvedFixture{Family: FamilyPDF, Filename: "conclusion.pdf", MediaType: "application/pdf", Payload: fixturePDF("Background evidence.", "The second page concludes that hybrid retrieval is required.")}, nil
	case "docx-en-v1":
		payload, err := fixtureOOXML(map[string]string{
			"[Content_Types].xml": `<Types><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
			"word/document.xml":   `<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>Approved owner</w:t></w:r></w:p><w:tbl><w:tr><w:tc><w:p><w:r><w:t>Component</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>platform team</w:t></w:r></w:p></w:tc></w:tr></w:tbl></w:body></w:document>`,
		})
		return ResolvedFixture{Family: FamilyDOCX, Filename: "owner.docx", MediaType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Payload: payload}, err
	case "pptx-en-v1":
		payload, err := fixtureOOXML(map[string]string{
			"[Content_Types].xml":   `<Types><Override PartName="/ppt/slides/slide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/><Override PartName="/ppt/slides/slide2.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/></Types>`,
			"ppt/slides/slide1.xml": fixtureSlide("Baseline", 914400, 914400, 3657600, 914400),
			"ppt/slides/slide2.xml": fixtureSlide("Recall target is 95 percent", 914400, 1828800, 3657600, 914400),
		})
		return ResolvedFixture{Family: FamilyPPTX, Filename: "metrics.pptx", MediaType: "application/vnd.openxmlformats-officedocument.presentationml.presentation", Payload: payload}, err
	case "mp3-en-v1":
		return ResolvedFixture{Family: FamilyMP3, Filename: "capacity.mp3", MediaType: "audio/mpeg", Payload: append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), []byte("interactive queue is reserved")...)}, nil
	case "wav-en-v1":
		return ResolvedFixture{Family: FamilyWAV, Filename: "degradation.wav", MediaType: "audio/wav", Payload: fixtureWAV([]byte("reranker unavailable"))}, nil
	case "m4a-en-v1":
		return ResolvedFixture{Family: FamilyM4A, Filename: "authority.m4a", MediaType: "audio/mp4", Payload: fixtureM4A("PostgreSQL is authority")}, nil
	case "png-en-v1":
		payload, err := fixturePNG()
		return ResolvedFixture{Family: FamilyPNG, Filename: "barrier.png", MediaType: "image/png", Payload: payload}, err
	case "jpeg-en-v1":
		payload, err := fixtureJPEG()
		return ResolvedFixture{Family: FamilyJPEG, Filename: "verifier.jpg", MediaType: "image/jpeg", Payload: payload}, err
	case "webp-en-v1":
		payload, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAA==")
		return ResolvedFixture{Family: FamilyWebP, Filename: "ready.webp", MediaType: "image/webp", Payload: payload}, err
	default:
		return ResolvedFixture{}, errors.New("unknown RAG Eval fixture")
	}
}

func textFixture(family SourceFamily, filename, mediaType, content string) ResolvedFixture {
	return ResolvedFixture{Family: family, Filename: filename, MediaType: mediaType, Payload: []byte(content)}
}

func fixturePDF(pageTexts ...string) []byte {
	objects := make([]string, 3+2*len(pageTexts))
	objects[0] = "<< /Type /Catalog /Pages 2 0 R >>"
	kids := make([]string, 0, len(pageTexts))
	for index := range pageTexts {
		kids = append(kids, fmt.Sprintf("%d 0 R", 4+index*2))
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(kids))
	objects[2] = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"
	for index, value := range pageTexts {
		pageObject := 4 + index*2
		contentObject := pageObject + 1
		objects[pageObject-1] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", contentObject)
		content := "BT /F1 12 Tf 72 720 Td (" + strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(value) + ") Tj ET"
		objects[contentObject-1] = fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
	}
	var document bytes.Buffer
	document.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = document.Len()
		_, _ = fmt.Fprintf(&document, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := document.Len()
	_, _ = fmt.Fprintf(&document, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for index := 1; index < len(offsets); index++ {
		_, _ = fmt.Fprintf(&document, "%010d 00000 n \n", offsets[index])
	}
	_, _ = fmt.Fprintf(&document, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return document.Bytes()
}

func fixtureOOXML(files map[string]string) ([]byte, error) {
	var payload bytes.Buffer
	archive := zip.NewWriter(&payload)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetModTime(time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC))
		entry, err := archive.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write([]byte(files[name])); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return payload.Bytes(), nil
}

func fixtureSlide(text string, x, y, width, height int) string {
	return fmt.Sprintf(`<p:sld xmlns:p="p" xmlns:a="a"><p:cSld><p:spTree><p:sp><p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm></p:spPr><p:txBody><a:p><a:r><a:t>%s</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`, x, y, width, height, text)
}

func fixtureWAV(content []byte) []byte {
	payload := make([]byte, 44+len(content))
	copy(payload, "RIFF")
	binary.LittleEndian.PutUint32(payload[4:8], uint32(len(payload)-8))
	copy(payload[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(payload[16:20], 16)
	binary.LittleEndian.PutUint16(payload[20:22], 1)
	binary.LittleEndian.PutUint16(payload[22:24], 1)
	binary.LittleEndian.PutUint32(payload[24:28], 8000)
	binary.LittleEndian.PutUint32(payload[28:32], 8000)
	binary.LittleEndian.PutUint16(payload[32:34], 1)
	binary.LittleEndian.PutUint16(payload[34:36], 8)
	copy(payload[36:], "data")
	binary.LittleEndian.PutUint32(payload[40:44], uint32(len(content)))
	copy(payload[44:], content)
	return payload
}

func fixtureM4A(content string) []byte {
	payload := make([]byte, 24+len(content))
	binary.BigEndian.PutUint32(payload[0:4], uint32(len(payload)))
	copy(payload[4:], "ftypM4A \x00\x00\x00\x00M4A ")
	copy(payload[24:], content)
	return payload
}

func fixturePNG() ([]byte, error) {
	value := image.NewRGBA(image.Rect(0, 0, 3, 2))
	value.Set(0, 0, color.RGBA{R: 255, A: 255})
	var payload bytes.Buffer
	err := png.Encode(&payload, value)
	return payload.Bytes(), err
}

func fixtureJPEG() ([]byte, error) {
	value := image.NewRGBA(image.Rect(0, 0, 3, 2))
	value.Set(0, 0, color.RGBA{R: 255, A: 255})
	var payload bytes.Buffer
	err := jpeg.Encode(&payload, value, &jpeg.Options{Quality: 90})
	return payload.Bytes(), err
}
