// Package app is the Panier Box Flow demo: the Go server for the checkout page,
// the ops panel and the API. The root main.go only calls Main.
package app

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/checkout/checkout-sdk-go/v3/configuration"
)

// Main starts the server. Paths (.env, data/, web/) are relative to the folder
// it runs from, so run it from the repo root: go run .
func Main() {
	if err := loadDotEnv(".env"); err != nil {
		log.Fatal(err)
	}
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	// Built at start so a bad key or subdomain fails now, not on the first payment.
	sdk, err := newPaymentsAPI(cfg, configuration.Sandbox(), nil, newSafeLogger(os.Stderr))
	if err != nil {
		log.Fatal(err)
	}
	events := newEventLog()
	api, sim := buildAPI(cfg, sdk, events, time.Now)
	// To reset: stop the server and delete data/orders.json.
	store, err := OpenStore(filepath.Join("data", "orders.json"))
	if err != nil {
		log.Fatal(err)
	}
	a := &app{
		cfg:    cfg,
		store:  store,
		api:    api,
		ids:    newOrderIDGen(store.OrderIDs()),
		now:    time.Now,
		events: events,
		sim:    sim,
	}

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: newRouter(a, os.DirFS("web")),
		// Without this, a client that sends headers very slowly holds a connection open forever.
		ReadHeaderTimeout: 10 * time.Second,
	}
	// Port and simulator settings only. Never log keys or the Config fields.
	log.Printf("listening on http://localhost:%s", cfg.Port)
	if sim != nil {
		log.Printf("simulated webhooks on, %s after each action (D-042)", cfg.SimulatedWebhookDelay)
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}

// buildAPI chains the PaymentsAPI wrappers: every action goes through the
// live log (D-038), and with the simulator on, through the simulator too,
// which posts the matching webhook to our own endpoint (D-042). sim is nil
// when switched off.
func buildAPI(cfg Config, sdk PaymentsAPI, events *eventLog, now func() time.Time) (PaymentsAPI, *webhookSimulator) {
	var api PaymentsAPI = newLoggedPayments(sdk, events, now)
	if !cfg.SimulateWebhooks {
		return api, nil
	}
	target := "http://127.0.0.1:" + cfg.Port + webhookPath
	sim := newWebhookSimulator(api, cfg.WebhookSignatureKey, target, cfg.SimulatedWebhookDelay, now)
	return sim, sim
}
