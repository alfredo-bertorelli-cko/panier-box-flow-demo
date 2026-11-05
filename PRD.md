# Panier Box Flow demo: PRD

Build spec for Claude Code. Read it with DECISIONS.md, which holds the reasons behind each choice (D, A, UC and S IDs point there). If the code and this PRD disagree, stop and ask.

## 1. Goal

A small web app that shows how Panier Box takes payments with Checkout.com Flow:

* A Panier Box checkout page where Marie pays for her first subscription box (a recurring CIT with 3DS).
* A success page with the payment details.
* An ops panel that does what Panier Box's back office would do: capture on dispatch, run next week's renewal (an MIT), and cancel (reverse).
* Webhooks that keep the payment record up to date (bonus in the pack).

It runs locally and goes public through a tunnel on the call. Julien wants to see it working before his team commits, so it must be reliable and easy to follow.

Pack rules: secret key only on the server, no raw card numbers anywhere, a short README, no API keys in the repo.

## 2. Scope

| Tier | What |
|---|---|
| Must | Checkout page with Flow (cards incl. CB, Google Pay). Recurring CIT session, 3DS on, authorise only. Success and failure handling. Payment record on the server. Ops panel: capture, renewal MIT, reverse, with idempotency keys. Status card per payment. Unit tests for every back-end module. README, .env.example |
| Nice | Webhook handler with signature check, dedupe and async processing. Live log of API calls and webhooks (server-sent events). One-off seasonal basket with a FR or PT switch (MB WAY for PT). No Alma (D-028) |
| Optional | Polish on the status card and live log |
| Out | Apple Pay, mobile apps, retries, Account Updater, network tokens, migration, estimated auth, partial capture, a database |

## 3. Stack and repo layout

* Go 1.27 or later (`go 1.27` in go.mod, D-018), standard library `net/http` with method routing.
* Module path `github.com/alfredo-bertorelli-cko/panier-box-flow-demo` (D-019).
* Checkout.com Go SDK v3: `github.com/checkout/checkout-sdk-go/v3` v3.8.0 for every Checkout.com API call (D-011, D-017). Pinned in go.mod. Don't upgrade mid-build.
* `github.com/joho/godotenv` v1.5.1 for `.env` (D-017). Ask before adding anything else.
* Plain HTML, CSS and JavaScript as ES modules. No framework, no bundler, no npm. The Go server serves the front end, so everything is same origin.
* Flow script only from `https://checkout-web-components.checkout.com/index.js`. Never downloaded, bundled or self-hosted (PCI).
* Storage: in memory, written to `data/orders.json` after each change and loaded on start (D-008).

```
panier-box-flow-demo/
  main.go            config, wiring, routes, server start
  config.go          reads and checks env variables
  baskets.go         demo baskets and amounts (server-side source of truth)
  ids.go             order IDs, renewal references, idempotency keys
  store.go           orders and payment records, JSON file store
  cko.go             PaymentsAPI interface + real implementation on the Go SDK
  requests.go        builds SDK request structs (session, capture, MIT, reverse)
  mapping.go         turns an SDK payment response into our status card data
  handlers.go        HTTP handlers for /api/*
  webhooks.go        signature check, dedupe, async processing
  events.go          live log and server-sent events
  *_test.go          unit and contract tests next to each file
  sandbox_test.go    sandbox tests, build tag "sandbox", I run them
  testdata/          JSON fixtures (no keys, ever)
  web/
    index.html  checkout.js     checkout page
    success.html  success.js    success page and ops panel
    lib.js                      pure helpers (formatting, polling, parsing), tested with node --test
    lib.test.js
    styles.css                  design tokens as CSS variables
    theme.js                    same tokens as the Flow appearance object
    assets/                     Panier Box logo (simple SVG we make), scheme badges
  inspiration/       competitor screenshots for UI ideas (gitignored)
  notes/             internal references (gitignored, see section 14)
  data/              orders.json (gitignored)
  .env.example  .gitignore  .claude/settings.json
  CLAUDE.md  PRD.md  DECISIONS.md  README.md
```

One Go package (`main`), split by job, so each file can be read and tested on its own.

## 4. Configuration

Read from `.env` with godotenv. Never hardcoded, printed or logged.

| Variable | What |
|---|---|
| `CKO_SECRET_KEY` | Sandbox secret key. Server only |
| `CKO_PUBLIC_KEY` | Sandbox public key. Sent to the browser by `/api/config` |
| `CKO_SUBDOMAIN` | First 8 characters of the client ID, without `cli_`. The SDK v3 needs it (`WithEnvironmentSubdomain`) |
| `CKO_PROCESSING_CHANNEL_ID` | `pc_...` |
| `CKO_WEBHOOK_SIGNATURE_KEY` | Signature key from the sandbox webhook settings |
| `PUBLIC_BASE_URL` | Where the browser reaches the app: `http://localhost:8080`, or the tunnel URL on the call. Used for success and failure URLs |
| `PORT` | Default 8080 |

On start, check every variable and stop with a message naming the missing one (name only).

Key scopes to confirm on Day 2 (D-008 point 7): public key `payment-sessions:pay` and `vault-tokenization`. Secret key: payment sessions, plus payments, get payment details, capture, void, refund.

SDK setup: `checkout.Builder().StaticKeys().WithEnvironment(configuration.Sandbox()).WithEnvironmentSubdomain(sub).WithSecretKey(sk).Build()`. Give it a logger that never prints headers or keys. Check what the SDK logs by default before the first sandbox call.

## 5. Architecture

```
Browser (checkout page + Flow, success page + ops panel)
   |  same origin: /api/*
Go server (localhost:8080, public through the tunnel)
   |-- Go SDK --> Checkout.com sandbox API (sessions, payments, captures, reversals)
   |<-- webhooks -- Checkout.com, via tunnel, POST /webhooks/checkout
   |-- data/orders.json
```

Who owns what:

* The server owns amounts and baskets. The browser only says which basket and country. It never sends an amount.
* The server creates the payment session and returns the session object to the browser unchanged. The browser initialises Flow with it.
* The browser passes back only `cko-payment-id`. The server looks the payment up itself.
* Payment status always comes from a server GET to Checkout.com. Webhooks trigger that GET but don't set the status themselves, because they can arrive late, twice, or out of order (D-009).
* The GET path works without the tunnel. With webhooks on, we have two paths to fresh status: polling after each action, and webhook-triggered GETs.

Code seams, so each part is testable alone:

* `PaymentsAPI` interface in `cko.go`: `CreateSession`, `GetPayment`, `Capture`, `Reverse`, `RequestMIT`. Handlers only use the interface. The real version calls the SDK. Tests use a fake.
* Request builders (`requests.go`) are pure functions: order in, SDK request struct out.
* Mapping (`mapping.go`) is a pure function: SDK payment response in, status card data out.

## 6. Data model (data/orders.json)

One file, one list of orders. Each order holds its payments. Webhook events are not saved (memory only, F8).

```json
{
  "orders": [
    {
      "id": "ORD-1042",
      "basket": "subscription",
      "country": "FR",
      "currency": "EUR",
      "items": [
        { "name": "Weekly box", "unit_price": 3000, "quantity": 1 },
        { "name": "Sourdough loaf", "unit_price": 600, "quantity": 1, "add_on": true },
        { "name": "Comté 200 g", "unit_price": 800, "quantity": 1, "add_on": true },
        { "name": "Seasonal fruit", "unit_price": 600, "quantity": 1, "add_on": true }
      ],
      "amount": 5000,
      "created_at": "2026-11-05T10:00:00Z",
      "source_id": "src_...",
      "scheme": "cartes_bancaires",
      "cit_payment_id": "pay_...",
      "payments": [
        {
          "payment_id": "pay_...",
          "kind": "CIT",
          "reference": "ORD-1042",
          "amount": 5000,
          "status": "Captured",
          "method": { "type": "card", "scheme": "visa", "card_schemes": ["visa"], "last4": "4242", "card_type": "Credit", "wallet": "" },
          "balances": { "authorized": 5000, "captured": 5000, "voided": 0, "refunded": 0 },
          "three_ds": "Y",
          "response_code": "10000",
          "response_summary": "Approved",
          "expires_on": "2026-11-12T10:00:00Z",
          "updated_at": "2026-11-05T10:05:00Z"
        }
      ],
      "next_renewal_week": 2
    }
  ]
}
```

Rules:

* Upsert by `payment_id`. A second GET or a duplicate webhook updates, never duplicates.
* `source_id` and `scheme` are set once, from the CIT.
* Write the file atomically: write a temp file, then rename.
* One mutex around the store. Handlers and webhook goroutines both write.
* Field names follow what we need on screen. Store our own struct, not the raw SDK response.

## 7. Demo data

Customer: Marie Dubois, marie.dubois@example.com, 12 rue Oberkampf, 75011 Paris, FR. Page copy in English with French details. Flow locale `en-GB`.

| Basket | Items | Amount | Session |
|---|---|---|---|
| Subscription, first box | Weekly box, EUR 30.00. Add-ons (flagged `add_on`): sourdough loaf EUR 6.00, Comté 200 g EUR 8.00, seasonal fruit EUR 6.00 | EUR 50.00 | Recurring, authorise only, `card` and `googlepay` |
| Renewal (MIT, ops panel) | Weekly box, EUR 30.00 | EUR 30.00 | Not a session. Server to server |
| One-off seasonal box (nice) | Christmas box, EUR 45.00 | EUR 45.00 | Regular, authorise only. PT adds `mbway` (D-007, D-028) |

Order IDs: `ORD-` plus a short number. Renewals: `ORD-1042-W2`, `ORD-1042-W3`. Every payment's reference is its order or renewal ID.

## 8. Server routes

| Route | Does |
|---|---|
| `GET /` | Checkout page |
| `GET /success` | Success page, reads `cko-payment-id` |
| `GET /api/config` | Public key and environment for Flow. Never the secret key |
| `POST /api/payment-sessions` | Body `{basket, country}`. Builds the session from server-side basket data, stores the order once the session exists. Returns 201 `{order, payment_session}`: the order summary, and the session object unchanged inside `payment_session` (D-023) |
| `GET /api/payments/{id}` | GETs the payment details and actions, upserts the record, records the CIT once if approved. Returns 200 `{order, payment}`: the order with every status card, `renewals_ready`, `source_id` and `next_renewal_week` for the back office band (D-029) |
| `POST /api/orders/{orderId}/capture` | Captures the first payment (dispatch) |
| `POST /api/orders/{orderId}/renewals` | Runs the next renewal MIT, authorise only. Subscription orders only |
| `POST /api/payments/{id}/reverse` | Reverse: void if not captured, refund if captured |
| `POST /api/payments/{id}/capture` | Capture one payment from its card, a renewal included (D-036). Same request and key as Dispatch |
| `POST /webhooks/checkout` | Webhook receiver (nice) |
| `GET /api/events?order={orderId}` | Server-sent events for the live log (nice): that order's lines from the last 50, then new ones, plus bad signatures (D-041) |
| `GET /api/orders/{orderId}` | The stored order view, no call to Checkout.com. The page re-reads it after a webhook (D-041) |

Failure URL: `/?payment=failed&cko-payment-id=...`.

## 9. Flows

### F1. Open checkout and create a session

1. Browser loads `/`, fetches `/api/config`.
2. Browser calls `POST /api/payment-sessions` with `{basket: "subscription", country: "FR"}`.
3. Server creates order `ORD-1042`, builds a `PaymentSessionsRequest` and calls `RequestPaymentSessions`. Same fields as this JSON:

```json
{
  "amount": 5000,
  "currency": "EUR",
  "reference": "ORD-1042",
  "payment_type": "Recurring",
  "processing_channel_id": "pc_...",
  "capture": false,
  "3ds": { "enabled": true, "challenge_indicator": "challenge_requested_mandate" },
  "locale": "en-GB",
  "billing": { "address": { "address_line1": "12 rue Oberkampf", "city": "Paris", "zip": "75011", "country": "FR" } },
  "customer": { "name": "Marie Dubois", "email": "marie.dubois@example.com" },
  "enabled_payment_methods": ["card", "googlepay"],
  "success_url": "{PUBLIC_BASE_URL}/success",
  "failure_url": "{PUBLIC_BASE_URL}/?payment=failed",
  "display_name": "Panier Box"
}
```

4. Server returns `{order, payment_session}` (D-023). `payment_session` is the session response (`id`, `payment_session_token`, `payment_session_secret`, links) unchanged, taken from the raw body, not the SDK struct. `order` is the summary for the page: ID, items, amount, and `renewal_amount` for subscriptions.
5. Browser: `CheckoutWebComponents({ publicKey, environment: "sandbox", locale: "en-GB", paymentSession, onPaymentCompleted, onError })`, then `checkout.create("flow").mount("#flow-container")`.

Known points:

* In SDK v3, `Capture` on the session request is `*bool`. Set it to a pointer to `false`. A contract test checks the JSON really contains `"capture": false`.
* No `merchant_initiated` on the session. A customer-initiated payment is the default (D-003).
* The recurring CIT sends `3ds.challenge_indicator: challenge_requested_mandate`. Issuers can decline later MITs with 20154 (3DS required) if the first payment wasn't strongly authenticated (D-022). The one-off session leaves it out (API default `no_preference`).
* `enabled_payment_methods` takes method names, not card schemes. CB comes through `card`.
* No `items` on the session (D-016). They're optional and not shown by Flow, and the SDK would add an undefined `"price": 0` to each. The order keeps its items (section 6), the server checks they add up to the amount (3000 + 600 + 800 + 600 = 5000), and the checkout page shows them as the order summary.
* Sessions last 24 hours. Every page load creates a new order and session, so an old session is never reused.

### F2. Pay without a redirect (frictionless card, Google Pay)

1. Flow fires `onPaymentCompleted(_self, paymentResponse)`.
2. Browser goes to `/success?cko-payment-id={paymentResponse.id}`.

### F3. Pay with 3DS challenge or redirect

1. Flow shows the challenge or redirects.
2. Checkout.com sends Marie to `success_url` or `failure_url` with `cko-payment-id`.
3. F2 and F3 end on the same success page.

### F4. Success page

1. Browser calls `GET /api/payments/{cko-payment-id}`.
2. Server calls `GetPaymentDetails`, finds the order by `reference`, upserts the CIT record, and sets `source_id` and `scheme` on the order.
3. Page shows "Thanks Marie, your order ORD-1042 is confirmed", basket summary, and the status card. No delivery date (D-024).
4. If `source.id` is missing on a Recurring payment, show it clearly and disable the renewal button (P-8).
5. If the payment status isn't Authorized or Captured, show the failure message and a link back to checkout.
6. A "Place another order" button sends the browser to `/`, which creates a fresh order and session (F11).

### F5. Dispatch (capture)

1. Ops clicks "Dispatch first box".
2. Server calls `CapturePayment(cit_id, {amount, reference}, key)` with key `capture-{cit_id}`.
3. Checkout.com answers 202 Accepted. The capture is asynchronous.
4. Browser polls `GET /api/payments/{id}` every second for up to 10 seconds, until the status changes. With webhooks on, the webhook-triggered GET also updates it.

### F6. Next week's renewal (MIT)

1. Ops clicks "Run next week's renewal". Server builds reference `ORD-1042-W{n}`.
2. Server calls `RequestPayment(request, key)` with key `renewal-ORD-1042-W{n}`. Same fields as:

```json
{
  "source": { "type": "id", "id": "src_..." },
  "amount": 3000,
  "currency": "EUR",
  "reference": "ORD-1042-W2",
  "payment_type": "Recurring",
  "merchant_initiated": true,
  "previous_payment_id": "pay_... (always the first CIT)",
  "capture": false,
  "processing_channel_id": "pc_...",
  "processing": { "preferred_scheme": "cartes_bancaires | visa | mastercard" }
}
```

3. No 3DS, no customer on screen. A new status card appears, labelled MIT. `next_renewal_week` goes up by one only after a successful response.
4. `previous_payment_id` always points at the first CIT. Checkout.com finds the scheme transaction ID and Mastercard TLID from it (D-007).
5. Check the exact `preferred_scheme` values in the API reference.

### F7. Reverse (cancel)

1. Each status card has a Reverse button.
2. Server calls `ReversePayment(id, request, key)` with key `reverse-{id}`. Checkout.com voids if not captured and refunds if captured (UC-9).
3. The card shows "Voided" or "Refunded" from the GET after the action (poll as in F5).
4. If Reverse isn't available on the account, fall back to `VoidPayment` when uncaptured and `RefundPayment` when captured. Log it in DECISIONS.md.

Demo story: reverse the uncaptured renewal (void) and the captured first payment (refund). Both outcomes, one checkout.

### F8. Webhooks (nice)

1. Checkout.com posts to `{tunnel}/webhooks/checkout`.
2. Read the raw body (limit 1 MB). HMAC-SHA256 it with `CKO_WEBHOOK_SIGNATURE_KEY`, hex-encode, compare with the `Cko-Signature` header using `hmac.Equal`. Mismatch: 401, log "bad signature", no body.
3. Parse `id`, `type`, `data.id`. Seen this event ID before: 200 and stop.
4. Remember the ID, add the event to the live log, return 200 at once. Checkout.com needs a 2XX within 10 seconds (S-03).
5. In a goroutine: if the payment belongs to a known order, GET it, upsert, push an SSE message.
6. Event store: a set of seen IDs plus the last 50 events, in memory. Lost on restart, fine for a demo. Panier Box would persist it.
7. Unknown payment IDs (other tests on the same sandbox): log and return 200.

Which key signs: on the current platform it's the signature key set on the webhook. Some older support articles say the secret key. If verification fails with a correct-looking setup, check this first, and check nothing changes the raw body.

Events to subscribe: `payment_approved`, `payment_declined`, `payment_pending`, `payment_captured`, `payment_capture_declined`, `payment_voided`, `payment_void_declined`, `payment_refunded`, `payment_refund_declined`, `payment_expired`.

### F9. Basket or country switch (nice)

1. Demo control at the top: "Subscription" or "One-off seasonal box". Country selector (FR or PT), one-off only.
2. Any switch creates a new order and session, then mounts Flow again. `update()` can't change payment type, billing country or methods (D-009).
3. Subscription and one-off FR: `card`, `googlepay`. One-off PT: adds `mbway`, billing country PT.
4. One-off orders have no renewal button.
5. MB WAY captures at payment. The sandbox accepted our `capture: false` session and the payment came back Captured (S-07, 2026-11-05). So the session stays as is. Ops buttons follow the balances, not the method: no Dispatch when `available_to_capture` is 0, and show "Captured at payment. Cancel means a refund". Cancel uses Reverse, which refunds a captured payment.

### F10. Errors and edge cases

| Case | What happens |
|---|---|
| Card declined, no redirect | Flow `onError`. Short message on checkout. Flow lets Marie try again |
| 3DS failed or abandoned | `failure_url`. Message, new order and session |
| Redirect lands on success but payment is Declined | Success page checks status (F4 step 5) |
| MB WAY not confirmed in time | Status Expired (GET, or `payment_expired`). Card shows it |
| Session older than 24 hours | Not possible in normal use, every load makes a new one |
| Double click on any ops button | Same idempotency key, one action at Checkout.com |
| Capture on a voided payment, reverse twice | Checkout.com returns an error. Show status and `error_codes` |
| Renewal without `source_id` | Button disabled with a reason |
| Webhook bad signature | 401 |
| Webhook for an unknown payment | 200, logged, ignored |
| Tunnel down | Everything except webhooks still works through GET and polling |
| Checkout.com 4xx or 5xx | Show HTTP status and `error_codes` in ops panel and live log. Never headers |

### F11. Start again

1. Success page, failure banner and ops panel all have a "Place another order" button. It goes to `/`.
2. `/` always creates a new order and a new session. No state from the last order carries over in the browser.
3. Old orders stay in `data/orders.json`. To clear everything before the call: stop the server and delete `data/orders.json`. The README says how.

## 10. UI

### Checkout page

* Panier Box header with a simple SVG logo we make. Fresh food-brand look: deep green, warm orange, cream background. Use `inspiration/` screenshots for layout and tone only. No copying logos, images or text.
* Step bar: Box, Delivery, Payment. Payment active. Delivery details filled in and read-only.
* Two columns on desktop, one on mobile. Left: delivery summary and Flow. Right: order summary with items (add-ons tagged), total. No delivery date (D-024).
* Recurring terms right above Flow: "You pay EUR 50.00 today for your first box, including EUR 20.00 of add-ons. Then EUR 30.00 every week until you cancel. Your card is authorised when you order and charged when your box ships. Skip, pause or cancel any time." Also dispute evidence (UC-15).
* Small "Demo" strip at the top for the basket and country switch, clearly separate from the Panier Box page.

### Flow styling

We don't demo customisation live. But Flow must look like part of the Panier Box page, not a foreign widget.

* Design tokens live once in `styles.css` as CSS variables: colours, font family, font sizes, border radius, spacing.
* `web/theme.js` mirrors those tokens in one object and passes it to `CheckoutWebComponents` as `appearance` (colours, borders, radius, typography). Check the exact keys in the Appearance options and library reference docs.
* Same font as the page where Flow allows it. If Flow can't load our font, pick the closest system font for both.
* Flow's pay button follows the page's primary button style (colour, radius, height).
* Spacing around Flow matches the cards and sections around it.
* Manual check in step 2.6 and again in 5.7: side by side, nothing in Flow looks off-palette or off-font.

### Success page and ops panel

* Top: confirmation, order ID, basket summary.
* Below a clear divider: "Panier Box back office (demo)". Buttons: Dispatch first box, Run next week's renewal. Status cards, newest first.
* Live log (nice): API calls ("POST /payments/{id}/captures 202") and webhooks ("payment_captured evt_..."). IDs and status only.

### Status card

| Shown | In a "Details" fold |
|---|---|
| Amount and status badge | 3DS result |
| CIT or MIT | Response code and summary |
| Payment ID and reference | Authorisation expiry (`expires_on`) |
| Scheme badge, last 4, card type | |
| Google Pay, if used | |
| Balances: authorised, captured, voided, refunded | |
| Reverse button | |

Scheme badges are local SVGs or styled text. No hotlinked logos.

## 11. Testing approach

Tests come with each step, not at the end. A step is done only when its tests pass and I've run the manual check.

| Layer | What | Tool | Who runs it |
|---|---|---|---|
| Unit | Pure Go: baskets, IDs, idempotency keys, request builders, mapping, store, signature, dedupe | `go test ./...`, table-driven tests | Claude and me |
| Handler | HTTP handlers with a fake `PaymentsAPI` | `httptest.NewRecorder` | Claude and me |
| Contract | The JSON and headers the SDK really sends. Check the full body, and flag any zero value the SDK sends that we didn't set (D-016) | `httptest.NewServer` as a fake Checkout.com, SDK pointed at it with `configuration.NewEnvironment` | Claude and me |
| Sandbox | Real calls to the sandbox | `go test -tags sandbox -run ...`, keys from `.env` | Me only. Claude writes them and tells me the command |
| Front end | Pure JS helpers: amount formatting, URL parsing, polling stop rules | `node --test web/` (Node's built-in runner, no npm). Skipped if Node isn't installed | Claude and me |
| Manual | Flow, 3DS, Google Pay, the page | Browser checklist per step | Me |

Rules:

* Fixtures in `testdata/` come from API reference examples or sandbox responses I paste in. Never keys. Claude checks every fixture for key-like strings before saving it.
* Fake keys in tests are obviously fake, like `sk_sbox_fake`.
* Each test has a one-line comment on what it proves.
* When a test needs something from me (test card, payment ID, tunnel URL), Claude asks for it and says why.
* After each step, Claude explains in a few sentences what the tests cover, what they don't, and how to run them.

## 12. Build plan

Five phases, small steps. Each step: plan, code plus tests, run tests, manual check, stop for my review, I commit.

### Phase 0: Setup

| Step | Build | Tests | Done when |
|---|---|---|---|
| 0.1 | `go mod init`, folders, `.gitignore`, `.env.example`, pin SDK v3 and godotenv | `go build`, `go vet` | Builds. `git status` shows no `.env`, `data/`, `inspiration/`, `notes/` |
| 0.2 | `config.go`: load and check env | Missing variable gives an error naming it. The error never contains a value | Tests pass |
| 0.3 | Static file server, `GET /api/config` | Handler returns the public key. Response never contains the secret key | Page loads at localhost:8080 |

### Phase 1: Domain basics (no network)

| Step | Build | Tests | Done when |
|---|---|---|---|
| 1.1 | `baskets.go`: demo baskets | Subscription totals 5000, items add up, renewal 3000, one-off 4500. Unknown basket is an error | Tests pass |
| 1.2 | `ids.go`: order IDs, renewal refs, idempotency keys | `ORD-1042-W2` format. Same inputs, same key. Different actions, different keys | Tests pass |
| 1.3 | `store.go`: memory plus JSON file | Upsert by payment ID doesn't duplicate. Save then load gives the same data (`t.TempDir()`). Concurrent writes are safe (`go test -race`) | Tests pass with `-race` |

### Phase 2: Payment session and checkout

| Step | Build | Tests | Done when |
|---|---|---|---|
| 2.1 | `cko.go`: `PaymentsAPI` interface, SDK client setup, safe logger | Client builds with fake keys. Contract test: a call reaches the fake server with the right path | Tests pass |
| 2.2 | `requests.go`: session request per basket and country | Table test: subscription FR is Recurring, card plus googlepay, capture false, 3DS on with `challenge_requested_mandate`, amounts from basket, no items. One-off FR is card plus googlepay, PT adds mbway with billing PT | Tests pass |
| 2.3 | Contract test for the session JSON | Body sent by the SDK has `"capture": false`, `"payment_type": "Recurring"`, `"3ds": {"enabled": true, "challenge_indicator": "challenge_requested_mandate"}`, no `items` | Tests pass |
| 2.4 | `POST /api/payment-sessions` handler | Fake API: returns session unchanged, rejects unknown basket, ignores any amount in the body | Tests pass |
| 2.5 | Sandbox test: create one real session | `go test -tags sandbox -run TestSandboxCreateSession -v ./...` returns a `ps_` ID. Sandbox tests are named `TestSandbox...` so `-run` never matches unit tests | I run it, it passes |
| 2.6 | Checkout page, Flow mount, Flow styling from `theme.js` | `lib.js` helpers tested with node. Manual: page and Flow load, card form shows, Flow matches the page palette and font | Flow visible and on-brand |

### Phase 3: Payment result

| Step | Build | Tests | Done when |
|---|---|---|---|
| 3.1 | `mapping.go`: payment response to status card data | Fixtures: card CIT Authorized, Google Pay, declined, captured, voided, refunded. Each field maps right | Tests pass |
| 3.1b | Sandbox findings: GET payment actions, real shapes | Response code from `GET /payments/{id}/actions` (D-027). `ref_*` fixtures match real shapes. MB WAY source type. Contract test for the actions path | Tests pass, re-dumps map |
| 3.2 | `GET /api/payments/{id}` handler | Fake API: upserts, finds order by reference, sets `source_id` once and only when `approved` (a failed 3DS still has a `source.id`), unknown reference is a clean error | Tests pass |
| 3.3 | Success page, failure banner, "Place another order" | JS helper reads `cko-payment-id`. Manual: frictionless card, 3DS challenge card, declined card, 3DS fail, then start again | All four look right and a new order starts clean |
| 3.4 | Sandbox checks | `source.id` present (P-8). Status Authorized, not Captured. CB brand choice on a co-badged card (P-11). Note `expires_on` | Findings logged in DECISIONS.md |

### Phase 4: Ops actions

| Step | Build | Tests | Done when |
|---|---|---|---|
| 4.1 | Capture: builder, handler, button | Contract: path, amount, `Cko-Idempotency-Key: capture-{id}`. Handler: no CIT gives an error | Manual capture works |
| 4.2 | Polling helper | node test: stops on status change, stops after 10 tries | Card updates after capture |
| 4.3 | Renewal MIT: builder, handler, button | Builder: `merchant_initiated` true, `previous_payment_id` always the CIT (also for W3), capture false, `preferred_scheme` from CIT, source type id. Handler: blocked without `source_id` or on a one-off | Manual renewal works, new MIT card |
| 4.4 | Reverse: builder, handler, buttons | Contract: path and key. Fallback picks void or refund from status | Void on MIT, refund on CIT |
| 4.5 | Sandbox checks | Double click sends one action. `expires_on` on the MIT (5-day Visa window, D-004) | Findings logged |

### Phase 5: Webhooks and extras

| Step | Build | Tests | Done when |
|---|---|---|---|
| 5.1 | Signature check | Known body and key give the expected hex. Changed body fails. Missing header fails | Tests pass |
| 5.2 | Dedupe and last-50 log | Same event twice is processed once. Log keeps 50 | Tests pass |
| 5.3 | Webhook handler | 401 on bad signature. 200 fast on good. Async GET runs (fake API). Unknown payment gives 200 | Tests pass |
| 5.4 | SSE live log | Handler test: a published event reaches a connected client | Log shows events |
| 5.5 | Tunnel and sandbox webhook setup | Manual: events arrive, signature passes, cards update | Works end to end |
| 5.6 | Basket and country switch (F9) | Builder tests from 2.2 cover the sessions. Manual: card and Google Pay on FR, MB WAY on PT | Works or finding logged |
| 5.7 | Google Pay, polish, README, DECISIONS trim | Full manual run of the demo script | Ready to submit |

## 13. README (for the panel)

Short. What it shows and how it maps to Panier Box. Prerequisites: Go 1.27 or later (D-018), sandbox keys and scopes, subdomain, processing channel, payment methods (cards, Google Pay, MB WAY), webhook URL and events. Steps: copy `.env.example` to `.env`, fill it, `go run .`, open the page, start the tunnel. How to run the tests. Test cards. Out of scope in two lines.

## 14. References

Order of trust: API reference first, then docs, then the SDK source. If they disagree, stop and tell me.

API reference

* [API reference](https://api-reference.checkout.com)
* [Request a payment session](https://api-reference.checkout.com/#operation/CreatePaymentSession)
* [OpenAPI spec (swagger.yaml)](https://api-reference.checkout.com/v1/swagger.yaml): machine-readable, good for exact field names

Flow (web)

* [Accept a payment on your website (Flow)](https://www.checkout.com/docs/payments/accept-payments/accept-a-payment-on-your-website)
* [Get started with Flow](https://www.checkout.com/docs/payments/accept-payments/accept-a-payment-on-your-website/get-started-with-flow)
* [Customize your Flow integration](https://www.checkout.com/docs/payments/accept-payments/accept-a-payment-on-your-website/customize-your-flow-integration)
* [Appearance options](https://www.checkout.com/docs/payments/accept-payments/accept-a-payment-on-your-website/customize-your-flow-integration#Appearance_options)
* [Flow library reference](https://www.checkout.com/docs/payments/accept-payments/accept-a-payment-on-your-website/flow-library-reference)
* [Flow demo next steps](https://flow-demo.cko.solutions/next-steps): Checkout.com's own list of the key Flow links
* [Flow library reference: CheckoutError](https://www.checkout.com/docs/payments/accept-payments/accept-a-payment-on-your-website/flow-library-reference/checkouterror)
* [Dynamically adjust the payment amount](https://www.checkout.com/docs/payments/accept-payments/accept-a-payment-on-your-website/extend-your-flow-integration/dynamically-adjust-the-payment-amount)
* [Key scopes for Flow (tokenize credentials)](https://www.checkout.com/docs/payments/store-and-manage-credentials/tokenize-credentials)
* [Add payment methods (Google Pay, MB WAY, Cartes Bancaires)](https://www.checkout.com/docs/payments/add-payment-methods)
* [MB WAY](https://www.checkout.com/docs/payments/add-payment-methods/mb-way)
* [Cartes Bancaires](https://beta.checkout.com/docs/payments/payment-methods/cartes-bancaires)

Recurring and payment actions

* [Recurring payments with stored card details](https://www.checkout.com/docs/payments/accept-payments/pay-with-stored-card-details/recurring-payments-with-stored-card-details)
* [Pay with stored card details](https://www.checkout.com/docs/payments/accept-payments/pay-with-stored-card-details)
* [Remember Me (instrument ID on recurring)](https://www.checkout.com/docs/payments/accept-payments/accept-a-payment-on-your-mobile-app/extend-your-flow-for-mobile-integration/enable-remember-me)
* [Authorize a payment](https://www.checkout.com/docs/payments/manage-payments/authorize-a-payment)
* [Capture a payment](https://www.checkout.com/docs/payments/manage-payments/capture-a-payment)
* [Reverse a payment](https://www.checkout.com/docs/payments/manage-payments/reverse-a-payment)
* [Get payment actions](https://www.checkout.com/docs/payments/manage-payments/get-payment-actions)
* [SCA compliance](https://www.checkout.com/docs/business-operations/ensure-regulatory-compliance/sca-compliance)
* [Mastercard Transaction Link Identifier](https://support.checkout.com/hc/en-us/articles/33589249067538-Mastercard-Transaction-Link-Identifier)

Webhooks

* [Set up your webhook receiver (signatures)](https://www.checkout.com/docs/developer-resources/webhooks/manage-webhooks/set-up-your-webhook-receiver)
* [Configure your webhook server](https://www.checkout.com/docs/developer-resources/event-notifications/receive-webhooks/configure-your-webhook-server)
* [Webhook event types](https://www.checkout.com/docs/developer-resources/event-notifications/event-types)
* [Acknowledge a webhook](https://support.checkout.com/hc/en-us/articles/29686471128722-How-to-acknowledge-my-server-received-a-webhook)
* [Verify a webhook was sent by Checkout.com](https://support.checkout.com/hc/en-us/articles/29686673313426-Verify-a-webhook-was-sent-by-Checkout-com)
* [Webhook signature verification failures](https://support.checkout.com/hc/en-us/articles/29686817111954-Webhook-signature-verification-failures)

Go SDK

* [checkout-sdk-go on GitHub](https://github.com/checkout/checkout-sdk-go): README covers setup, subdomain, custom environment for mock servers, logging, telemetry
* [Releases](https://github.com/checkout/checkout-sdk-go/releases): read the notes for the pinned version
* [payments/sessions package](https://pkg.go.dev/github.com/checkout/checkout-sdk-go/v3/payments/sessions)
* [payments/nas package (payments, capture, void, refund, reverse)](https://pkg.go.dev/github.com/checkout/checkout-sdk-go/v3/payments/nas)

Testing

* [Test cards](https://www.checkout.com/docs/testing/test-cards)
* [Improve payment request data quality](https://www.checkout.com/docs/payments/optimize-payments/improve-payment-request-data-quality)

Internal context: `notes/internal-refs.md` (gitignored, not in the repo). Checkout.com internal pages and Slack threads, with the takeaway from each written out, since Claude Code can't open internal links.
