package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/damuraiz/plex-helper/internal/server"
)

var version = "0.2.2"

func main() {
	listen := flag.String("listen", "127.0.0.1:8650", "HTTP listen address")
	data := flag.String("data", "./local-data", "Persistent settings and journal directory")
	initPassword := flag.Bool("init-password", false, "Initialize password from stdin without running the server")
	initDSM := flag.Bool("init-dsm", false, "Initialize settings without an application password")
	auth := flag.String("auth", "local", "Authentication mode: local or dsm")
	showVersion := flag.Bool("version", false, "Print version")
	cert := flag.String("tls-cert", "", "Optional TLS certificate file")
	key := flag.String("tls-key", "", "Optional TLS private key file")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	debug.SetMemoryLimit(256 << 20)
	if *initDSM {
		if _, err := server.OpenDSMStore(*data); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *initPassword {
		store, err := server.OpenStore(*data)
		if err != nil {
			log.Fatal(err)
		}
		bytes, err := io.ReadAll(io.LimitReader(os.Stdin, 1025))
		if err != nil {
			log.Fatal("could not read password")
		}
		if err = store.InitPassword(strings.TrimSuffix(strings.TrimSuffix(string(bytes), "\n"), "\r")); err != nil {
			log.Fatal(err)
		}
		fmt.Println("Password initialized.")
		return
	}
	if (*cert == "") != (*key == "") {
		log.Fatal("provide both --tls-cert and --tls-key")
	}
	if *auth != "local" && *auth != "dsm" {
		log.Fatal("unknown authentication mode")
	}
	if *auth == "dsm" && (!server.ValidDSMListen(*listen) || *cert != "") {
		log.Fatal("DSM mode requires a loopback listener; TLS is provided by DSM")
	}
	var app *server.App
	var err error
	if *auth == "dsm" {
		app, err = server.NewDSM(*data, version)
	} else {
		app, err = server.New(*data, version)
	}
	if err != nil {
		log.Fatal(err)
	}
	serverHTTP := &http.Server{Addr: *listen, Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		app.Engine.RequestStop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := serverHTTP.Shutdown(shutdownCtx); err != nil {
			log.Printf("Graceful shutdown timeout: %v; journal recovery may be required", err)
		}
		close(done)
	}()
	log.Printf("Plex Helper %s listening on %s; data %s", version, *listen, *data)
	if _, err = os.Stat(filepath.Join(*data, "setup-token")); *auth == "local" && err == nil {
		log.Printf("Initial setup required. Code is in %s", filepath.Join(*data, "setup-token"))
	}
	if *cert != "" {
		err = serverHTTP.ListenAndServeTLS(*cert, *key)
	} else {
		err = serverHTTP.ListenAndServe()
	}
	if err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	if ctx.Err() != nil {
		<-done
	}
}
