package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

const defaultConfigFile = "vibebot.yaml"

type runtimeOptions struct {
	ConfigPath   string
	DBPath       string
	SeedDir      string
	Tick         time.Duration
	LLMProvider  string
	GeminiModel  string
	GeminiAPIKey string
	IRC          ircOptions
	MCPStdio     bool
}

type ircOptions struct {
	Server   string
	Port     int
	TLS      bool
	Nick     string
	Channel  string
	SASLUser string
	SASLPass string
}

type fileConfig struct {
	DB           string        `yaml:"db"`
	Seed         string        `yaml:"seed"`
	Tick         string        `yaml:"tick"`
	LLM          string        `yaml:"llm"`
	GeminiModel  string        `yaml:"gemini_model"`
	GeminiAPIKey string        `yaml:"gemini_api_key"`
	IRC          fileIRCConfig `yaml:"irc"`
	MCP          fileMCPConfig `yaml:"mcp"`
}

type fileIRCConfig struct {
	Server  string           `yaml:"server"`
	Port    int              `yaml:"port"`
	TLS     *bool            `yaml:"tls"`
	Nick    string           `yaml:"nick"`
	Channel string           `yaml:"channel"`
	SASL    *fileIRCSASLAuth `yaml:"sasl"`
}

// fileIRCSASLAuth holds the SASL PLAIN credentials. Putting a password in a
// committed config file is a footgun; VIBEBOT_SASL_PASSWORD in the env wins
// over Pass when both are set so secrets can live outside the repo.
type fileIRCSASLAuth struct {
	User string `yaml:"user"`
	Pass string `yaml:"pass"`
}

type fileMCPConfig struct {
	Stdio *bool `yaml:"stdio"`
}

type runtimeFlagValues struct {
	configPath   *string
	dbPath       *string
	seedDir      *string
	tick         *time.Duration
	llmProvider  *string
	geminiModel  *string
	geminiAPIKey *string
	ircServer    *string
	ircPort      *int
	ircTLS       *bool
	ircNick      *string
	ircChannel   *string
	ircSASLUser  *string
	ircSASLPass  *string
	mcpStdio     *bool
}

func defaultRuntimeOptions() runtimeOptions {
	return runtimeOptions{
		DBPath:      "vibebot.db",
		SeedDir:     "seed",
		Tick:        2 * time.Minute,
		LLMProvider: "echo",
		GeminiModel: "gemini-flash-lite-latest",
		IRC: ircOptions{
			Port:    6667,
			Nick:    "vibebot",
			Channel: "#vibebot",
		},
	}
}

func parseRuntimeOptions(args []string, cwd string) (runtimeOptions, error) {
	opts := defaultRuntimeOptions()

	fs := flag.NewFlagSet("sim", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	flags := bindRuntimeFlags(fs, opts)
	if err := fs.Parse(args); err != nil {
		return runtimeOptions{}, err
	}

	explicit := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	resolvedConfig := *flags.configPath
	configWasExplicit := explicit["config"]
	if resolvedConfig == "" {
		resolvedConfig = filepath.Join(cwd, defaultConfigFile)
	}
	opts.ConfigPath = resolvedConfig
	if err := applyConfigFile(&opts, resolvedConfig, configWasExplicit); err != nil {
		return runtimeOptions{}, err
	}

	if explicit["db"] {
		opts.DBPath = *flags.dbPath
	}
	if explicit["seed"] {
		opts.SeedDir = *flags.seedDir
	}
	if explicit["tick"] {
		opts.Tick = *flags.tick
	}
	if explicit["llm"] {
		opts.LLMProvider = *flags.llmProvider
	}
	if explicit["gemini-model"] {
		opts.GeminiModel = *flags.geminiModel
	}
	if explicit["irc-server"] {
		opts.IRC.Server = *flags.ircServer
	}
	if explicit["irc-port"] {
		opts.IRC.Port = *flags.ircPort
	}
	if explicit["irc-tls"] {
		opts.IRC.TLS = *flags.ircTLS
	}
	if explicit["irc-nick"] {
		opts.IRC.Nick = *flags.ircNick
	}
	if explicit["irc-channel"] {
		opts.IRC.Channel = *flags.ircChannel
	}
	if explicit["irc-sasl-user"] {
		opts.IRC.SASLUser = *flags.ircSASLUser
	}
	if explicit["irc-sasl-pass"] {
		opts.IRC.SASLPass = *flags.ircSASLPass
	}
	if explicit["mcp-stdio"] {
		opts.MCPStdio = *flags.mcpStdio
	}
	if explicit["gemini-api-key"] {
		opts.GeminiAPIKey = *flags.geminiAPIKey
	}

	// Env overrides for secrets, so users can keep them out of the file/flags.
	if v := os.Getenv("VIBEBOT_SASL_PASSWORD"); v != "" {
		opts.IRC.SASLPass = v
	}
	if v := os.Getenv("GEMINI_API_KEY"); v != "" {
		opts.GeminiAPIKey = v
	}

	return opts, nil
}

func bindRuntimeFlags(fs *flag.FlagSet, opts runtimeOptions) runtimeFlagValues {
	return runtimeFlagValues{
		configPath:   fs.String("config", "", "path to YAML runtime config"),
		dbPath:       fs.String("db", opts.DBPath, "path to SQLite event store (':memory:' allowed)"),
		seedDir:      fs.String("seed", opts.SeedDir, "directory containing characters.yaml and groups.yaml"),
		tick:         fs.Duration("tick", opts.Tick, "world ticker interval"),
		llmProvider:  fs.String("llm", opts.LLMProvider, "LLM provider: echo|gemini"),
		geminiModel:  fs.String("gemini-model", opts.GeminiModel, "Gemini model id"),
		geminiAPIKey: fs.String("gemini-api-key", opts.GeminiAPIKey, "Gemini API key (env GEMINI_API_KEY overrides)"),
		ircServer:    fs.String("irc-server", opts.IRC.Server, "IRC server (omit to disable IRC)"),
		ircPort:      fs.Int("irc-port", opts.IRC.Port, "IRC port"),
		ircTLS:       fs.Bool("irc-tls", opts.IRC.TLS, "use TLS for IRC"),
		ircNick:      fs.String("irc-nick", opts.IRC.Nick, "IRC nick"),
		ircChannel:   fs.String("irc-channel", opts.IRC.Channel, "IRC channel"),
		ircSASLUser:  fs.String("irc-sasl-user", opts.IRC.SASLUser, "SASL PLAIN username (enables SASL when set)"),
		ircSASLPass:  fs.String("irc-sasl-pass", opts.IRC.SASLPass, "SASL PLAIN password (env VIBEBOT_SASL_PASSWORD overrides)"),
		mcpStdio:     fs.Bool("mcp-stdio", opts.MCPStdio, "run an MCP server over stdin/stdout instead of IRC"),
	}
}

func printRuntimeUsage(w io.Writer) {
	fs := flag.NewFlagSet("sim", flag.ContinueOnError)
	fs.SetOutput(w)
	bindRuntimeFlags(fs, defaultRuntimeOptions())
	if _, err := fmt.Fprintln(w, "Usage of sim:"); err != nil {
		return
	}
	fs.PrintDefaults()
}

func applyConfigFile(opts *runtimeOptions, path string, explicit bool) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !explicit {
			return nil
		}
		return fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg fileConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.DB != "" {
		opts.DBPath = cfg.DB
	}
	if cfg.Seed != "" {
		opts.SeedDir = cfg.Seed
	}
	if cfg.Tick != "" {
		d, err := time.ParseDuration(cfg.Tick)
		if err != nil {
			return fmt.Errorf("parse config tick %q: %w", cfg.Tick, err)
		}
		opts.Tick = d
	}
	if cfg.LLM != "" {
		opts.LLMProvider = cfg.LLM
	}
	if cfg.GeminiModel != "" {
		opts.GeminiModel = cfg.GeminiModel
	}
	if cfg.GeminiAPIKey != "" {
		opts.GeminiAPIKey = cfg.GeminiAPIKey
	}
	if cfg.IRC.Server != "" {
		opts.IRC.Server = cfg.IRC.Server
	}
	if cfg.IRC.Port != 0 {
		opts.IRC.Port = cfg.IRC.Port
	}
	if cfg.IRC.TLS != nil {
		opts.IRC.TLS = *cfg.IRC.TLS
	}
	if cfg.IRC.Nick != "" {
		opts.IRC.Nick = cfg.IRC.Nick
	}
	if cfg.IRC.Channel != "" {
		opts.IRC.Channel = cfg.IRC.Channel
	}
	if cfg.IRC.SASL != nil {
		if cfg.IRC.SASL.User != "" {
			opts.IRC.SASLUser = cfg.IRC.SASL.User
		}
		if cfg.IRC.SASL.Pass != "" {
			opts.IRC.SASLPass = cfg.IRC.SASL.Pass
		}
	}
	if cfg.MCP.Stdio != nil {
		opts.MCPStdio = *cfg.MCP.Stdio
	}
	return nil
}
