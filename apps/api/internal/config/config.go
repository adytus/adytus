package config

import (
	"encoding/base64"
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/reaganiwadha/adytus/apps/api/internal/crypto"
)

type Config struct {
	Addr        string `env:"ADDR" envDefault:":8080"`
	DatabaseURL string `env:"DATABASE_URL" envDefault:"postgres://adytus:adytus@localhost:5432/adytus?sslmode=disable"`

	// ServerKey wraps DEKs of objects the user opted into offline processing.
	// Base64, 32 bytes. Objects without opt-in are never readable with it.
	ServerKeyB64 string `env:"SERVER_KEY,required"`
	ServerKey    []byte `env:"-"`

	// LLMProvider is "openrouter" or "ollama".
	LLMProvider  string `env:"LLM_PROVIDER" envDefault:"ollama"`
	LLMBaseURL   string `env:"LLM_BASE_URL" envDefault:"http://localhost:11434/v1"`
	LLMAPIKey    string `env:"LLM_API_KEY"`
	ModelsFile   string `env:"MODELS_FILE" envDefault:"models.ollama.json"`
	SystemPrompt string `env:"SYSTEM_PROMPT" envDefault:"You are Adytus, a concise and helpful assistant."`

	// Budgeting. Money is in micro-USD.
	DailyCapMicros int64         `env:"DAILY_CAP_MICROS" envDefault:"2000000"`
	HoldTTL        time.Duration `env:"HOLD_TTL" envDefault:"10m"`
	ChatPerMinute  int           `env:"CHAT_PER_MINUTE" envDefault:"20"`
	AuthPerMinute  int           `env:"AUTH_PER_MINUTE" envDefault:"10"`

	SessionTTL   time.Duration `env:"SESSION_TTL" envDefault:"24h"`
	SessionIdle  time.Duration `env:"SESSION_IDLE" envDefault:"2h"`
	CookieSecure bool          `env:"COOKIE_SECURE" envDefault:"false"`

	S3Endpoint     string `env:"S3_ENDPOINT" envDefault:"http://localhost:9000"`
	S3Bucket       string `env:"S3_BUCKET" envDefault:"adytus"`
	S3AccessKey    string `env:"S3_ACCESS_KEY" envDefault:"adytus"`
	S3SecretKey    string `env:"S3_SECRET_KEY" envDefault:"adytus-secret"`
	MaxUploadBytes int    `env:"MAX_UPLOAD_BYTES" envDefault:"26214400"`

	// KDF is not configurable from the environment; tests lower it.
	KDF crypto.KDFParams `env:"-"`
}

func Load() (*Config, error) {
	var c Config
	if err := env.Parse(&c); err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(c.ServerKeyB64)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("SERVER_KEY must be 32 bytes, base64 encoded")
	}
	c.ServerKey = key
	if c.LLMProvider != "openrouter" && c.LLMProvider != "ollama" {
		return nil, fmt.Errorf("LLM_PROVIDER must be openrouter or ollama, got %q", c.LLMProvider)
	}
	c.KDF = crypto.DefaultKDF
	return &c, nil
}
