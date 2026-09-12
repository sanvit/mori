// Command mori is a read-only S3 file browser with preview and ZIP download.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
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
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck(cfg); err != nil {
			log.Fatal(err)
		}
		return
	}
	if _, assetErr := web.Files.ReadFile("vendor/manifest.json"); assetErr != nil {
		log.Print("preview libraries are not bundled; run make assets before building (Docker builds include this step)")
	}
	store, err := openBackend(cfg)
	if err != nil {
		log.Fatal(err)
	}
	a, err := server.Open(cfg, store)
	if err != nil {
		log.Fatal(err)
	}
	defer a.Shutdown()
	srv := &http.Server{Addr: cfg.Listen, Handler: a, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if srv.Shutdown(c) != nil {
			srv.Close()
			a.Shutdown()
		}
	}()
	log.Printf("mori %s | mode=%s backend=%s cache=%s | listen %s", version, cfg.ServeMode, cfg.Backend, cfg.CacheMode, cfg.Listen)
	if cfg.ZipDisabled {
		log.Print("ZIP downloads disabled (BROWSER_ZIP_ENABLED=false)")
	}
	if cfg.Public {
		log.Print("WARNING: all visitors may list and download the configured bucket/prefix; protect it with a TLS/auth gateway if necessary")
	}
	if e := srv.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
		log.Fatal(e)
	}
	<-drained
}

func healthcheck(cfg config.Config) error {
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, port)
	if cfg.HealthPath == "" {
		conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err == nil {
			conn.Close()
		}
		return err
	}
	u := url.URL{Scheme: "http", Host: addr, Path: cfg.HealthPath}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(u.String())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("health returned %d", resp.StatusCode)
	}
	return nil
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
