package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexeyLars/go-moodle/internal/config"
	"github.com/AlexeyLars/go-moodle/internal/testmoodle"
)

func TestCLI(t *testing.T) {
	fixture := testmoodle.New(t)
	path := filepath.Join(t.TempDir(), ".env")
	if err := config.Save(path, config.Config{URL: fixture.Server.URL + "/moodle/webservice/rest/server.php", Token: "local-test-token"}, false); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"check", "config"} {
		var out, errout bytes.Buffer
		err := run(t.Context(), []string{command, "--env-file", path}, os.Stdin, &out, &errout)
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		if strings.Contains(out.String(), "local-test-token") {
			t.Fatal("token printed")
		}
		if command == "config" {
			var data any
			if err := json.Unmarshal(out.Bytes(), &data); err != nil {
				t.Fatal(err)
			}
		} else if !strings.Contains(out.String(), "Токен принят") {
			t.Fatal(out.String())
		}
	}
	var out bytes.Buffer
	err := run(context.Background(), []string{"setup", "--env-file", path}, os.Stdin, &out, &out)
	if err == nil || !strings.Contains(err.Error(), "существует") {
		t.Fatalf("setup must protect existing credentials: %v", err)
	}
	if err := run(t.Context(), []string{"unknown"}, os.Stdin, &out, &out); err == nil {
		t.Fatal("unknown command accepted")
	}
}
