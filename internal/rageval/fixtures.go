package rageval

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
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
			"[Content_Types].xml":             `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/><Override PartName="/ppt/slides/slide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/><Override PartName="/ppt/slides/slide2.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/></Types>`,
			"_rels/.rels":                     `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/></Relationships>`,
			"ppt/presentation.xml":            `<p:presentation xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:sldIdLst><p:sldId id="256" r:id="rId1"/><p:sldId id="257" r:id="rId2"/></p:sldIdLst><p:sldSz cx="9144000" cy="5143500" type="screen16x9"/></p:presentation>`,
			"ppt/_rels/presentation.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide2.xml"/></Relationships>`,
			"ppt/slides/slide1.xml":           fixtureSlide("Baseline", 914400, 914400, 3657600, 914400),
			"ppt/slides/slide2.xml":           fixtureSlide("Recall target is 95 percent", 914400, 1828800, 3657600, 914400),
		})
		return ResolvedFixture{Family: FamilyPPTX, Filename: "metrics.pptx", MediaType: "application/vnd.openxmlformats-officedocument.presentationml.presentation", Payload: payload}, err
	case "mp3-en-v1":
		payload, err := decodeFixtureAsset(fixtureMP3Base64, false)
		return ResolvedFixture{Family: FamilyMP3, Filename: "capacity.mp3", MediaType: "audio/mpeg", Payload: payload}, err
	case "wav-en-v1":
		payload, err := decodeFixtureAsset(fixtureWAVGzipBase64, true)
		return ResolvedFixture{Family: FamilyWAV, Filename: "degradation.wav", MediaType: "audio/wav", Payload: payload}, err
	case "m4a-en-v1":
		payload, err := decodeFixtureAsset(fixtureM4ABase64, false)
		return ResolvedFixture{Family: FamilyM4A, Filename: "authority.m4a", MediaType: "audio/mp4", Payload: payload}, err
	case "png-en-v1":
		payload, err := fixturePNG()
		return ResolvedFixture{Family: FamilyPNG, Filename: "barrier.png", MediaType: "image/png", Payload: payload}, err
	case "jpeg-en-v1":
		payload, err := fixtureJPEG()
		return ResolvedFixture{Family: FamilyJPEG, Filename: "verifier.jpg", MediaType: "image/jpeg", Payload: payload}, err
	case "webp-en-v1":
		payload, err := fixtureWebP()
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
	return fmt.Sprintf(`<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:cSld><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr><p:sp><p:nvSpPr><p:cNvPr id="2" name="Text"/><p:cNvSpPr txBox="1"/><p:nvPr/></p:nvSpPr><p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr><p:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:rPr lang="en-US" sz="2400"/><a:t>%s</a:t></a:r><a:endParaRPr lang="en-US"/></a:p></p:txBody></p:sp></p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sld>`, x, y, width, height, text)
}
