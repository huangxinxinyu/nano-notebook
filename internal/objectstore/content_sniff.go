package objectstore

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"unicode/utf8"
)

func ValidateSourceContent(format string, reader io.ReaderAt, size int64) error {
	if reader == nil || size < 1 || size > 100*1024*1024 {
		return fmt.Errorf("%w: invalid content bounds", ErrUploadMismatch)
	}
	prefixSize := size
	if prefixSize > 64*1024 {
		prefixSize = 64 * 1024
	}
	prefix := make([]byte, prefixSize)
	if _, err := reader.ReadAt(prefix, 0); err != nil && err != io.EOF {
		return fmt.Errorf("read Source signature: %w", err)
	}
	valid := false
	switch format {
	case "txt", "markdown":
		valid = utf8.Valid(prefix) && !bytes.Contains(prefix, []byte{0})
	case "pdf":
		valid = bytes.HasPrefix(prefix, []byte("%PDF-"))
	case "docx":
		valid = validOOXML(reader, size, "word/document.xml")
	case "pptx":
		valid = validOOXML(reader, size, "ppt/presentation.xml")
	case "png":
		valid = bytes.HasPrefix(prefix, []byte("\x89PNG\r\n\x1a\n"))
	case "jpeg":
		valid = len(prefix) >= 3 && prefix[0] == 0xff && prefix[1] == 0xd8 && prefix[2] == 0xff
	case "webp":
		valid = len(prefix) >= 12 && string(prefix[:4]) == "RIFF" && string(prefix[8:12]) == "WEBP"
	case "mp3":
		valid = bytes.HasPrefix(prefix, []byte("ID3")) ||
			(len(prefix) >= 2 && prefix[0] == 0xff && prefix[1]&0xe0 == 0xe0)
	case "wav":
		valid = len(prefix) >= 12 && string(prefix[:4]) == "RIFF" && string(prefix[8:12]) == "WAVE"
	case "m4a":
		valid = len(prefix) >= 12 && string(prefix[4:8]) == "ftyp" && validM4ABrand(string(prefix[8:12]))
	}
	if !valid {
		return fmt.Errorf("%w: content does not match declared format %q", ErrUploadMismatch, format)
	}
	return nil
}

func validOOXML(reader io.ReaderAt, size int64, primaryPart string) bool {
	archive, err := zip.NewReader(reader, size)
	if err != nil || len(archive.File) == 0 || len(archive.File) > 10_000 {
		return false
	}
	hasTypes, hasPrimary := false, false
	for _, file := range archive.File {
		switch file.Name {
		case "[Content_Types].xml":
			hasTypes = true
		case primaryPart:
			hasPrimary = true
		}
	}
	return hasTypes && hasPrimary
}

func validM4ABrand(brand string) bool {
	switch brand {
	case "M4A ", "M4B ", "mp42", "isom":
		return true
	default:
		return false
	}
}
