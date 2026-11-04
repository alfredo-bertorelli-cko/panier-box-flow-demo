// Panier Box Flow demo: Go server for the checkout page, the ops panel and the API.
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/checkout/checkout-sdk-go/v3/configuration"
)

func main() {
	if err := loadDotEnv(".env"); err != nil {
		log.Fatal(err)
	}
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	// Built at start so a bad key or subdomain fails now, not on the first
	// payment. Handlers start using it in step 2.4.
	if _, err := newPaymentsAPI(cfg, configuration.Sandbox(), nil, newSafeLogger(os.Stderr)); err != nil {
		log.Fatal(err)
	}

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: newRouter(cfg, os.DirFS("web")),
		// Without this, a client that sends headers very slowly holds a connection open forever.
		ReadHeaderTimeout: 10 * time.Second,
	}
	// Port only. Never log keys or the Config fields.
	log.Printf("listening on http://localhost:%s", cfg.Port)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}
