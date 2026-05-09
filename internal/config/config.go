package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/spf13/viper"
)

type Config struct {
	Addr                  string        `mapstructure:"REDGE_ADDR" validate:"required"`
	AdminEnabled          bool          `mapstructure:"REDGE_ADMIN_ENABLED"`
	AdminAddr             string        `mapstructure:"REDGE_ADMIN_ADDR" validate:"required"`
	Environment           string        `mapstructure:"REDGE_ENV" validate:"required,oneof=development test production"`
	LogLevel              string        `mapstructure:"REDGE_LOG_LEVEL" validate:"required,oneof=debug info warn error"`
	LogFormat             string        `mapstructure:"REDGE_LOG_FORMAT" validate:"required,oneof=json console"`
	ShutdownTimeout       time.Duration `mapstructure:"REDGE_SHUTDOWN_TIMEOUT"`
	Password              string        `mapstructure:"REDGE_PASSWORD"`
	RequireAuth           bool          `mapstructure:"REDGE_REQUIRE_AUTH"`
	MaxRequestBytes       int64         `mapstructure:"REDGE_MAX_REQUEST_BYTES" validate:"min=1024"`
	ReadTimeout           time.Duration `mapstructure:"REDGE_READ_TIMEOUT"`
	WriteTimeout          time.Duration `mapstructure:"REDGE_WRITE_TIMEOUT"`
	DatabaseURL           string        `mapstructure:"DATABASE_URL" validate:"required"`
	TursoAuthToken        string        `mapstructure:"TURSO_AUTH_TOKEN"`
	D1APIToken            string        `mapstructure:"D1_API_TOKEN"`
	D1BaseURL             string        `mapstructure:"D1_BASE_URL"`
	D1RetryMax            int           `mapstructure:"D1_RETRY_MAX" validate:"min=0"`
	D1RetryMinBackoff     time.Duration `mapstructure:"D1_RETRY_MIN_BACKOFF"`
	D1RetryMaxBackoff     time.Duration `mapstructure:"D1_RETRY_MAX_BACKOFF"`
	DatabaseMaxOpenConns  int           `mapstructure:"DATABASE_MAX_OPEN_CONNS" validate:"min=1"`
	DatabaseMaxIdleConns  int           `mapstructure:"DATABASE_MAX_IDLE_CONNS" validate:"min=1"`
	DatabaseConnMaxLife   time.Duration `mapstructure:"DATABASE_CONN_MAX_LIFETIME"`
	MigrationsAuto        bool          `mapstructure:"REDGE_MIGRATIONS_AUTO"`
	CacheEnabled          bool          `mapstructure:"REDGE_CACHE_ENABLED"`
	CacheMaxKeys          int           `mapstructure:"REDGE_CACHE_MAX_KEYS" validate:"min=1"`
	CacheMaxBytes         int64         `mapstructure:"REDGE_CACHE_MAX_BYTES" validate:"min=1"`
	CacheDefaultTTL       time.Duration `mapstructure:"REDGE_CACHE_DEFAULT_TTL"`
	ExpiryCleanupEnabled  bool          `mapstructure:"REDGE_EXPIRY_CLEANUP_ENABLED"`
	ExpiryCleanupInterval time.Duration `mapstructure:"REDGE_EXPIRY_CLEANUP_INTERVAL"`
	ExpiryCleanupLimit    int           `mapstructure:"REDGE_EXPIRY_CLEANUP_LIMIT" validate:"min=1"`
	AdminToken            string        `mapstructure:"REDGE_ADMIN_TOKEN"`
	AdminAllowedIPs       string        `mapstructure:"REDGE_ADMIN_ALLOWED_IPS"`
	AdminIPCheckEnabled   bool          `mapstructure:"REDGE_ADMIN_IP_CHECK_ENABLED"`
	AdminReadOnly         bool          `mapstructure:"REDGE_ADMIN_READONLY"`
}

func Load() (*Config, error) {
	viper.SetConfigFile(".env")
	viper.AutomaticEnv()
	setDefaults()

	if err := viper.ReadInConfig(); err != nil {
		_, notFound := err.(viper.ConfigFileNotFoundError)
		if !notFound && !os.IsNotExist(err) && !strings.Contains(strings.ToLower(err.Error()), "cannot find the file") {
			return nil, fmt.Errorf("read .env: %w", err)
		}
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if cfg.RequireAuth && cfg.Password == "" {
		return nil, fmt.Errorf("REDGE_REQUIRE_AUTH=true requires REDGE_PASSWORD")
	}
	if cfg.IsProduction() && cfg.AdminEnabled && cfg.AdminToken == "" {
		return nil, fmt.Errorf("REDGE_ADMIN_TOKEN is required in production when admin is enabled")
	}
	if err := validator.New().Struct(&cfg); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	return &cfg, nil
}

func setDefaults() {
	viper.SetDefault("REDGE_ADDR", "0.0.0.0:6379")
	viper.SetDefault("REDGE_ADMIN_ENABLED", true)
	viper.SetDefault("REDGE_ADMIN_ADDR", "0.0.0.0:9090")
	viper.SetDefault("REDGE_ENV", "development")
	viper.SetDefault("REDGE_LOG_LEVEL", "info")
	viper.SetDefault("REDGE_LOG_FORMAT", "json")
	viper.SetDefault("REDGE_SHUTDOWN_TIMEOUT", "10s")
	viper.SetDefault("REDGE_PASSWORD", "")
	viper.SetDefault("REDGE_REQUIRE_AUTH", false)
	viper.SetDefault("REDGE_MAX_REQUEST_BYTES", 1048576)
	viper.SetDefault("REDGE_READ_TIMEOUT", "30s")
	viper.SetDefault("REDGE_WRITE_TIMEOUT", "30s")
	viper.SetDefault("DATABASE_URL", "sqlite://redge.db")
	viper.SetDefault("TURSO_AUTH_TOKEN", "")
	viper.SetDefault("D1_API_TOKEN", "")
	viper.SetDefault("D1_BASE_URL", "")
	viper.SetDefault("D1_RETRY_MAX", 3)
	viper.SetDefault("D1_RETRY_MIN_BACKOFF", "200ms")
	viper.SetDefault("D1_RETRY_MAX_BACKOFF", "2s")
	viper.SetDefault("DATABASE_MAX_OPEN_CONNS", 50)
	viper.SetDefault("DATABASE_MAX_IDLE_CONNS", 10)
	viper.SetDefault("DATABASE_CONN_MAX_LIFETIME", "1h")
	viper.SetDefault("REDGE_MIGRATIONS_AUTO", true)
	viper.SetDefault("REDGE_CACHE_ENABLED", true)
	viper.SetDefault("REDGE_CACHE_MAX_KEYS", 100000)
	viper.SetDefault("REDGE_CACHE_MAX_BYTES", 134217728)
	viper.SetDefault("REDGE_CACHE_DEFAULT_TTL", "5s")
	viper.SetDefault("REDGE_EXPIRY_CLEANUP_ENABLED", true)
	viper.SetDefault("REDGE_EXPIRY_CLEANUP_INTERVAL", "30s")
	viper.SetDefault("REDGE_EXPIRY_CLEANUP_LIMIT", 1000)
	viper.SetDefault("REDGE_ADMIN_TOKEN", "")
	viper.SetDefault("REDGE_ADMIN_ALLOWED_IPS", "")
	viper.SetDefault("REDGE_ADMIN_IP_CHECK_ENABLED", false)
	viper.SetDefault("REDGE_ADMIN_READONLY", false)
}

func (c *Config) IsDevelopment() bool { return c.Environment == "development" }
func (c *Config) IsProduction() bool  { return c.Environment == "production" }

func (c *Config) GetResolvedDatabaseURL() string {
	dsn := strings.TrimSpace(c.DatabaseURL)
	if !strings.HasPrefix(dsn, "libsql://") {
		return dsn
	}
	lower := strings.ToLower(dsn)
	idx := strings.Index(lower, "authtoken=")
	if idx == -1 {
		return dsn
	}
	start := idx
	if start > 0 && (dsn[start-1] == '&' || dsn[start-1] == '?') {
		start--
	}
	endRel := strings.Index(dsn[idx:], "&")
	if endRel == -1 {
		return strings.TrimRight(dsn[:start], "?&")
	}
	return strings.TrimRight(dsn[:start]+dsn[idx+endRel:], "?&")
}

func (c *Config) GetTursoAuthToken() string { return strings.TrimSpace(c.TursoAuthToken) }

func (c *Config) DatabaseType() string {
	dsn := strings.TrimSpace(c.DatabaseURL)
	switch {
	case strings.HasPrefix(dsn, "postgres://"), strings.HasPrefix(dsn, "postgresql://"):
		return "postgres"
	case strings.HasPrefix(dsn, "mysql://"):
		return "mysql"
	case strings.HasPrefix(dsn, "sqlite://"):
		return "sqlite"
	case strings.HasPrefix(dsn, "libsql://"), strings.HasPrefix(dsn, "file:"):
		return "libsql"
	case strings.HasPrefix(dsn, "d1://"):
		return "d1"
	default:
		return "postgres"
	}
}

func (c *Config) D1Parts() (accountID, databaseID string, err error) {
	dsn := strings.TrimSpace(c.DatabaseURL)
	if !strings.HasPrefix(dsn, "d1://") {
		return "", "", fmt.Errorf("not a d1 database url")
	}
	raw := strings.Trim(strings.TrimPrefix(dsn, "d1://"), "/")
	parts := strings.Split(raw, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("DATABASE_URL for D1 must be d1://account_id/database_id")
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func (c *Config) GetAdminAllowedIPs() []string {
	normalized := strings.NewReplacer("\n", ",", ";", ",").Replace(c.AdminAllowedIPs)
	parts := strings.Split(normalized, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Trim(strings.TrimSpace(part), "\"'")
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
