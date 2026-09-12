// Command mori is a read-only S3 file browser with preview and ZIP download.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mori/internal/backend"
	"mori/internal/backend/ftp"
	"mori/internal/backend/sftp"
	"mori/internal/backend/webdav"
	"mori/internal/config"
	"mori/internal/s3"
	"mori/internal/server"
	"mori/web"
)

const version = "0.5.0"

func main() {
	if err := config.LoadEnv(".env"); err != nil {
		log.Fatal(err)
	}
	cfg, err := config.Read()
	if err != nil {
		log.Fatal(err)
	}
	if _, assetErr := web.Files.ReadFile("vendor/manifest.json"); assetErr != nil {
		log.Print("preview libraries are not bundled; run make assets before building (Docker builds include this step)")
	}
	store, err := openBackend(cfg)
	if err != nil {
		log.Fatal(err)
	}
	a := server.NewWithBackend(cfg, store)
	srv := &http.Server{Addr: cfg.Listen, Handler: a, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		d, e := time.ParseDuration(config.Env("SHUTDOWN_TIMEOUT", "30s"))
		if e != nil || d <= 0 {
			d = 30 * time.Second
		}
		c, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		if srv.Shutdown(c) != nil {
			srv.Close()
		}
	}()
	mode := cfg.Backend
	if cfg.Backend == "s3" {
		mode = "S3 direct (no disk cache)"
		if cfg.Proxy != nil {
			mode = "S3 + caching proxy"
		}
	}
	log.Printf("mori %s | %s | listen %s", version, mode, cfg.Listen)
	if cfg.ZipDisabled {
		log.Print("ZIP downloads disabled (BROWSER_ZIP_ENABLED=false)")
	}
	if cfg.Public {
		log.Print("WARNING: all visitors may list and download the configured bucket/prefix; protect it with a TLS/auth gateway if necessary")
	}
	if e := srv.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
		log.Fatal(e)
	}
}

func openBackend(cfg config.Config) (backend.Backend, error) {
	switch cfg.Backend {
	case "s3":
		return s3.New(cfg), nil
	case "webdav":
		return webdav.New(cfg.WebDAV), nil
	case "ftp":
		return ftp.New(cfg.FTP), nil
	case "sftp":
		return sftp.New(cfg.SFTP)
	}
	return nil, fmt.Errorf("unsupported STORAGE_BACKEND %q", cfg.Backend)
}
