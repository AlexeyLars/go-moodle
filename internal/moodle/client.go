package moodle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AlexeyLars/go-moodle/internal/config"
)

const maxResponse = 32 << 20

type Client struct {
	endpoint string
	token    string
	http     *http.Client
	slots    chan struct{}
}

type APIError struct {
	Code     string
	Function string
	Message  string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Moodle [%s] %s: %s", e.Code, e.Function, e.Message)
}

func NewClient(cfg config.Config) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 6
	return &Client{
		endpoint: cfg.URL,
		token:    cfg.Token,
		slots:    make(chan struct{}, 4),
		http: &http.Client{
			Timeout:       30 * time.Second,
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) post(ctx context.Context, endpoint, function string, fields url.Values) (any, error) {
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(fields.Encode()))
	if err != nil {
		return nil, errors.New("не удалось создать запрос к Moodle")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "github.com/AlexeyLars/go-moodle/1.0")
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Do not include the request, response body, credentials, or URL in errors
		return nil, &APIError{"network_error", function, "не удалось выполнить HTTPS-запрос"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, &APIError{"http_error", function, fmt.Sprintf("HTTP %d", response.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil {
		return nil, &APIError{"read_error", function, "не удалось прочитать ответ"}
	}
	if len(body) > maxResponse {
		return nil, &APIError{"response_too_large", function, "ответ превышает 32 MiB"}
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, &APIError{"invalid_json", function, "сервер вернул не JSON; проверь URL и способ входа"}
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, &APIError{"invalid_json", function, "лишние данные после JSON"}
	}
	if object, ok := value.(map[string]any); ok && (truth(object["errorcode"]) || truth(object["exception"]) || truth(object["error"])) {
		code := str(object["errorcode"])
		if code == "" {
			code = "api_error"
		}
		message := str(object["message"])
		if message == "" {
			message = str(object["error"])
		}
		for _, key := range []string{"password", "token", "wstoken"} {
			if secret := fields.Get(key); secret != "" {
				code = strings.ReplaceAll(code, secret, "[redacted]")
				message = strings.ReplaceAll(message, secret, "[redacted]")
			}
		}
		return nil, &APIError{short(code, 100), function, short(message, 400)}
	}
	return value, nil
}

func (c *Client) Call(ctx context.Context, function string, params url.Values) (any, error) {
	fields := url.Values{"wstoken": {c.token}, "wsfunction": {function}, "moodlewsrestformat": {"json"}}
	for key, values := range params {
		for _, value := range values {
			fields.Add(key, value)
		}
	}
	return c.post(ctx, c.endpoint, function, fields)
}

func Authenticate(ctx context.Context, rawURL, username, password string) (config.Config, error) {
	base, endpoint, err := config.NormalizeURL(rawURL)
	if err != nil {
		return config.Config{}, err
	}
	client := NewClient(config.Config{URL: endpoint})
	defer client.Close()
	value, err := client.post(ctx, base+"/login/token.php", "login", url.Values{
		"username": {username}, "password": {password}, "service": {"moodle_mobile_app"},
	})
	if err != nil {
		return config.Config{}, err
	}
	token := str(obj(value)["token"])
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return config.Config{}, errors.New("Moodle не вернул мобильный токен")
	}
	client.token = token
	info, err := client.Call(ctx, "core_webservice_get_site_info", nil)
	if err != nil {
		return config.Config{}, err
	}
	if integer(obj(info)["userid"]) <= 0 {
		return config.Config{}, errors.New("Moodle не подтвердил пользователя")
	}
	return config.Config{URL: endpoint, Token: token}, nil
}
