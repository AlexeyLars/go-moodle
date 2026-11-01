package config

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	URL   string
	Token string
}

// NormalizeURL accepts a Moodle root (including a subdirectory) or REST endpoint
func NormalizeURL(raw string) (base, endpoint string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return "", "", errors.New("укажи полный URL Moodle, например https://moodle.example.edu")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", errors.New("URL Moodle не должен содержать пароль, параметры запроса или фрагмент")
	}
	local := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		local = ip.IsLoopback()
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return "", "", errors.New("для Moodle требуется HTTPS (HTTP разрешён только на localhost)")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.Path = strings.TrimSuffix(u.Path, "/webservice/rest/server.php")
	u.RawPath = ""
	base = strings.TrimRight(u.String(), "/")
	return base, base + "/webservice/rest/server.php", nil
}

// ReadEnv parses data only: shell expansion and command substitution are never used
func ReadEnv(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimPrefix(text, "export ")
		key, value, ok := strings.Cut(text, "=")
		if !ok {
			return nil, fmt.Errorf("строка %d в .env: ожидается KEY=VALUE", line)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if strings.HasPrefix(value, "\"") {
			decoded, err := strconv.Unquote(value)
			if err != nil {
				return nil, fmt.Errorf("строка %d в .env: некорректные кавычки", line)
			}
			value = decoded
		} else if strings.HasPrefix(value, "'") {
			if len(value) < 2 || !strings.HasSuffix(value, "'") {
				return nil, fmt.Errorf("строка %d в .env: некорректные кавычки", line)
			}
			value = value[1 : len(value)-1]
		} else if i := strings.Index(value, " #"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		}
		values[key] = value
	}
	return values, scanner.Err()
}

func Load(path string, explicit bool) (Config, error) {
	values, err := ReadEnv(path)
	if err != nil && (!os.IsNotExist(err) || explicit) {
		return Config{}, fmt.Errorf("не удалось прочитать .env: %w", err)
	}
	if values == nil {
		values = map[string]string{}
	}
	// An explicitly selected account file takes precedence over ambient credentials
	for _, name := range []string{"MOODLE_URL", "MOODLE_TOKEN"} {
		if value := os.Getenv(name); value != "" && (!explicit || values[name] == "") {
			values[name] = value
		}
	}
	_, endpoint, err := NormalizeURL(values["MOODLE_URL"])
	if err != nil {
		return Config{}, fmt.Errorf("%w; сначала запусти go-moodle setup", err)
	}
	if strings.TrimSpace(values["MOODLE_TOKEN"]) == "" {
		return Config{}, errors.New("MOODLE_TOKEN не задан; сначала запусти go-moodle setup")
	}
	return Config{URL: endpoint, Token: values["MOODLE_TOKEN"]}, nil
}

// Save writes a private file atomically, and never overwrites without --force
func Save(path string, cfg Config, force bool) error {
	if !force {
		if _, err := os.Lstat(path); err == nil {
			return errors.New(".env уже существует; для замены используй --force")
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data := []byte("MOODLE_URL=" + strconv.Quote(cfg.URL) + "\nMOODLE_TOKEN=" + strconv.Quote(cfg.Token) + "\n")
	tmp, err := os.CreateTemp(filepath.Dir(path), ".moodle-env-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if force {
		return os.Rename(tmp.Name(), path)
	}
	// Link is atomic and fails if a concurrent setup created the destination
	return os.Link(tmp.Name(), path)
}
