package moodle

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexeyLars/go-moodle/internal/config"
	"github.com/AlexeyLars/go-moodle/internal/testmoodle"
)

func fixtureClient(t *testing.T) (*Client, *testmoodle.Fixture) {
	t.Helper()
	f := testmoodle.New(t)
	c := NewClient(config.Config{URL: f.Server.URL + "/moodle/webservice/rest/server.php", Token: "local-test-token"})
	t.Cleanup(c.Close)
	return c, f
}

func TestAuthenticate(t *testing.T) {
	_, fixture := fixtureClient(t)
	cfg, err := Authenticate(t.Context(), fixture.Server.URL+"/moodle/", "student", "local-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != fixture.Server.URL+"/moodle/webservice/rest/server.php" || cfg.Token != "local-test-token" {
		t.Fatal("wrong credentials returned")
	}
	_, err = Authenticate(t.Context(), fixture.Server.URL+"/moodle", "student", "incorrect")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "invalidlogin" {
		t.Fatalf("expected invalidlogin, got %v", err)
	}
}

func TestHTTPFailuresAndSecretRedaction(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, body, code string
		status           int
	}{
		{"bad json", "<html>Login</html>", "invalid_json", 200},
		{"trailing json", `{"userid":1} {"extra":true}`, "invalid_json", 200},
		{"upstream failure", "secret=private-value", "http_error", 503},
		{"API error", `{"errorcode":"bad-private-value","message":"your token is private-value"}`, "bad-[redacted]", 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.RawQuery != "" || r.Method != "POST" {
					t.Error("credentials placed in URL")
				}
				_ = r.ParseForm()
				if r.Form.Get("wstoken") != "private-value" {
					t.Error("missing form token")
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer srv.Close()
			c := NewClient(config.Config{URL: srv.URL, Token: "private-value"})
			defer c.Close()
			_, err := c.Call(t.Context(), "example", nil)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Code != test.code {
				t.Fatalf("unexpected error %v", err)
			}
			if strings.Contains(err.Error(), "private-value") {
				t.Fatal("credential leaked")
			}
		})
	}
}

func TestRedirectDoesNotForwardCredentials(t *testing.T) {
	t.Parallel()
	var forwarded atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	c := NewClient(config.Config{URL: source.URL, Token: "private-value"})
	defer c.Close()
	_, err := c.Call(t.Context(), "example", nil)
	if err == nil || forwarded.Load() != 0 {
		t.Fatal("redirect followed")
	}
}

func TestCancellationAndConcurrencyLimit(t *testing.T) {
	t.Parallel()
	started := make(chan struct{}, 20)
	release := make(chan struct{})
	var current, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active := current.Add(1)
		defer current.Add(-1)
		for old := peak.Load(); active > old; old = peak.Load() {
			if peak.CompareAndSwap(old, active) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-release:
			_, _ = w.Write([]byte(`{"ok":true}`))
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	c := NewClient(config.Config{URL: srv.URL, Token: "private-value"})
	defer c.Close()
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.Call(ctx, "example", nil) }()
	}
	for range 4 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("requests did not start")
		}
	}
	cancel()
	close(release)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("requests did not stop after cancellation")
	}
	if peak.Load() > 4 {
		t.Fatalf("parallel limit exceeded: %d", peak.Load())
	}
	_, err := c.Call(ctx, "cancelled", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestSessionDeduplicationAndFreshness(t *testing.T) {
	client, fixture := fixtureClient(t)
	session := NewSession(client)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := session.call(t.Context(), "core_webservice_get_site_info", url.Values{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if count := fixture.Count("core_webservice_get_site_info"); count != 1 {
		t.Fatalf("expected 1 request, got %d", count)
	}
	if _, err := NewSession(client).call(t.Context(), "core_webservice_get_site_info", nil); err != nil {
		t.Fatal(err)
	}
	if count := fixture.Count("core_webservice_get_site_info"); count != 2 {
		t.Fatalf("cached across user requests: %d", count)
	}
}
