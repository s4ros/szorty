package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"szorty.infinigy.pl/internal/clock"
	"szorty.infinigy.pl/internal/config"
	"szorty.infinigy.pl/internal/httpapp"
	"szorty.infinigy.pl/internal/store"
)

// sweepInterval is how often the process deletes expired links.
const sweepInterval = 15 * time.Minute

func main() {
	os.Exit(mainExit(os.Getenv, os.Stderr))
}

func mainExit(getenv func(string) string, stderr io.Writer) int {
	cfg, err := config.Load(getenv)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 1
	}
	if err := serve(cfg); err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 1
	}
	return 0
}

func serve(cfg config.Config) error {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	clk := clock.Real{}
	app := httpapp.New(cfg, st, clk, newGoogleExchanger(cfg))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sweepEvery(ctx, st, clk)
	}()
	defer wg.Wait()
	defer stop()

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()
	log.Printf("listening on %s", cfg.ListenAddr)

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func sweepEvery(ctx context.Context, st *store.Store, clk clock.Clock) {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := st.Sweep(context.Background(), clk.Now())
			if err != nil {
				log.Printf("sweep failed")
				continue
			}
			log.Printf("sweep deleted %d", n)
		}
	}
}
