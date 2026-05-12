package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeOptionsLoadsDefaultConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, defaultConfigFile)
	if err := os.WriteFile(cfg, []byte(`
db: persistent.db
seed: custom-seed
tick: 30s
llm: gemini
gemini_model: gemini-test
irc:
  server: irc.example.net
  port: 6697
  tls: true
  nick: botnick
  channel: "#bots"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	opts, err := parseRuntimeOptions(nil, dir)
	if err != nil {
		t.Fatal(err)
	}

	if opts.DBPath != "persistent.db" {
		t.Errorf("DBPath=%q", opts.DBPath)
	}
	if opts.SeedDir != "custom-seed" {
		t.Errorf("SeedDir=%q", opts.SeedDir)
	}
	if opts.Tick != 30*time.Second {
		t.Errorf("Tick=%s", opts.Tick)
	}
	if opts.LLMProvider != "gemini" {
		t.Errorf("LLMProvider=%q", opts.LLMProvider)
	}
	if opts.GeminiModel != "gemini-test" {
		t.Errorf("GeminiModel=%q", opts.GeminiModel)
	}
	if opts.IRC.Server != "irc.example.net" || opts.IRC.Port != 6697 || !opts.IRC.TLS ||
		opts.IRC.Nick != "botnick" || opts.IRC.Channel != "#bots" {
		t.Errorf("IRC=%+v", opts.IRC)
	}
}

func TestRuntimeOptionsFlagsOverrideConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, defaultConfigFile)
	if err := os.WriteFile(cfg, []byte(`
db: persistent.db
tick: 30s
irc:
  server: irc.example.net
  port: 6697
  tls: true
  nick: botnick
  channel: "#bots"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	opts, err := parseRuntimeOptions([]string{
		"-db", "override.db",
		"-tick", "5s",
		"-irc-server", "irc.override.net",
		"-irc-port", "6667",
		"-irc-tls=false",
		"-irc-channel", "#override",
	}, dir)
	if err != nil {
		t.Fatal(err)
	}

	if opts.DBPath != "override.db" {
		t.Errorf("DBPath=%q", opts.DBPath)
	}
	if opts.Tick != 5*time.Second {
		t.Errorf("Tick=%s", opts.Tick)
	}
	if opts.IRC.Server != "irc.override.net" {
		t.Errorf("IRC.Server=%q", opts.IRC.Server)
	}
	if opts.IRC.Port != 6667 {
		t.Errorf("IRC.Port=%d", opts.IRC.Port)
	}
	if opts.IRC.TLS {
		t.Error("IRC.TLS=true, want false")
	}
	if opts.IRC.Nick != "botnick" {
		t.Errorf("IRC.Nick=%q", opts.IRC.Nick)
	}
	if opts.IRC.Channel != "#override" {
		t.Errorf("IRC.Channel=%q", opts.IRC.Channel)
	}
}

func TestRuntimeOptionsLoadsSASLAndGeminiKey(t *testing.T) {
	t.Setenv("VIBEBOT_SASL_PASSWORD", "")
	t.Setenv("GEMINI_API_KEY", "")
	dir := t.TempDir()
	cfg := filepath.Join(dir, defaultConfigFile)
	if err := os.WriteFile(cfg, []byte(`
gemini_api_key: cfg-key
irc:
  server: irc.example.net
  nick: botnick
  channel: "#bots"
  sasl:
    user: sasluser
    pass: saslpass
`), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, err := parseRuntimeOptions(nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	if opts.GeminiAPIKey != "cfg-key" {
		t.Errorf("GeminiAPIKey=%q", opts.GeminiAPIKey)
	}
	if opts.IRC.SASLUser != "sasluser" || opts.IRC.SASLPass != "saslpass" {
		t.Errorf("SASL=%+v", opts.IRC)
	}
}

func TestRuntimeOptionsEnvOverridesSecretsFromConfig(t *testing.T) {
	t.Setenv("VIBEBOT_SASL_PASSWORD", "env-sasl-pass")
	t.Setenv("GEMINI_API_KEY", "env-gemini-key")
	dir := t.TempDir()
	cfg := filepath.Join(dir, defaultConfigFile)
	if err := os.WriteFile(cfg, []byte(`
gemini_api_key: cfg-key
irc:
  server: irc.example.net
  nick: botnick
  channel: "#bots"
  sasl:
    user: sasluser
    pass: cfg-sasl-pass
`), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, err := parseRuntimeOptions(nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	if opts.GeminiAPIKey != "env-gemini-key" {
		t.Errorf("env GEMINI_API_KEY should win, got %q", opts.GeminiAPIKey)
	}
	if opts.IRC.SASLPass != "env-sasl-pass" {
		t.Errorf("env VIBEBOT_SASL_PASSWORD should win, got %q", opts.IRC.SASLPass)
	}
	// User stays from config — env override only applies to the password.
	if opts.IRC.SASLUser != "sasluser" {
		t.Errorf("SASLUser=%q", opts.IRC.SASLUser)
	}
}

func TestRuntimeOptionsMissingDefaultConfigIsOK(t *testing.T) {
	opts, err := parseRuntimeOptions(nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if opts.DBPath != "vibebot.db" {
		t.Errorf("DBPath=%q", opts.DBPath)
	}
	if opts.IRC.Server != "" {
		t.Errorf("IRC.Server=%q", opts.IRC.Server)
	}
}

func TestParseRuntimeOptions_MCPStdioFlag(t *testing.T) {
	opts, err := parseRuntimeOptions([]string{"-mcp-stdio"}, t.TempDir())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !opts.MCPStdio {
		t.Fatal("MCPStdio: got false, want true")
	}
}

func TestParseRuntimeOptions_MCPStdioDefaultsFalse(t *testing.T) {
	opts, err := parseRuntimeOptions(nil, t.TempDir())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.MCPStdio {
		t.Fatal("MCPStdio default: got true, want false")
	}
}
