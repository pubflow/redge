package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/pubflow/redge/internal/admin"
	"github.com/pubflow/redge/internal/cache"
	"github.com/pubflow/redge/internal/command"
	"github.com/pubflow/redge/internal/config"
	"github.com/pubflow/redge/internal/database"
	"github.com/pubflow/redge/internal/server"
	"github.com/pubflow/redge/internal/status"
	"github.com/pubflow/redge/internal/store"
	"github.com/pubflow/redge/internal/store/d1store"
	"github.com/pubflow/redge/internal/store/sqlstore"
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

	tcp := server.NewTCP(server.Options{
		Addr:            cfg.Addr,
		Password:        cfg.Password,
		MaxRequestBytes: cfg.MaxRequestBytes,
		ReadTimeout:     cfg.ReadTimeout,
		WriteTimeout:    cfg.WriteTimeout,
		Router:          router,
		Logger:          logger,
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

	var statusServer *status.Server
	if cfg.HTTPEnabled {
		statusServer = status.New(status.Options{
			Addr:         cfg.HTTPAddr,
			Store:        st,
			DatabaseType: databaseType,
			Logger:       logger,
		})
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := statusServer.ListenAndServe(); err != nil && !errors.Is(err, status.ErrClosed) {
				logger.Fatal("http status server stopped", zap.Error(err))
			}
		}()
	}

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

	logger.Info("redge started", zap.String("redis_addr", cfg.Addr), zap.String("http_addr", cfg.HTTPAddr), zap.String("admin_addr", cfg.AdminAddr), zap.String("database", databaseType))
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
