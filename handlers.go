package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"
	"sync"
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
	// events is the live log and webhook dedupe (D-039).
	events *eventLog
	// background counts webhook work still running after its 200, so tests
	// (and a clean shutdown) can wait for it.
	background sync.WaitGroup
}

// newRouter wires every route. web is the front-end file system: os.DirFS("web")
// in main, an in-memory fstest.MapFS in tests.
func newRouter(a *app, web fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/config", handleConfig(a.cfg))
	mux.HandleFunc("POST /api/payment-sessions", a.handleCreateSession)
	mux.HandleFunc("GET /api/payments/{id}", a.handleGetPayment)
	mux.HandleFunc("GET /api/orders/{orderId}", a.handleGetOrder)
	mux.HandleFunc("GET /api/events", a.handleEvents)
	mux.HandleFunc("POST /api/orders/{orderId}/capture", a.handleCapture)
	mux.HandleFunc("POST /api/orders/{orderId}/renewals", a.handleRenewal)
	mux.HandleFunc("POST /api/payments/{id}/reverse", a.handleReverse)
	mux.HandleFunc("POST /api/payments/{id}/capture", a.handlePaymentCapture)
	mux.HandleFunc("POST /webhooks/checkout", a.handleWebhook)
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
// every payment's status card, and whether renewals can run. source_id is
// shown in the back office band: one stored card reused for every renewal is
// the point of the demo. It's a token for a card stored at Checkout.com, not
// card data, and only works with our secret key (D-029, updated in 4.1a).
type orderView struct {
	orderSummary
	CITPaymentID    string        `json:"cit_payment_id"`
	SourceID        string        `json:"source_id"`
	Scheme          string        `json:"scheme"`
	NextRenewalWeek int           `json:"next_renewal_week"`
	Payments        []paymentView `json:"payments"`
	// RenewalsReady is false when the first payment gave no reusable card
	// (P-8) or failed. The page then disables the renewal button.
	RenewalsReady bool `json:"renewals_ready"`
	// CanCapture drives the Dispatch button. When false, CaptureReason says
	// why. Same rule as the capture handler, so page and server agree.
	CanCapture    bool   `json:"can_capture"`
	CaptureReason string `json:"capture_reason"`
	// CanRenew drives the renewal button, RenewReason says why not.
	CanRenew    bool   `json:"can_renew"`
	RenewReason string `json:"renew_reason"`
}

func viewFor(o Order) orderView {
	payments := make([]paymentView, len(o.Payments))
	for i, p := range o.Payments {
		action, why := reverseAction(p)
		payments[i] = paymentView{Payment: p, ReverseAction: action, ReverseReason: why, CanCapture: canCapture(p)}
	}
	_, reason, canCapture := captureTarget(o)
	return orderView{
		orderSummary:    summaryFor(o),
		CITPaymentID:    o.CITPaymentID,
		SourceID:        o.SourceID,
		Scheme:          o.Scheme,
		NextRenewalWeek: o.NextRenewalWeek,
		Payments:        payments,
		RenewalsReady:   o.NextRenewalWeek >= 2 && o.CITPaymentID != "" && o.SourceID != "" && o.Scheme != "",
		CanCapture:      canCapture,
		CaptureReason:   reason,
		CanRenew:        renewalBlock(o) == "",
		RenewReason:     renewalBlock(o),
	}
}

// paymentView is one status card: the stored payment plus what its buttons
// may do. View only, nothing extra is stored. The server decides, so the
// page never works out payment rules itself.
type paymentView struct {
	Payment
	ReverseAction string `json:"reverse_action"` // "void", "refund" or ""
	ReverseReason string `json:"reverse_reason"`
	// CanCapture shows the card's Capture button. No button otherwise.
	CanCapture bool `json:"can_capture"`
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
	o, p, err := a.syncPayment(r.Context(), id)
	if err != nil {
		writeSyncError(w, err, "We couldn't load this payment. Please refresh the page.")
		return
	}
	writePayment(w, o, p)
}

var (
	errNotOurOrder = errors.New("payment is not for a Panier Box order")
	errStoreFailed = errors.New("store failed")
)

// syncPayment GETs a payment's details and actions, stores the status card
// on its order and records the CIT if allowed. Every read of a payment goes
// through here: the success page, the poll after an action, later webhooks.
func (a *app) syncPayment(ctx context.Context, id string) (Order, Payment, error) {
	details, err := a.api.GetPayment(ctx, id)
	if err != nil {
		return Order{}, Payment{}, err
	}
	// The response code is only on the actions (D-027). If this read fails,
	// store nothing: a retry is cheap, a half-filled card isn't.
	actions, err := a.api.GetActions(ctx, id)
	if err != nil {
		return Order{}, Payment{}, err
	}

	// Our references tie every payment to its order: ORD-1042 or ORD-1042-W2.
	o, ok := a.store.OrderForReference(details.Reference)
	if !ok {
		return Order{}, Payment{}, errNotOurOrder
	}
	p := mapPayment(details, actions.Actions, a.now())
	o, changed, err := a.store.UpsertPaymentChanged(o.ID, p)
	if err != nil {
		return Order{}, Payment{}, fmt.Errorf("%w: payment %s: %v", errStoreFailed, id, err)
	}
	// A read goes on the live log only when it moved a card: a first sight,
	// or a new status. Ten polls of an unchanged payment add nothing (D-038).
	if changed {
		a.events.Add(logEntry{
			Time: a.now(), Kind: entryRead, Dir: dirOut, Text: "GET /payments/" + id,
			HTTPStatus: http.StatusOK, Status: p.Status, PaymentID: id, OrderID: o.ID,
		})
	}

	// Renewals reuse the first payment's card and scheme, so record them only
	// from an approved CIT on a subscription. A failed 3DS still returns a
	// source.id, and that card was never authenticated. RecordCIT never
	// overwrites, so a later GET or a renewal can't change them.
	if p.Kind == kindCIT && details.Approved && o.NextRenewalWeek >= 2 {
		if src, scheme := citDetails(details); src != "" && scheme != "" {
			if o, err = a.store.RecordCIT(o.ID, p.PaymentID, src, scheme); err != nil {
				return Order{}, Payment{}, fmt.Errorf("%w: record CIT %s: %v", errStoreFailed, id, err)
			}
		}
	}
	return o, p, nil
}

// writeSyncError maps a syncPayment error to a status: 404 for a payment
// that isn't ours, 500 for our own store, 502 for Checkout.com.
func writeSyncError(w http.ResponseWriter, err error, msg string) {
	switch {
	case errors.Is(err, errNotOurOrder):
		writeError(w, http.StatusNotFound, "This payment isn't for a Panier Box order.")
	case errors.Is(err, errStoreFailed):
		log.Print(err)
		writeError(w, http.StatusInternalServerError, msg)
	default:
		writeAPIError(w, err, msg)
	}
}

// writePayment answers with the order view and one status card. The page
// polls this after an action, so never serve it from a cache.
func writePayment(w http.ResponseWriter, o Order, p Payment) {
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

// revalidate is the Cache-Control for the front-end files: the browser may
// keep a copy but must ask before using it (a 304 when unchanged). Without
// it, browsers guess a freshness time from Last-Modified and can mix a new
// success.js with a stale lib.js, which broke the page in step 5.4b.
const revalidate = "no-cache"

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
		w.Header().Set("Cache-Control", revalidate)
		files.ServeHTTP(w, r)
	})
}

// pageHandler serves one HTML file from web at a route without an extension.
func pageHandler(web fs.FS, name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", revalidate)
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
