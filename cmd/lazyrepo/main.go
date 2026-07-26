package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/teamzuzu/lazyrepo/internal/config"
	"github.com/teamzuzu/lazyrepo/internal/github"
	"github.com/teamzuzu/lazyrepo/internal/index"
	"github.com/teamzuzu/lazyrepo/internal/poller"
	"github.com/teamzuzu/lazyrepo/internal/server"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config.yaml")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	idx, err := index.LoadCache(cfg.CacheFile)
	if err != nil {
		log.Fatalf("loading cache: %v", err)
	}

	gh := github.NewClient()
	p := poller.New(cfg, gh, idx)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go p.Run(ctx)

	srv := server.New(idx, p)
	httpServer := &http.Server{Addr: cfg.Listen, Handler: srv.Handler()}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
	}()

	log.Printf("lazyrepo listening on %s", cfg.Listen)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("http server: %v", err)
	}
}
