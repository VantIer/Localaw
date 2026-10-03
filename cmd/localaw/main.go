// Command localaw is the Localaw entry point. Localaw is a purely local AI
// agent: it talks to an OpenAI-compatible LLM and executes the model's tool
// calls on the machine hosting the process.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"localaw/internal/cli"
	"localaw/internal/config"
	"localaw/internal/engine"
	"localaw/internal/local"
	"localaw/internal/web"
)

func main() {
	mode := flag.String("mode", "cli", "run mode: cli | web")
	cfgPath := flag.String("config", "config.json", "path to the JSON config file")
	flag.Parse()

	logger := log.New(os.Stderr, "[localaw] ", log.LstdFlags)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		logger.Fatalf("config: %v", err)
	}

	cmdTimeout := time.Duration(cfg.Policy.CmdTimeout) * time.Second
	file := local.NewFileModule()
	runner := local.NewExecutor(file)
	queue := local.NewQueue(runner, cfg.Policy.QueueCapacity, cmdTimeout)
	eng := engine.New(cfg, queue)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch *mode {
	case "cli":
		done := make(chan struct{})
		go func() {
			cli.Run(cfg, eng)
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			logger.Printf("interrupt received; shutting down")
		}
	case "web":
		srv := web.New(cfg, eng, file)
		webErr := make(chan error, 1)
		go func() { webErr <- srv.ListenAndServe() }()
		logger.Printf("web panel on http://%s:%d", cfg.Web.ListenHost, cfg.Web.ListenPort)
		select {
		case <-ctx.Done():
			logger.Printf("interrupt received; shutting down")
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = srv.Shutdown(shutCtx)
			cancel()
		case err := <-webErr:
			if err != nil && err != http.ErrServerClosed {
				logger.Fatalf("web: %v", err)
			}
		}
	default:
		logger.Fatalf("unknown mode %q (use cli|web)", *mode)
	}

	queue.Close()
}
