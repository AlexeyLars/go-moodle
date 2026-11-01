package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeURL(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input, base string
		bad         bool
	}{
		{"https://moodle.example.edu/", "https://moodle.example.edu", false},
		{"https://host.edu/moodle/", "https://host.edu/moodle", false},
		{"https://host.edu/moodle/webservice/rest/server.php", "https://host.edu/moodle", false},
		{"http://127.0.0.1:8000/moodle", "http://127.0.0.1:8000/moodle", false},
		{"http://host.edu", "", true}, {"https://name:password@host.edu", "", true}, {"https://host.edu?token=x", "", true},
		{"https://host.edu#x", "", true}, {"not a URL", "", true}, {"ftp://host.edu", "", true},
	} {
		t.Run(test.input, func(t *testing.T) {
			base, endpoint, err := NormalizeURL(test.input)
			if test.bad {
				if err == nil {
					t.Fatal("accepted unsafe URL")
				}
				return
			}
			if err != nil || base != test.base || endpoint != test.base+"/webservice/rest/server.php" {
				t.Fatalf("normalization: %s %s %v", base, endpoint, err)
			}
		})
	}
}
func TestPrivateEnvRoundTrip(t *testing.T) {
	t.Setenv("MOODLE_URL", "https://wrong.example")
	t.Setenv("MOODLE_TOKEN", "ambient-token")
	path := filepath.Join(t.TempDir(), ".env")
	want := Config{URL: "https://host.edu/moodle/webservice/rest/server.php", Token: `quote"slash\and$()кириллица`}
	if err := Save(path, want, false); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path, true)
	if err != nil || got != want {
		t.Fatalf("round trip mismatch: %v", err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Mode().Perm()&0077 != 0 {
		t.Fatal("file readable by other users")
	}
	if err := Save(path, Config{URL: want.URL, Token: "new"}, false); err == nil {
		t.Fatal("overwrote existing credentials")
	}
	bytes, _ := os.ReadFile(path)
	if strings.Contains(string(bytes), "PASSWORD") {
		t.Fatal("unexpected password")
	}
	if err := Save(path, Config{URL: want.URL, Token: "new"}, true); err != nil {
		t.Fatal(err)
	}
	got, err = Load(path, true)
	if err != nil || got.Token != "new" {
		t.Fatal("replacement failed")
	}
}
func TestEnvParsing(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("export MOODLE_URL='https://host.edu' \nMOODLE_TOKEN=abc # comment\n"), 0600); err != nil {
		t.Fatal(err)
	}
	values, err := ReadEnv(path)
	if err != nil || values["MOODLE_TOKEN"] != "abc" {
		t.Fatalf("parse: %v", err)
	}
	if err := os.WriteFile(path, []byte("MOODLE_TOKEN=\"unclosed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEnv(path); err == nil {
		t.Fatal("accepted malformed credentials")
	}
}
