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

	// LLMProvider is "openrouter" or "ollama". These and MODELS_FILE only
	// seed the database on first start; the backoffice owns them after.
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

	// Tools. An empty SEARXNG_URL leaves web search out. The Python sandbox
	// runs in docker containers under gVisor (PYTHON_RUNTIME=runsc);
	// PYTHON_RUNTIME=runc runs them without it, for machines that lack it.
	// PYTHON_ENABLED=false leaves the sandbox out.
	SearxngURL     string        `env:"SEARXNG_URL" envDefault:"http://127.0.0.1:8888"`
	PythonEnabled  bool          `env:"PYTHON_ENABLED" envDefault:"true"`
	PythonImage    string        `env:"PYTHON_IMAGE" envDefault:"adytus-sandbox"`
	PythonRuntime  string        `env:"PYTHON_RUNTIME" envDefault:"runsc"`
	PythonCPUs     string        `env:"PYTHON_CPUS" envDefault:"1"`
	PythonWarm     int           `env:"PYTHON_WARM" envDefault:"2"`
	PythonTimeout  time.Duration `env:"PYTHON_TIMEOUT" envDefault:"10s"`
	PythonMemoryMB int           `env:"PYTHON_MEMORY_MB" envDefault:"256"`
	PythonParallel int           `env:"PYTHON_PARALLEL" envDefault:"2"`
	ToolMaxSteps   int           `env:"TOOL_MAX_STEPS" envDefault:"8"`

	// Crypto payments on Base. Off unless BASE_XPUB is set. The xpub is a
	// public key: deposit addresses are derived from it, and funds are swept
	// with keys that never touch this server. BASE_USDC and BASE_ETH_USD_FEED
	// default to mainnet; BASE_CHAIN_ID=84532 selects Base Sepolia, whose
	// addresses must then be set (see .env.example).
	BaseRPCURL        string        `env:"BASE_RPC_URL"`
	BaseXPub          string        `env:"BASE_XPUB"`
	BaseChainID       int64         `env:"BASE_CHAIN_ID" envDefault:"8453"`
	BaseUSDC          string        `env:"BASE_USDC" envDefault:"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"`
	BaseETHUSDFeed    string        `env:"BASE_ETH_USD_FEED" envDefault:"0x71041dddad3595F9CEd3DcCFBe3D1F4b0a16Bb70"`
	CryptoConfirms    int64         `env:"CRYPTO_CONFIRMATIONS" envDefault:"12"`
	CryptoQuoteTTL    time.Duration `env:"CRYPTO_QUOTE_TTL" envDefault:"15m"`
	CryptoMinMicros   int64         `env:"CRYPTO_MIN_MICROS" envDefault:"5000000"`
	CryptoDustMicros  int64         `env:"CRYPTO_DUST_MICROS" envDefault:"100000"`
	CryptoToleranceBP int64         `env:"CRYPTO_ETH_TOLERANCE_BPS" envDefault:"100"`
	CryptoPoll        time.Duration `env:"CRYPTO_POLL" envDefault:"6s"`
	CryptoFullScan    time.Duration `env:"CRYPTO_FULL_SCAN" envDefault:"5m"`

	// BackofficeDir holds the built backoffice UI (apps/backoffice/dist),
	// served to admins only.
	BackofficeDir string `env:"BACKOFFICE_DIR" envDefault:"../backoffice/dist"`

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
	if c.BaseXPub != "" {
		if c.BaseRPCURL == "" {
			return nil, fmt.Errorf("BASE_RPC_URL is required when BASE_XPUB is set")
		}
		if c.CryptoConfirms < 1 || c.CryptoToleranceBP < 0 || c.CryptoToleranceBP >= 10_000 {
			return nil, fmt.Errorf("CRYPTO_CONFIRMATIONS must be at least 1 and CRYPTO_ETH_TOLERANCE_BPS in [0, 10000)")
		}
	}
	c.KDF = crypto.DefaultKDF
	return &c, nil
}
