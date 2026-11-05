package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"
	"time"

	ckoerrors "github.com/checkout/checkout-sdk-go/v3/errors"
)

// app holds what the handlers share. api is the PaymentsAPI interface, never
// the SDK directly, so tests can pass a fake.
type app struct {
	cfg   Config
	store *Store
	api   PaymentsAPI
	ids   *orderIDGen
	now   func() time.Time
}

// newRouter wires every route. web is the front-end file system: os.DirFS("web")
// in main, an in-memory fstest.MapFS in tests.
func newRouter(a *app, web fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/config", handleConfig(a.cfg))
	mux.HandleFunc("POST /api/payment-sessions", a.handleCreateSession)
	mux.HandleFunc("GET /api/payments/{id}", a.handleGetPayment)
	// success_url lands here with ?cko-payment-id=. The page reads it and calls
	// GET /api/payments/{id}. The query never reaches the file server.
	mux.Handle("GET /success", pageHandler(web, "success.html"))
	mux.Handle("GET /", staticHandler(web))
	return mux
}

// createSessionRequest is all the browser may say: which basket, which country.
// Any other field in the body (an amount, a currency) is ignored on purpose.
type createSessionRequest struct {
	Basket  string `json:"basket"`
	Country string `json:"country"`
}

// orderSummary is what the checkout page shows next to Flow. Every amount
// comes from the server, including the weekly price in the recurring terms.
type orderSummary struct {
	ID            string `json:"id"`
	Basket        string `json:"basket"`
	Country       string `json:"country"`
	Currency      string `json:"currency"`
	Items         []Item `json:"items"`
	Amount        int64  `json:"amount"`
	RenewalAmount int64  `json:"renewal_amount,omitempty"`
	// Customer is the same demo data the session sends as customer and
	// billing, so the Delivery panel and Checkout.com never disagree.
	Customer customerSummary `json:"customer"`
}

type customerSummary struct {
	Name    string         `json:"name"`
	Email   string         `json:"email"`
	Address addressSummary `json:"address"`
}

type addressSummary struct {
	Line1   string `json:"address_line1"`
	City    string `json:"city"`
	Zip     string `json:"zip"`
	Country string `json:"country"`
}

func customerFor(country string) customerSummary {
	a := demoBilling[country]
	return customerSummary{
		Name:  demoCustomer.Name,
		Email: demoCustomer.Email,
		Address: addressSummary{
			Line1: a.AddressLine1, City: a.City, Zip: a.Zip, Country: string(a.Country),
		},
	}
}

// createSessionResponse wraps the session. payment_session is Checkout.com's
// raw response body, not the SDK struct, so Flow gets the same JSON value
// even if the API adds a field the SDK doesn't know (D-023). encoding/json
// compacts it, which changes whitespace only.
type createSessionResponse struct {
	Order          orderSummary    `json:"order"`
	PaymentSession json.RawMessage `json:"payment_session"`
}

const maxSessionBody = 1 << 10 // 1 KB: the body is two short strings

// handleCreateSession creates a new order and its Flow payment session.
// Every page load calls it, so an old session is never reused (F11).
func (a *app) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var body createSessionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSessionBody)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request.")
		return
	}
	b, err := LookupBasket(body.Basket, body.Country)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Unknown basket or country.")
		return
	}

	o := NewOrder(a.ids.Next(), b, body.Country, a.now())
	req, err := buildSessionRequest(o, b, sessionSettingsFrom(a.cfg))
	if err != nil {
		log.Printf("build session request: %v", err)
		writeError(w, http.StatusInternalServerError, "We couldn't start the payment. Please try again.")
		return
	}
	resp, err := a.api.CreateSession(r.Context(), req)
	if err != nil {
		writeAPIError(w, err, "We couldn't start the payment. Please try again.")
		return
	}
	raw := resp.HttpMetadata.ResponseBody
	if !json.Valid(raw) {
		log.Printf("create payment session %s: response body is not valid JSON", o.ID)
		writeError(w, http.StatusBadGateway, "We couldn't start the payment. Please try again.")
		return
	}
	// Store the order only once the session exists, so a failed session
	// leaves no orphan order behind.
	if err := a.store.CreateOrder(o); err != nil {
		log.Printf("store order: %v", err)
		writeError(w, http.StatusInternalServerError, "We couldn't start the payment. Please try again.")
		return
	}

	writeJSON(w, http.StatusCreated, createSessionResponse{Order: summaryFor(o), PaymentSession: raw})
}

// summaryFor builds the order summary from the stored order. A subscription
// has renewals (next_renewal_week 2 or more), so it shows the weekly price.
func summaryFor(o Order) orderSummary {
	s := orderSummary{
		ID: o.ID, Basket: o.Basket, Country: o.Country, Currency: o.Currency,
		Items: o.Items, Amount: o.Amount, Customer: customerFor(o.Country),
	}
	if o.NextRenewalWeek >= 2 {
		s.RenewalAmount = RenewalBasket().Total()
	}
	return s
}

// orderView is the order for the success page and ops panel: the summary,
// every payment's status card, and whether renewals can run. No source_id:
// the browser never needs the stored card's ID.
type orderView struct {
	orderSummary
	CITPaymentID string    `json:"cit_payment_id"`
	Scheme       string    `json:"scheme"`
	Payments     []Payment `json:"payments"`
	// RenewalsReady is false when the first payment gave no reusable card
	// (P-8) or failed. The page then disables the renewal button.
	RenewalsReady bool `json:"renewals_ready"`
}

func viewFor(o Order) orderView {
	payments := o.Payments
	if payments == nil {
		payments = []Payment{}
	}
	return orderView{
		orderSummary:  summaryFor(o),
		CITPaymentID:  o.CITPaymentID,
		Scheme:        o.Scheme,
		Payments:      payments,
		RenewalsReady: o.NextRenewalWeek >= 2 && o.CITPaymentID != "" && o.SourceID != "" && o.Scheme != "",
	}
}

type paymentResponse struct {
	Order   orderView `json:"order"`
	Payment Payment   `json:"payment"`
}

// handleGetPayment refreshes one payment from Checkout.com and stores it.
// The success page calls it with cko-payment-id, and the ops panel polls it
// after each action. Status always comes from these GETs (D-009).
func (a *app) handleGetPayment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := checkPaymentID(id); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid payment ID.")
		return
	}
	const failMsg = "We couldn't load this payment. Please refresh the page."
	details, err := a.api.GetPayment(r.Context(), id)
	if err != nil {
		writeAPIError(w, err, failMsg)
		return
	}
	// The response code is only on the actions (D-027). If this read fails,
	// answer 502 and store nothing: a retry is cheap, a half-filled card isn't.
	actions, err := a.api.GetActions(r.Context(), id)
	if err != nil {
		writeAPIError(w, err, failMsg)
		return
	}

	// Our references tie every payment to its order: ORD-1042 or ORD-1042-W2.
	o, ok := a.store.OrderForReference(details.Reference)
	if !ok {
		writeError(w, http.StatusNotFound, "This payment isn't for a Panier Box order.")
		return
	}
	p := mapPayment(details, actions.Actions, a.now())
	if o, err = a.store.UpsertPayment(o.ID, p); err != nil {
		log.Printf("store payment %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, failMsg)
		return
	}

	// Renewals reuse the first payment's card and scheme, so record them only
	// from an approved CIT on a subscription. A failed 3DS still returns a
	// source.id, and that card was never authenticated. RecordCIT never
	// overwrites, so a later GET or a renewal can't change them.
	if p.Kind == kindCIT && details.Approved && o.NextRenewalWeek >= 2 {
		if src, scheme := citDetails(details); src != "" && scheme != "" {
			if o, err = a.store.RecordCIT(o.ID, p.PaymentID, src, scheme); err != nil {
				log.Printf("record CIT %s: %v", id, err)
				writeError(w, http.StatusInternalServerError, failMsg)
				return
			}
		}
	}

	// The page polls this after an action, so never serve it from a cache.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, paymentResponse{Order: viewFor(o), Payment: p})
}

// errorBody is every error the API returns to the browser: a plain message,
// plus Checkout.com's status and error codes when there are some (F10).
type errorBody struct {
	Error      string   `json:"error"`
	CkoStatus  int      `json:"cko_status,omitempty"`
	ErrorCodes []string `json:"error_codes,omitempty"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

// writeAPIError answers 502 for a failed Checkout.com call. It logs our
// wrapped error (action, ID and HTTP status) and the error codes. Never headers.
func writeAPIError(w http.ResponseWriter, err error, msg string) {
	body := errorBody{Error: msg}
	var apiErr ckoerrors.CheckoutAPIError
	if errors.As(err, &apiErr) {
		body.CkoStatus = apiErr.StatusCode
		if apiErr.Data != nil {
			body.ErrorCodes = apiErr.Data.ErrorCodes
		}
	}
	log.Printf("%v %v", err, body.ErrorCodes)
	writeJSON(w, http.StatusBadGateway, body)
}

// publicConfig is everything the browser may know. It is its own struct, not
// built from Config, so a field added to Config later can't leak to the browser.
type publicConfig struct {
	PublicKey   string `json:"publicKey"`
	Environment string `json:"environment"`
}

// handleConfig gives Flow its public key. Names match the CheckoutWebComponents
// options, so the front end can pass them straight through.
func handleConfig(cfg Config) http.Handler {
	body := publicConfig{
		PublicKey:   cfg.PublicKey,
		Environment: "sandbox", // sandbox only, never production
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, body)
	})
}

// staticHandler serves files from web, with directory listings switched off:
// any path that is a directory (other than "/" with an index.html) is a 404.
func staticHandler(web fs.FS) http.Handler {
	files := http.FileServerFS(web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		info, err := fs.Stat(web, name)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// pageHandler serves one HTML file from web at a route without an extension.
func pageHandler(web fs.FS, name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, web, name)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are already sent, so all we can do is log it.
		log.Printf("write json response: %v", err)
	}
}
