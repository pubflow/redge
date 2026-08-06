package database

import (
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"strings"

	glebarez "github.com/glebarez/sqlite"
	"github.com/pubflow/redge/internal/config"
	libsql "github.com/tursodatabase/libsql-client-go/libsql"
	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Connection struct {
	DB     *gorm.DB
	dbType string
	closer io.Closer
}

func NewConnection(cfg *config.Config, log *zap.Logger) (*Connection, error) {
	dsn := cfg.GetResolvedDatabaseURL()
	dbType := detectType(dsn)

	var dialector gorm.Dialector
	var closer io.Closer
	switch dbType {
	case "postgres":
		dialector = postgres.Open(dsn)
	case "mysql":
		dialector = mysql.Open(trimMySQLScheme(dsn))
	case "sqlite":
		path := strings.TrimPrefix(dsn, "sqlite://")
		dialector = glebarez.Open(path)
	case "libsql":
		d, c, err := newLibSQLDialector(dsn, cfg.GetTursoAuthToken())
		if err != nil {
			return nil, fmt.Errorf("libsql dialector: %w", err)
		}
		dialector, closer = d, c
	default:
		return nil, fmt.Errorf("unsupported database type: %s", dbType)
	}

	gormLogger := logger.Default.LogMode(logger.Silent)
	if cfg.IsDevelopment() {
		gormLogger = logger.Default.LogMode(logger.Warn)
	}
	db, err := gorm.Open(dialector, &gorm.Config{Logger: gormLogger})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(cfg.DatabaseMaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.DatabaseMaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.DatabaseConnMaxLife)

	log.Info("database connected", zap.String("type", dbType), zap.Int("max_open", cfg.DatabaseMaxOpenConns))
	return &Connection{DB: db, dbType: dbType, closer: closer}, nil
}

func (c *Connection) Close() error {
	sqlDB, err := c.DB.DB()
	if err != nil {
		return err
	}
	if err := sqlDB.Close(); err != nil {
		return err
	}
	if c.closer != nil {
		return c.closer.Close()
	}
	return nil
}

func (c *Connection) Type() string       { return c.dbType }
func (c *Connection) IsPostgres() bool   { return c.dbType == "postgres" }
func (c *Connection) IsMySQL() bool      { return c.dbType == "mysql" }
func (c *Connection) IsSQLiteLike() bool { return c.dbType == "sqlite" || c.dbType == "libsql" }

func detectType(dsn string) string {
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

func trimMySQLScheme(dsn string) string {
	if !strings.HasPrefix(dsn, "mysql://") {
		return dsn
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" {
		return strings.TrimPrefix(dsn, "mysql://")
	}
	user := u.User.Username()
	password, _ := u.User.Password()
	auth := user
	if password != "" {
		auth += ":" + password
	}
	dbName := strings.TrimPrefix(u.Path, "/")
	query := u.Query()
	sslMode := strings.ToLower(firstURLQueryValue(query, "ssl-mode", "sslmode"))
	query.Del("ssl-mode")
	query.Del("sslmode")
	if query.Get("parseTime") == "" {
		query.Set("parseTime", "true")
	}
	if query.Get("tls") == "" {
		switch sslMode {
		case "required", "require":
			query.Set("tls", "skip-verify")
		case "verify-ca", "verify-full", "verify_identity", "verify-identity":
			query.Set("tls", "true")
		case "disabled", "disable":
			query.Set("tls", "false")
		}
	}
	return fmt.Sprintf("%s@tcp(%s)/%s?%s", auth, u.Host, dbName, query.Encode())
}

func firstURLQueryValue(values url.Values, keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(values.Get(key)); v != "" {
			return v
		}
	}
	return ""
}

func newLibSQLDialector(dbURL, authToken string) (gorm.Dialector, io.Closer, error) {
	var opts []libsql.Option
	if authToken != "" {
		opts = append(opts, libsql.WithAuthToken(authToken))
	}
	connector, err := libsql.NewConnector(dbURL, opts...)
	if err != nil {
		return nil, nil, err
	}
	rawDB := sql.OpenDB(connector)
	return glebarez.Dialector{Conn: rawDB}, nil, nil
}
