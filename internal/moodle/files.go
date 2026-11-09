package moodle

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path"
	"strings"
	"unicode/utf8"
)

const maxFileBytes = 16 << 20
const maxExtractedBytes = 32 << 20

// fileURL never accepts arbitrary hosts or non-file Moodle endpoints. The token
// goes in the POST body and is never returned to the caller or put in a URL.
func (c *Client) fileURL(raw string) (*url.URL, error) {
	base, err := url.Parse(c.endpoint)
	if err != nil {
		return nil, errors.New("invalid Moodle endpoint")
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || u.Scheme != base.Scheme || !strings.EqualFold(u.Host, base.Host) {
		return nil, errors.New("fileurl must be a file URL on the configured Moodle site")
	}
	root := strings.TrimSuffix(base.Path, "/webservice/rest/server.php")
	prefix := root + "/webservice/pluginfile.php/"
	if strings.HasPrefix(u.Path, root+"/pluginfile.php/") {
		u.Path = prefix + strings.TrimPrefix(u.Path, root+"/pluginfile.php/")
		u.RawPath = ""
	}
	if !strings.HasPrefix(u.Path, prefix) || strings.TrimPrefix(u.Path, prefix) == "" {
		return nil, errors.New("fileurl must point to pluginfile.php")
	}
	// Reject decoded dot segments, backslashes and double-escaped paths.
	if strings.ContainsAny(u.Path, "\\%\x00\r\n") {
		return nil, errors.New("invalid file path")
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return nil, errors.New("invalid file path")
		}
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errors.New("invalid file query")
	}
	// Retain only Moodle download options, removing supplied credentials.
	safe := url.Values{}
	for _, key := range []string{"forcedownload", "preview", "revision"} {
		if v := q.Get(key); v != "" {
			safe.Set(key, v)
		}
	}
	u.RawQuery = safe.Encode()
	return u, nil
}

func (c *Client) fetchFile(ctx context.Context, raw string) ([]byte, string, error) {
	u, err := c.fileURL(raw)
	if err != nil {
		return nil, "", err
	}
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(url.Values{"token": {c.token}}.Encode()))
	if err != nil {
		return nil, "", errors.New("could not create file request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "github.com/AlexeyLars/go-moodle/1.0")
	r, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", errors.New("Moodle file download failed")
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("Moodle file download: HTTP %d", r.StatusCode)
	}
	if r.ContentLength > maxFileBytes {
		return nil, "", errors.New("file exceeds 16 MiB")
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, maxFileBytes+1))
	if err != nil {
		return nil, "", errors.New("could not read Moodle file")
	}
	if len(b) > maxFileBytes {
		return nil, "", errors.New("file exceeds 16 MiB")
	}
	var api Object
	if json.Unmarshal(b, &api) == nil && truth(api["exception"]) && truth(api["errorcode"]) {
		return nil, "", errors.New("Moodle refused file download; check token and service file-download permission")
	}
	return b, r.Header.Get("Content-Type"), nil
}

// ReadFile returns a bounded text window or base64 window for binary consumers.
// Offsets are Unicode characters in text mode and bytes in base64 mode.
func (s *Session) ReadFile(ctx context.Context, fileURL, mode string, offset, limit int) (Object, error) {
	if mode == "" {
		mode = "text"
	}
	if mode != "text" && mode != "base64" {
		return nil, errors.New("mode must be text or base64")
	}
	if offset < 0 || limit < 0 || limit > 100000 {
		return nil, errors.New("offset must be nonnegative and limit between 0 and 100000")
	}
	if limit == 0 {
		limit = 20000
	}
	b, mime, err := s.client.fetchFile(ctx, fileURL)
	if err != nil {
		return nil, err
	}
	u, _ := s.client.fileURL(fileURL)
	name := path.Base(u.Path)
	result := Object{"filename": name, "mimetype": mime, "size_bytes": len(b), "mode": mode, "offset": offset}
	if mode == "base64" {
		if offset > len(b) {
			return nil, errors.New("offset exceeds file size")
		}
		end := min(offset+limit, len(b))
		result["data"] = base64.StdEncoding.EncodeToString(b[offset:end])
		result["next_offset"], result["has_more"] = end, end < len(b)
		return result, nil
	}
	text, err := extractFileText(ctx, name, b)
	if err != nil {
		return nil, err
	}
	chars := []rune(text)
	if offset > len(chars) {
		return nil, errors.New("offset exceeds extracted text length")
	}
	end := min(offset+limit, len(chars))
	result["text"], result["total_characters"] = string(chars[offset:end]), len(chars)
	result["next_offset"], result["has_more"] = end, end < len(chars)
	if len(strings.TrimSpace(text)) == 0 {
		result["warning"] = "No text extracted; scanned documents require OCR"
	}
	return result, nil
}

func extractFileText(ctx context.Context, name string, b []byte) (string, error) {
	switch strings.ToLower(path.Ext(name)) {
	case ".docx":
		return docxText(b)
	case ".pdf":
		if !bytes.HasPrefix(b, []byte("%PDF-")) {
			return "", errors.New("invalid PDF file")
		}
		bin, err := exec.LookPath("pdftotext")
		if err != nil {
			return "", errors.New("PDF text extraction requires Poppler pdftotext on PATH; use mode=base64 to read the original file")
		}
		cmd := exec.CommandContext(ctx, bin, "-layout", "-enc", "UTF-8", "-", "-")
		cmd.Stdin = bytes.NewReader(b)
		out := &boundedText{}
		cmd.Stdout = out
		// Do not expose stderr: it can contain untrusted document content.
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", errors.New("PDF extraction failed or text exceeds 32 MiB")
		}
		return out.String(), nil
	case ".html", ".htm":
		if !utf8.Valid(b) {
			return "", errors.New("text file must be UTF-8")
		}
		return html.UnescapeString(cleanHTML(string(b))), nil
	case ".txt", ".md", ".csv", ".json", ".xml", ".log":
		if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
			return "", errors.New("text file must be UTF-8")
		}
		return strings.TrimPrefix(string(b), "\ufeff"), nil
	default:
		return "", errors.New("unsupported text format; use mode=base64 to read the original file (legacy DOC/PPT and scanned PDFs need conversion/OCR)")
	}
}

type boundedText struct{ buffer bytes.Buffer }

func (w *boundedText) String() string { return w.buffer.String() }

func (w *boundedText) Write(p []byte) (int, error) {
	if len(p) > maxExtractedBytes-w.buffer.Len() {
		return 0, errors.New("extracted text exceeds 32 MiB")
	}
	return w.buffer.Write(p)
}

func docxText(b []byte) (string, error) {
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return "", errors.New("invalid DOCX archive")
	}
	for _, f := range z.File {
		if f.Name != "word/document.xml" {
			continue
		}
		if f.UncompressedSize64 > maxExtractedBytes {
			return "", errors.New("DOCX document XML exceeds 32 MiB")
		}
		r, err := f.Open()
		if err != nil {
			return "", errors.New("could not open DOCX document XML")
		}
		defer r.Close()
		data, err := io.ReadAll(io.LimitReader(r, maxExtractedBytes+1))
		if err != nil || len(data) > maxExtractedBytes {
			return "", errors.New("could not read DOCX document XML within 32 MiB")
		}
		d := xml.NewDecoder(bytes.NewReader(data))
		var out strings.Builder
		inText := false
		const ns = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
		for {
			t, err := d.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", errors.New("invalid DOCX document XML")
			}
			switch v := t.(type) {
			case xml.StartElement:
				if v.Name.Space != ns {
					continue
				}
				if v.Name.Local == "t" {
					inText = true
				}
				if v.Name.Local == "tab" {
					out.WriteByte('\t')
				}
				if v.Name.Local == "br" {
					out.WriteByte('\n')
				}
			case xml.EndElement:
				if v.Name.Space != ns {
					continue
				}
				if v.Name.Local == "t" {
					inText = false
				}
				if v.Name.Local == "p" || v.Name.Local == "tr" {
					out.WriteByte('\n')
				}
				if v.Name.Local == "tc" {
					out.WriteByte('\t')
				}
			case xml.CharData:
				if inText {
					out.Write(v)
				}
			}
		}
		return strings.TrimSpace(out.String()), nil
	}
	return "", errors.New("DOCX archive has no word/document.xml")
}
