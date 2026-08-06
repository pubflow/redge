package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pubflow/redge/internal/admin"
	"github.com/pubflow/redge/internal/cache"
	"github.com/pubflow/redge/internal/command"
	"github.com/pubflow/redge/internal/config"
	"github.com/pubflow/redge/internal/database"
	"github.com/pubflow/redge/internal/docapi"
	"github.com/pubflow/redge/internal/docstore"
	"github.com/pubflow/redge/internal/server"
	"github.com/pubflow/redge/internal/status"
	"github.com/pubflow/redge/internal/store"
	"github.com/pubflow/redge/internal/store/d1store"
	"github.com/pubflow/redge/internal/store/sqlstore"
	"github.com/pubflow/redge/internal/storeapi"
	"go.uber.org/zap"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	logger, err := newLogger(cfg)
	if err != nil {
		log.Fatalf("init logger: %v", err)
	}
	defer logger.Sync()

	var (
		conn         *database.Connection
		st           store.Store
		databaseType = cfg.DatabaseType()
	)
	if databaseType == "d1" {
		st, err = d1store.New(cfg, logger)
		if err != nil {
			logger.Fatal("d1 store init failed", zap.Error(err))
		}
	} else {
		conn, err = database.NewConnection(cfg, logger)
		if err != nil {
			logger.Fatal("database connection failed", zap.Error(err))
		}
		defer conn.Close()

		sqlStore, err := sqlstore.New(conn, logger)
		if err != nil {
			logger.Fatal("store init failed", zap.Error(err))
		}
		st = sqlStore
		databaseType = conn.Type()
	}

	if cfg.MigrationsAuto {
		if err := st.Migrate(context.Background()); err != nil {
			logger.Fatal("migration failed", zap.Error(err))
		}
	}

	l1 := cache.NewMemory(cache.Options{
		Enabled:    cfg.CacheEnabled,
		MaxKeys:    cfg.CacheMaxKeys,
		MaxBytes:   cfg.CacheMaxBytes,
		DefaultTTL: cfg.CacheDefaultTTL,
	})
	defer l1.Close()

	router := command.NewRouter(command.RouterOptions{
		Store:    st,
		Cache:    l1,
		Password: cfg.Password,
		Logger:   logger,
	})

	tlsConfig, err := newRedisTLSConfig(cfg)
	if err != nil {
		logger.Fatal("redis tls config failed", zap.Error(err))
	}

	tcp := server.NewTCP(server.Options{
		Addr:             cfg.Addr,
		Password:         cfg.Password,
		MaxRequestBytes:  cfg.MaxRequestBytes,
		ReadTimeout:      cfg.ReadTimeout,
		WriteTimeout:     cfg.WriteTimeout,
		AllowedIPs:       cfg.GetAllowedIPs(),
		MaxConnections:   cfg.MaxConnections,
		AuthFailureDelay: cfg.AuthFailureDelay,
		TLSConfig:        tlsConfig,
		Router:           router,
		Logger:           logger,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := tcp.ListenAndServe(); err != nil && !errors.Is(err, server.ErrClosed) {
			logger.Fatal("tcp server stopped", zap.Error(err))
		}
	}()

	var adminServer *admin.Server
	if cfg.AdminEnabled {
		adminServer = admin.New(admin.Options{
			Addr:         cfg.AdminAddr,
			Token:        cfg.AdminToken,
			AllowedIPs:   cfg.GetAdminAllowedIPs(),
			IPCheck:      cfg.AdminIPCheckEnabled,
			ReadOnly:     cfg.AdminReadOnly,
			Store:        st,
			Cache:        l1,
			DatabaseType: databaseType,
			Logger:       logger,
		})
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := adminServer.ListenAndServe(); err != nil && !errors.Is(err, admin.ErrClosed) {
				logger.Fatal("admin server stopped", zap.Error(err))
			}
		}()
	}

	var docServer *docapi.Server
	if cfg.DocAPIEnabled {
		docStore, ok := st.(docstore.Store)
		if !ok {
			logger.Fatal("document api is not supported by this store")
		}
		if cfg.MigrationsAuto {
			if err := docStore.MigrateDocs(context.Background()); err != nil {
				logger.Fatal("document api migration failed", zap.Error(err))
			}
		}
		docServer = docapi.New(docapi.Options{
			Addr:               cfg.HTTPAddr,
			Token:              cfg.GetAPIToken(),
			MaxBodyBytes:       cfg.APIMaxBodyBytes,
			WSEnabled:          cfg.WSEnabled,
			AllowedIPs:         cfg.GetAPIAllowedIPs(),
			IPCheck:            cfg.APIIPCheckEnabled,
			CORSEnabled:        cfg.APICORSEnabled,
			CORSOrigins:        cfg.GetAPICORSOrigins(),
			RateLimitEnabled:   cfg.APIRateLimit,
			RateLimitRPS:       cfg.APIRateLimitRPS,
			RateLimitBurst:     cfg.APIRateLimitBurst,
			WSMaxMessageBytes:  cfg.WSMaxBytes,
			WSIdleTimeout:      cfg.WSIdleTimeout,
			WSMaxSubscriptions: cfg.WSMaxSubs,
			Store:              docStore,
			Logger:             logger,
		})
	}

	var storeServer *storeapi.Server
	if cfg.StoreAPIEnabled {
		storeServer = storeapi.New(storeapi.Options{
			Addr:             cfg.HTTPAddr,
			Token:            cfg.GetAPIToken(),
			MaxBodyBytes:     cfg.APIMaxBodyBytes,
			AllowedIPs:       cfg.GetAPIAllowedIPs(),
			IPCheck:          cfg.APIIPCheckEnabled,
			CORSEnabled:      cfg.APICORSEnabled,
			CORSOrigins:      cfg.GetAPICORSOrigins(),
			RateLimitEnabled: cfg.APIRateLimit,
			RateLimitRPS:     cfg.APIRateLimitRPS,
			RateLimitBurst:   cfg.APIRateLimitBurst,
			Store:            st,
			Cache:            l1,
			Logger:           logger,
		})
	}

	var statusServer *status.Server
	if cfg.HTTPEnabled {
		statusServer = status.New(status.Options{
			Addr:         cfg.HTTPAddr,
			Store:        st,
			DatabaseType: databaseType,
			Logger:       logger,
			Mount: func(mux *http.ServeMux) {
				if docServer != nil {
					docServer.Mount(mux)
				}
				if storeServer != nil {
					storeServer.Mount(mux)
				}
			},
		})
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := statusServer.ListenAndServe(); err != nil && !errors.Is(err, status.ErrClosed) {
				logger.Fatal("http server stopped", zap.Error(err))
			}
		}()
	}

	if cfg.ExpiryCleanupEnabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(cfg.ExpiryCleanupInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					n, err := st.CleanupExpired(context.Background(), cfg.ExpiryCleanupLimit)
					if err != nil {
						logger.Warn("expiry cleanup failed", zap.Error(err))
						continue
					}
					if n > 0 {
						logger.Debug("expired keys cleaned", zap.Int64("count", n))
					}
				}
			}
		}()
	}

	logger.Info("redge started", zap.String("redis_addr", cfg.Addr), zap.String("http_addr", cfg.HTTPAddr), zap.String("admin_addr", cfg.AdminAddr), zap.Bool("docapi", cfg.DocAPIEnabled), zap.Bool("storeapi", cfg.StoreAPIEnabled), zap.String("database", databaseType))
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	_ = tcp.Shutdown(shutdownCtx)
	if statusServer != nil {
		_ = statusServer.Shutdown(shutdownCtx)
	}
	if adminServer != nil {
		_ = adminServer.Shutdown(shutdownCtx)
	}
	wg.Wait()
	logger.Info("redge stopped")
}

func newLogger(cfg *config.Config) (*zap.Logger, error) {
	if cfg.LogFormat == "console" || cfg.IsDevelopment() {
		return zap.NewDevelopment()
	}
	return zap.NewProduction()
}

func newRedisTLSConfig(cfg *config.Config) (*tls.Config, error) {
	if !cfg.TLSEnabled {
		return nil, nil
	}
	cert, err := loadRedisTLSCertificate(cfg)
	if err != nil {
		return nil, err
	}
	minVersion := uint16(tls.VersionTLS12)
	if cfg.TLSMinVersion == "1.3" {
		minVersion = tls.VersionTLS13
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   minVersion,
	}, nil
}

func loadRedisTLSCertificate(cfg *config.Config) (tls.Certificate, error) {
	source, err := cfg.GetTLSMaterialSource()
	if err != nil {
		return tls.Certificate{}, err
	}
	switch source {
	case "pem":
		return tls.X509KeyPair(normalizePEMEnv(cfg.TLSCertPEM), normalizePEMEnv(cfg.TLSKeyPEM))
	case "base64":
		certPEM, err := decodeTLSBase64(cfg.TLSCertB64)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("decode REDGE_TLS_CERT_B64: %w", err)
		}
		keyPEM, err := decodeTLSBase64(cfg.TLSKeyB64)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("decode REDGE_TLS_KEY_B64: %w", err)
		}
		return tls.X509KeyPair(certPEM, keyPEM)
	case "file":
		return tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
	default:
		return tls.Certificate{}, fmt.Errorf("unsupported TLS material source %q", source)
	}
}

func normalizePEMEnv(raw string) []byte {
	normalized := strings.ReplaceAll(strings.TrimSpace(raw), `\n`, "\n")
	return []byte(normalized)
}

func decodeTLSBase64(raw string) ([]byte, error) {
	compact := strings.NewReplacer("\r", "", "\n", "", "\t", "", " ", "").Replace(strings.TrimSpace(raw))
	return base64.StdEncoding.DecodeString(compact)
}
