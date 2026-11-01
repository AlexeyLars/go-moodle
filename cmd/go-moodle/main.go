package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/AlexeyLars/go-moodle/internal/config"
	"github.com/AlexeyLars/go-moodle/internal/moodle"
	"github.com/AlexeyLars/go-moodle/internal/server"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/term"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, in *os.File, out, errout io.Writer) error {
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	if command == "version" {
		fmt.Fprintln(out, "go-moodle "+server.Version)
		return nil
	}
	if command == "help" {
		printHelp(out)
		return nil
	}
	switch command {
	case "serve", "setup", "check", "config":
	default:
		return fmt.Errorf("неизвестная команда %q; используй help", command)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(errout)
	envFile := flags.String("env-file", ".env", "Путь к файлу MOODLE_URL/MOODLE_TOKEN")
	var rawURL, username *string
	var force *bool
	if command == "setup" {
		rawURL = flags.String("url", "", "Базовый URL Moodle (если не указан, будет запрошен)")
		username = flags.String("username", "", "Логин Moodle (если не указан, будет запрошен)")
		force = flags.Bool("force", false, "Заменить существующий .env после успешной проверки токена")
	}
	flags.Usage = func() { printHelp(errout); flags.PrintDefaults() }
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("лишние позиционные аргументы")
	}
	path, err := filepath.Abs(*envFile)
	if err != nil {
		return err
	}
	explicit := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "env-file" {
			explicit = true
		}
	})
	if command == "config" {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		value := map[string]any{"mcpServers": map[string]any{"moodle": map[string]any{"command": executable, "args": []string{"serve", "--env-file", path}}}}
		encoder := json.NewEncoder(out)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	}
	if command == "setup" {
		if _, err := os.Lstat(path); err == nil && !*force {
			return errors.New(".env уже существует; для замены используй setup --force")
		}
		if !term.IsTerminal(int(in.Fd())) {
			return errors.New("setup требует терминал для скрытого ввода пароля")
		}
		reader := bufio.NewReader(in)
		prompt := func(label string) (string, error) {
			fmt.Fprint(out, label)
			line, err := reader.ReadString('\n')
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(line), nil
		}
		if *rawURL == "" {
			*rawURL, err = prompt("URL Moodle: ")
			if err != nil {
				return err
			}
		}
		if _, _, err = config.NormalizeURL(*rawURL); err != nil {
			return err
		}
		if *username == "" {
			*username, err = prompt("Логин Moodle: ")
			if err != nil {
				return err
			}
		}
		if *username == "" {
			return errors.New("логин не должен быть пустым")
		}
		fmt.Fprint(out, "Пароль Moodle: ")
		password, err := term.ReadPassword(int(in.Fd()))
		fmt.Fprintln(out)
		if err != nil {
			return errors.New("не удалось прочитать пароль")
		}
		defer clear(password)
		if len(password) == 0 {
			return errors.New("пароль не должен быть пустым")
		}
		loginCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		cfg, err := moodle.Authenticate(loginCtx, *rawURL, *username, string(password))
		clear(password)
		if err != nil {
			return err
		}
		if err = config.Save(path, cfg, *force); err != nil {
			return err
		}
		fmt.Fprintf(out, "Токен получен и проверен. Настройки сохранены: %s\nПароль не сохранён.\n", path)
		fmt.Fprintln(out, "Для параметров подключения выполни: go-moodle config --env-file <путь-к-.env>")
		return nil
	}
	cfg, err := config.Load(path, explicit)
	if err != nil {
		return err
	}
	client := moodle.NewClient(cfg)
	defer client.Close()
	if command == "check" {
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		data, err := client.Call(checkCtx, "core_webservice_get_site_info", nil)
		if err != nil {
			return err
		}
		info, ok := data.(map[string]any)
		if !ok || info["userid"] == nil {
			return errors.New("Moodle не подтвердил пользователя")
		}
		fmt.Fprintf(out, "Соединение работает: %v. Токен принят.\n", info["sitename"])
		return nil
	}
	return server.New(client).Run(ctx, &mcp.StdioTransport{})
}

func printHelp(out io.Writer) {
	fmt.Fprintln(out, `Moodle MCP — локальный сервер на Go

Команды:
  serve                    MCP через stdin/stdout (по умолчанию)
  setup                    Запросить URL, логин и пароль, получить токен
  check                    Проверить подключение
  config                   Показать JSON-настройку для MCP-клиента
  version                  Показать версию

Примеры:
  go-moodle setup --env-file .env
  go-moodle serve --env-file /absolute/path/.env
  go-moodle check --env-file .env

Пароль вводится скрыто и не сохраняется. В .env сохраняется только URL и токен.
Прямой вход требует включённого Moodle mobile web service.
Для SSO можно сохранить существующий мобильный токен в .env вручную.`)
}
