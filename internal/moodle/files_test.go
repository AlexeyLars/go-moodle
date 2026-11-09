package moodle

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/AlexeyLars/go-moodle/internal/config"
)

func makeDOCX(t *testing.T, xml string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(f, xml); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestReadFilePaginationAndAuthentication(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Method != "POST" || r.PostForm.Get("token") != "secret" || r.URL.Query().Get("token") != "" {
			t.Error("credentials not confined to POST body")
		}
		if !strings.HasPrefix(r.URL.Path, "/moodle/webservice/pluginfile.php/") {
			t.Error("wrong endpoint")
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "А😀БВ")
	}))
	defer server.Close()
	c := NewClient(config.Config{URL: server.URL + "/moodle/webservice/rest/server.php", Token: "secret"})
	defer c.Close()
	s := NewSession(c)
	u := server.URL + "/moodle/pluginfile.php/1/lecture.txt?token=untrusted&forcedownload=1"
	r, err := s.ReadFile(t.Context(), u, "", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if r["text"] != "😀Б" || r["next_offset"] != 3 || r["has_more"] != true || r["total_characters"] != 4 {
		t.Fatalf("bad Unicode page: %v", r)
	}
	r, err = s.ReadFile(t.Context(), u, "base64", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	b, err := base64.StdEncoding.DecodeString(str(r["data"]))
	if err != nil || string(b) != "А" {
		t.Fatal("binary window changed bytes")
	}
	for _, tc := range []struct {
		mode          string
		offset, limit int
	}{{"bad", 0, 1}, {"text", -1, 1}, {"text", 0, -1}, {"text", 0, 100001}} {
		if _, err := s.ReadFile(t.Context(), u, tc.mode, tc.offset, tc.limit); err == nil {
			t.Fatal("accepted invalid input")
		}
	}
	if requests != 2 {
		t.Fatalf("invalid inputs performed network requests: %d", requests)
	}
	if _, err := s.ReadFile(t.Context(), u, "text", 5, 1); err == nil {
		t.Fatal("accepted offset beyond end")
	}
}

func TestFileURLBoundaries(t *testing.T) {
	c := NewClient(config.Config{URL: "https://school.example/moodle/webservice/rest/server.php", Token: "secret"})
	defer c.Close()
	for _, raw := range []string{
		"https://other.example/moodle/webservice/pluginfile.php/1/a.txt",
		"http://school.example/moodle/webservice/pluginfile.php/1/a.txt",
		"https://school.example/other/webservice/pluginfile.php/1/a.txt",
		"https://school.example/moodle/webservice/rest/server.php",
		"https://user:secret@school.example/moodle/webservice/pluginfile.php/1/a.txt",
		"https://school.example/moodle/webservice/pluginfile.php/../rest/server.php",
		"https://school.example/moodle/webservice/pluginfile.php/%2e%2e/rest/server.php",
		"https://school.example/moodle/webservice/pluginfile.php/%252e%252e/a.txt",
		"https://school.example/moodle/webservice/pluginfile.php/1%5ca.txt",
		"https://school.example/moodle/webservice/pluginfile.php/1/a.txt#fragment",
		"/moodle/webservice/pluginfile.php/1/a.txt",
	} {
		if _, err := c.fileURL(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	u, err := c.fileURL("https://school.example/moodle/pluginfile.php/1/Лекция.pdf?forcedownload=1&token=bad&wstoken=bad")
	if err != nil || u.Query().Get("forcedownload") != "1" || strings.Contains(u.String(), "token") {
		t.Fatalf("bad normalized URL: %v %v", u, err)
	}
}

func TestFileDownloadFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		length string
	}{
		{"redirect", 302, "", ""}, {"forbidden", 403, "secret", ""},
		{"MoodleJSON", 200, `{"exception":"error","message":"secret","errorcode":"secret"}`, ""},
		{"length", 200, "", "16777217"},
		{"chunked", 200, strings.Repeat("x", maxFileBytes+1), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.length != "" {
					w.Header().Set("Content-Length", tc.length)
				}
				w.Header().Set("Location", "https://other.example/?token=secret")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			c := NewClient(config.Config{URL: server.URL + "/webservice/rest/server.php", Token: "secret"})
			defer c.Close()
			_, _, err := c.fetchFile(t.Context(), server.URL+"/webservice/pluginfile.php/1/file.txt")
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe error: %v", err)
			}
		})
	}
}

func TestDocumentExtraction(t *testing.T) {
	xml := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Привет &amp; мир</w:t><w:tab/><w:t>😀</w:t><w:br/><w:t>Строка</w:t></w:r></w:p><w:tbl><w:tr><w:tc><w:p><w:r><w:t>Ячейка</w:t></w:r></w:p></w:tc></w:tr></w:tbl></w:body></w:document>`
	got, err := extractFileText(t.Context(), "test.docx", makeDOCX(t, xml))
	if err != nil || !strings.HasPrefix(got, "Привет & мир\t😀\nСтрока\n") || !strings.Contains(got, "Ячейка") {
		t.Fatalf("DOCX text: %q %v", got, err)
	}
	got, err = extractFileText(t.Context(), "test.html", []byte("<p>А &amp; Б</p>"))
	if err != nil || got != "А & Б" {
		t.Fatalf("HTML text: %q %v", got, err)
	}
	for _, tc := range []struct {
		name string
		b    []byte
	}{
		{"broken.docx", []byte("not zip")}, {"broken.docx", makeDOCX(t, "<broken>")},
		{"bad.txt", []byte{0xff}}, {"bad.txt", []byte{0}}, {"legacy.doc", []byte("legacy")},
		{"bad.pdf", []byte("<html>login required</html>")},
	} {
		if _, err := extractFileText(t.Context(), tc.name, tc.b); err == nil {
			t.Errorf("accepted invalid %s", tc.name)
		}
	}
}

func TestPDFMissingDependencyAndBoundedOutput(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := extractFileText(t.Context(), "lecture.pdf", []byte("%PDF-1.4")); err == nil || !strings.Contains(err.Error(), "pdftotext") {
		t.Fatalf("missing dependency: %v", err)
	}
	w := &boundedText{}
	if _, err := io.Copy(w, strings.NewReader(strings.Repeat("x", maxExtractedBytes+1))); err == nil {
		t.Fatal("output limit bypassed")
	}
	if w.buffer.Len() > maxExtractedBytes {
		t.Fatal("buffer exceeded bound")
	}
}

func TestFileCancellation(t *testing.T) {
	c := NewClient(config.Config{URL: "http://127.0.0.1/webservice/rest/server.php", Token: "secret"})
	defer c.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := c.fetchFile(ctx, "http://127.0.0.1/webservice/pluginfile.php/1/a.txt")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestPDFExtraction(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("Poppler pdftotext not installed")
	}
	b, err := os.ReadFile("testdata/lecture.pdf")
	if err != nil {
		t.Fatal(err)
	}
	got, err := extractFileText(t.Context(), "lecture.pdf", b)
	if err != nil || !strings.Contains(got, "PDF attachment test") {
		t.Fatalf("PDF extraction: %q %v", got, err)
	}
}
