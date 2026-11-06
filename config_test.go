package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

// validEnv returns a complete, obviously fake environment. Each value is
// distinctive so tests can search for it in error messages and output.
func validEnv() map[string]string {
	return map[string]string{
		envSecretKey:           "sk_sbox_fake_secret_7f3a",
		envPublicKey:           "pk_sbox_fake_public_9c1d",
		envSubdomain:           "fakesub1",
		envProcessingChannelID: "pc_fake_channel_42",
		envWebhookSignatureKey: "whsec_fake_signature_5e2b",
		envPublicBaseURL:       "http://localhost:8080",
		envPort:                "",
	}
}

// fakeGetenv turns a map into a getenv function, so tests never touch the real env.
func fakeGetenv(env map[string]string) func(string) string {
	return func(name string) string { return env[name] }
}

// assertNoValues fails if any env value shows up in s. This is the safety
// property itself: whatever the message says, it must never carry a value.
// Values shorter than 4 characters are skipped, they'd match by chance.
func assertNoValues(t *testing.T, s string, env map[string]string) {
	t.Helper()
	for name, value := range env {
		if len(value) >= 4 && strings.Contains(s, value) {
			t.Errorf("output contains the value of %s: %q", name, s)
		}
	}
}

// Proves a complete env maps to the right fields and PORT defaults to 8080.
func TestLoadConfigValid(t *testing.T) {
	cfg, err := LoadConfig(fakeGetenv(validEnv()))
	if err != nil {
		t.Fatalf("LoadConfig: unexpected error: %v", err)
	}
	want := Config{
		SecretKey:           "sk_sbox_fake_secret_7f3a",
		PublicKey:           "pk_sbox_fake_public_9c1d",
		Subdomain:           "fakesub1",
		ProcessingChannelID: "pc_fake_channel_42",
		WebhookSignatureKey: "whsec_fake_signature_5e2b",
		PublicBaseURL:       "http://localhost:8080",
		Port:                "8080",
	}
	// Compare fields directly: printing cfg would go through the redacting String().
	if cfg.SecretKey != want.SecretKey || cfg.PublicKey != want.PublicKey ||
		cfg.Subdomain != want.Subdomain || cfg.ProcessingChannelID != want.ProcessingChannelID ||
		cfg.WebhookSignatureKey != want.WebhookSignatureKey ||
		cfg.PublicBaseURL != want.PublicBaseURL || cfg.Port != want.Port {
		t.Errorf("LoadConfig fields don't match the env")
	}
}

// Proves the simulator settings: on after 800 ms when unset, and both can
// be changed (D-042).
func TestLoadConfigSimulator(t *testing.T) {
	cfg, err := LoadConfig(fakeGetenv(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SimulateWebhooks || cfg.SimulatedWebhookDelay != 800*time.Millisecond {
		t.Errorf("defaults: on %v, delay %v", cfg.SimulateWebhooks, cfg.SimulatedWebhookDelay)
	}
	env := validEnv()
	env[envSimulateWebhooks] = "false"
	env[envSimulatedWebhookDelay] = "0"
	if cfg, err = LoadConfig(fakeGetenv(env)); err != nil {
		t.Fatal(err)
	}
	if cfg.SimulateWebhooks || cfg.SimulatedWebhookDelay != 0 {
		t.Errorf("set: on %v, delay %v", cfg.SimulateWebhooks, cfg.SimulatedWebhookDelay)
	}
}

// Proves small clean-ups: explicit PORT kept, trailing slashes and spaces trimmed.
func TestLoadConfigNormalises(t *testing.T) {
	tests := []struct {
		name    string
		set     map[string]string
		wantURL string
		wantPrt string
	}{
		{"explicit port", map[string]string{envPort: "9090"}, "http://localhost:8080", "9090"},
		{"trailing slash", map[string]string{envPublicBaseURL: "https://demo.example.dev/"}, "https://demo.example.dev", "8080"},
		{"surrounding spaces", map[string]string{envPublicBaseURL: "  https://demo.example.dev  "}, "https://demo.example.dev", "8080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			for k, v := range tt.set {
				env[k] = v
			}
			cfg, err := LoadConfig(fakeGetenv(env))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.PublicBaseURL != tt.wantURL || cfg.Port != tt.wantPrt {
				t.Errorf("got URL %q port %q, want %q %q", cfg.PublicBaseURL, cfg.Port, tt.wantURL, tt.wantPrt)
			}
		})
	}
}

// Proves each required variable, when missing, is named in the error, and nothing else is.
func TestLoadConfigMissingEachVariable(t *testing.T) {
	for _, name := range requiredEnv {
		t.Run(name, func(t *testing.T) {
			env := validEnv()
			delete(env, name)
			_, err := LoadConfig(fakeGetenv(env))
			if err == nil {
				t.Fatal("expected an error")
			}
			want := "missing required env variables: " + name
			if err.Error() != want {
				t.Errorf("got %q, want %q", err.Error(), want)
			}
			assertNoValues(t, err.Error(), env)
		})
	}
}

// Proves several missing variables are all named, in a stable order, and
// whitespace-only counts as missing.
func TestLoadConfigMissingSeveral(t *testing.T) {
	env := validEnv()
	delete(env, envWebhookSignatureKey)
	delete(env, envSecretKey)
	env[envSubdomain] = "   "
	_, err := LoadConfig(fakeGetenv(env))
	want := "missing required env variables: CKO_SECRET_KEY, CKO_SUBDOMAIN, CKO_WEBHOOK_SIGNATURE_KEY"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
	assertNoValues(t, err.Error(), env)
}

// Proves each format check rejects a bad value, names the variable,
// and never echoes the bad value or any other value.
func TestLoadConfigInvalidFormat(t *testing.T) {
	tests := []struct {
		name   string
		envVar string
		badVal string
	}{
		{"production-looking secret key", envSecretKey, "sk_fakeprod_9b8a"},
		{"public key in secret slot", envSecretKey, "pk_sbox_fake_swapped_1a2b"},
		{"production-looking public key", envPublicKey, "pk_fakeprod_3c4d"},
		{"channel without pc_", envProcessingChannelID, "fake_channel_77"},
		{"base URL without scheme", envPublicBaseURL, "localhost:8080"},
		{"base URL with ftp", envPublicBaseURL, "ftp://demo.example.dev"},
		{"base URL without host", envPublicBaseURL, "https://"},
		{"port not a number", envPort, "eighty"},
		{"port too high", envPort, "70000"},
		{"port zero", envPort, "00000"},
		{"subdomain upper case", envSubdomain, "FakeSub1"},
		{"subdomain with underscore", envSubdomain, "fake_sub"},
		{"subdomain with cli_ prefix", envSubdomain, "cli_fakesub1"},
		{"simulate not a bool", envSimulateWebhooks, "sometimes"},
		{"delay not a number", envSimulatedWebhookDelay, "fast"},
		{"delay negative", envSimulatedWebhookDelay, "-1"},
		{"delay too long", envSimulatedWebhookDelay, "10001"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			env[tt.envVar] = tt.badVal
			_, err := LoadConfig(fakeGetenv(env))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.envVar) {
				t.Errorf("error %q doesn't name %s", err.Error(), tt.envVar)
			}
			assertNoValues(t, err.Error(), env)
		})
	}
}

// Proves printing a Config with any fmt verb or slog never shows a value
// other than the base URL and port.
func TestConfigPrintingRedacts(t *testing.T) {
	env := validEnv()
	cfg, err := LoadConfig(fakeGetenv(env))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	secretish := map[string]string{
		envSecretKey:           env[envSecretKey],
		envPublicKey:           env[envPublicKey],
		envSubdomain:           env[envSubdomain],
		envProcessingChannelID: env[envProcessingChannelID],
		envWebhookSignatureKey: env[envWebhookSignatureKey],
	}

	var logBuf bytes.Buffer
	slog.New(slog.NewJSONHandler(&logBuf, nil)).Info("config", "cfg", cfg)

	outputs := map[string]string{
		"%v":   fmt.Sprintf("%v", cfg),
		"%+v":  fmt.Sprintf("%+v", cfg),
		"%#v":  fmt.Sprintf("%#v", cfg),
		"%s":   fmt.Sprintf("%s", cfg),
		"&cfg": fmt.Sprintf("%+v", &cfg),
		"slog": logBuf.String(),
	}
	for verb, out := range outputs {
		t.Run(verb, func(t *testing.T) {
			assertNoValues(t, out, secretish)
			if !strings.Contains(out, "[set]") {
				t.Errorf("expected redaction markers in %q", out)
			}
		})
	}
}

// Proves a missing .env file is not an error.
func TestLoadDotEnvMissingFile(t *testing.T) {
	if err := loadDotEnv(filepath.Join(t.TempDir(), ".env")); err != nil {
		t.Errorf("missing file should be fine, got %v", err)
	}
}

// Proves a valid file is loaded into the process env.
func TestLoadDotEnvLoads(t *testing.T) {
	// t.Setenv registers a restore on cleanup. Unsetenv then makes the variable
	// truly absent, because godotenv never overrides a variable that already exists.
	t.Setenv("PBX_TEST_LOADED", "")
	os.Unsetenv("PBX_TEST_LOADED")

	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("PBX_TEST_LOADED=hello_fake\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadDotEnv(path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("PBX_TEST_LOADED"); got != "hello_fake" {
		t.Errorf("got %q, want hello_fake", got)
	}
}

// Proves the godotenv leak is closed: a malformed file gives an error that
// carries none of the file's content. Both cases make godotenv v1.5.1 quote
// raw content in its own error.
func TestLoadDotEnvParseErrorHidesContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"unterminated quote", "PBX_TEST_A=\"sk_sbox_fake_unterminated_8d7c\n"},
		{"bad variable name", "PBX-TEST-B=x\nPBX_TEST_C=sk_sbox_fake_after_bad_line_6e5f\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".env")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			// Precondition: godotenv's own error really does carry the fake key.
			// If a future godotenv stops leaking, this says so instead of the
			// test passing for the wrong reason.
			if raw := godotenv.Load(path); raw == nil || !strings.Contains(raw.Error(), "sk_sbox_fake") {
				t.Fatalf("precondition failed: godotenv raw error no longer quotes content, re-check loadDotEnv")
			}
			err := loadDotEnv(path)
			if err == nil {
				t.Fatal("expected a parse error")
			}
			if strings.Contains(err.Error(), "sk_sbox_fake") || strings.Contains(err.Error(), "PBX") {
				t.Errorf("error leaks file content: %q", err.Error())
			}
			if !strings.Contains(err.Error(), "could not parse") {
				t.Errorf("unexpected message: %q", err.Error())
			}
		})
	}
}

// Proves the committed .env.example names every variable the app reads, leaves
// the keys and IDs empty, and that its defaults pass the same checks as a real
// .env once the empty values are filled in.
func TestEnvExample(t *testing.T) {
	example, err := godotenv.Read(".env.example")
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}
	all := []string{envSecretKey, envPublicKey, envSubdomain, envProcessingChannelID,
		envWebhookSignatureKey, envPublicBaseURL, envPort, envSimulateWebhooks, envSimulatedWebhookDelay}
	if len(example) != len(all) {
		t.Errorf(".env.example has %d variables, want %d", len(example), len(all))
	}
	for _, name := range all {
		if _, ok := example[name]; !ok {
			t.Errorf(".env.example is missing %s", name)
		}
	}
	for _, name := range []string{envSecretKey, envPublicKey, envSubdomain, envProcessingChannelID, envWebhookSignatureKey} {
		if example[name] != "" {
			t.Errorf("%s must be empty in .env.example", name)
		}
	}
	// Fill the empty ones with the fake values, keep the example's defaults.
	env := validEnv()
	for name, v := range example {
		if v != "" {
			env[name] = v
		}
	}
	cfg, err := LoadConfig(fakeGetenv(env))
	if err != nil {
		t.Fatalf("defaults in .env.example don't load: %v", err)
	}
	if cfg.Port != "8080" || !cfg.SimulateWebhooks || cfg.SimulatedWebhookDelay != defaultSimulatedDelay {
		t.Errorf("defaults: port %q, simulate %v, delay %v", cfg.Port, cfg.SimulateWebhooks, cfg.SimulatedWebhookDelay)
	}
}
