package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Env variable names. Errors only ever mention these names, never values.
const (
	envSecretKey           = "CKO_SECRET_KEY"
	envPublicKey           = "CKO_PUBLIC_KEY"
	envSubdomain           = "CKO_SUBDOMAIN"
	envProcessingChannelID = "CKO_PROCESSING_CHANNEL_ID"
	envWebhookSignatureKey = "CKO_WEBHOOK_SIGNATURE_KEY"
	envPublicBaseURL       = "PUBLIC_BASE_URL"
	envPort                = "PORT"
)

// requiredEnv is also the order used in "missing" errors, so messages are stable.
var requiredEnv = []string{
	envSecretKey,
	envPublicKey,
	envSubdomain,
	envProcessingChannelID,
	envWebhookSignatureKey,
	envPublicBaseURL,
}

const defaultPort = "8080"

// Config holds everything read from the environment.
// String, GoString and LogValue redact it, so printing a Config never shows a key.
type Config struct {
	SecretKey           string
	PublicKey           string
	Subdomain           string
	ProcessingChannelID string
	WebhookSignatureKey string
	PublicBaseURL       string
	Port                string
}

// LoadConfig reads and checks every variable. It takes getenv instead of
// calling os.Getenv so tests can pass a plain map.
func LoadConfig(getenv func(string) string) (Config, error) {
	get := func(name string) string { return strings.TrimSpace(getenv(name)) }

	var missing []string
	for _, name := range requiredEnv {
		if get(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env variables: %s", strings.Join(missing, ", "))
	}

	cfg := Config{
		SecretKey:           get(envSecretKey),
		PublicKey:           get(envPublicKey),
		Subdomain:           get(envSubdomain),
		ProcessingChannelID: get(envProcessingChannelID),
		WebhookSignatureKey: get(envWebhookSignatureKey),
		// Trim the trailing slash so "{base}/success" never becomes "//success".
		PublicBaseURL: strings.TrimRight(get(envPublicBaseURL), "/"),
		Port:          get(envPort),
	}
	if cfg.Port == "" {
		cfg.Port = defaultPort
	}

	// Sandbox only: a production key (sk_..., pk_... without _sbox_) is refused
	// at start, before the SDK ever sees it.
	var problems []string
	if !strings.HasPrefix(cfg.SecretKey, "sk_sbox_") {
		problems = append(problems, envSecretKey+" must be a sandbox secret key (sk_sbox_...)")
	}
	if !strings.HasPrefix(cfg.PublicKey, "pk_sbox_") {
		problems = append(problems, envPublicKey+" must be a sandbox public key (pk_sbox_...)")
	}
	if !strings.HasPrefix(cfg.ProcessingChannelID, "pc_") {
		problems = append(problems, envProcessingChannelID+" must start with pc_")
	}
	if !validBaseURL(cfg.PublicBaseURL) {
		problems = append(problems, envPublicBaseURL+" must be an http or https URL with a host")
	}
	if !validPort(cfg.Port) {
		problems = append(problems, envPort+" must be a number from 1 to 65535")
	}
	if len(problems) > 0 {
		return Config{}, fmt.Errorf("invalid env variables: %s", strings.Join(problems, "; "))
	}
	return cfg, nil
}

func validBaseURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func validPort(raw string) bool {
	n, err := strconv.Atoi(raw)
	return err == nil && n >= 1 && n <= 65535
}

// loadDotEnv loads a .env file into the process env. A missing file is fine:
// variables can come from the real environment instead.
func loadDotEnv(path string) error {
	err := godotenv.Load(path)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		// Open or read failure: the PathError holds the path and the OS reason, no file content.
		return fmt.Errorf("could not read %s: %w", path, err)
	}
	// godotenv v1.5.1 parse errors quote raw file content ("near ..." and
	// "unterminated quoted value ..."), which would print keys. Drop the
	// original error on purpose and say only where to look.
	return fmt.Errorf("could not parse %s (check quotes and variable names)", path)
}

// String is used by %v, %+v and %s. Only the base URL and port are shown.
func (c Config) String() string {
	return fmt.Sprintf(
		"Config{SecretKey:%s PublicKey:%s Subdomain:%s ProcessingChannelID:%s WebhookSignatureKey:%s PublicBaseURL:%s Port:%s}",
		redact(c.SecretKey), redact(c.PublicKey), redact(c.Subdomain),
		redact(c.ProcessingChannelID), redact(c.WebhookSignatureKey),
		c.PublicBaseURL, c.Port,
	)
}

// GoString is used by %#v, which would otherwise print every field raw.
func (c Config) GoString() string { return c.String() }

// LogValue keeps slog from printing the fields raw.
func (c Config) LogValue() slog.Value { return slog.StringValue(c.String()) }

func redact(v string) string {
	if v == "" {
		return "[empty]"
	}
	return "[set]"
}
