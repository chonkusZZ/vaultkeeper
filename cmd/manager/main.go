// Command vk-manager runs the Vaultkeeper web UI, API and scheduler.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"vaultkeeper/internal/server"
	"vaultkeeper/internal/store"
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func main() {
	listen := flag.String("listen", env("VK_LISTEN", ":8080"), "listen address")
	dataDir := flag.String("data-dir", env("VK_DATA_DIR", "./data"), "directory for the database")
	cert := flag.String("tls-cert", env("VK_TLS_CERT", ""), "TLS certificate (optional)")
	key := flag.String("tls-key", env("VK_TLS_KEY", ""), "TLS key (optional)")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		log.Fatal(err)
	}
	st, err := store.Open(filepath.Join(*dataDir, "vaultkeeper.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	srv, err := server.New(st)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv.SetInfo(server.Info{Listen: *listen, TLS: *cert != "" && *key != "", DataDir: *dataDir, DBPath: filepath.Join(*dataDir, "vaultkeeper.db"), Started: time.Now()})
	srv.Start(ctx)

	hs := &http.Server{Addr: *listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sc, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = hs.Shutdown(sc)
	}()
	log.Printf("Vaultkeeper manager listening on %s", *listen)
	if *cert != "" && *key != "" {
		err = hs.ListenAndServeTLS(*cert, *key)
	} else {
		err = hs.ListenAndServe()
	}
	if err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
