# Panier Box: Decision Log

Why the demo is built the way it is. The use case map comes first, then the decisions (D entries) in the order we made them. Replaced decisions stay in the log, marked as replaced. PRD.md has the build spec.

## Index

| ID | Summary |
|---|---|
| UC-1 to UC-15 | Use case map: who starts each payment, CIT or MIT, when to authorise and capture |
| D-001 | Use an existing sandbox where I can enable payment methods |
| D-002 | No separate smoke test, one early check for a reusable card |
| D-003 | First subscription payment is a Recurring CIT with 3DS |
| D-004 | Authorise renewals close to dispatch, read each payment's `expires_on` |
| D-005 | Demo scope: checkout, success page, back office actions |
| D-006 | Stack: Go back end, plain JS front end, web only |
| D-007 | Scope tiers: must, nice to have, out |
| D-008 | Design: status from our GET, idempotency keys, local run. Tunnel part replaced by D-042 |
| D-009 | Sessions, status card, webhook rules, secrets |
| D-010 | Build architecture and stable idempotency keys |
| D-011 | Use the Checkout.com Go SDK v3, with contract tests |
| D-012 | Build in small tested steps |
| D-013 | Only public sources in the repo |
| D-014 | Check that every decision is covered in PRD.md |
| D-015 | Flow styling follows the page tokens. Start again button |
| D-016 | SDK sends `"price": 0` on session items, so no items on the session |
| D-017 | Pin SDK v3.8.0 and godotenv v1.5.1 |
| D-018 | `go 1.27` in go.mod |
| D-019 | Module path matches the GitHub repo |
| D-020 | Config never prints a value, even on a bad `.env` |
| D-021 | What the SDK logs and sends, and how tests reach a fake server |
| D-022 | Session fields, `challenge_requested_mandate` on the recurring CIT |
| D-023 | Session endpoint returns the order summary and the raw session |
| D-024 | Checkout page data and copy |
| D-025 | Flow appearance mirrors the page tokens |
| D-026 | Mapping GET payment to the status card, scheme from `processing.scheme` |
| D-027 | Response code from GET payment actions, real sandbox shapes |
| D-028 | Alma removed from scope |
| D-029 | GET payment handler, when the CIT is recorded |
| D-030 | Success page and failure banner |
| D-031 | Back office band, read only |
| D-032 | Dispatch captures the first payment |
| D-033 | Polling after an action |
| D-034 | Renewal MIT, linked to the first CIT every week |
| D-035 | Reverse on each status card: void or refund |
| D-036 | Capture on each status card, a renewal included |
| D-037 | Webhook signature check on the raw body. Hex check replaced by D-042 |
| D-038 | Live log shows actions, webhooks, and reads that change a status |
| D-039 | One event log for webhook dedupe and the live log |
| D-040 | Webhook handler: verify, dedupe, answer 200, GET after |
| D-041 | Live log over server-sent events |
| D-042 | Simulated webhooks replace the tunnel |
| D-043 | Subscriptions also in PT, without MB WAY |
| D-044 | Reusable card, co-badged brand choice, MB WAY capture, webhook reply time |

## Use case map

### Round 1

Built from the pack plus the assumptions in the README. Round 2 adds failed renewals, cancellations, skips and pauses, card changes, and what happens after delivery: refunds, missing items, disputes.

| ID | Use case | Who starts it | Type | Authorise | Capture | 3DS | Based on |
|---|---|---|---|---|---|---|---|
| UC-1 | First order: box plus any add-ons in one basket | Marie, on web or app, through Flow | CIT, first in a recurring series | At order | At dispatch. Up to 10 days if the first delivery is later (D-004) | Yes, required | Pack, D-003 |
| UC-2 | Normal weekly renewal | Panier Box, server to server | MIT, linked to UC-1 | At the weekly order point | At dispatch. Gap not in the pack (D-004) | No, out of scope | Pack, assumption |
| UC-3 | Scheduled box | Panier Box | MIT | At order | At dispatch, up to 10 days later. Past every window (D-004) | No, out of scope | Pack, assumption |
| UC-4 | Pre-ordered box | Marie | CIT, new or saved card | At order | At dispatch, up to 10 days later. Past most windows (D-004) | Yes, unless an exemption applies | Pack, assumption |
| UC-5 | Add-on after the box is authorised | Marie, in the app | CIT on the saved card, its own payment | At order | At dispatch | Yes. SCA applies to online CITs, unless an exemption applies | Assumption, agreed 2026-11-04 |
| UC-6 | Renewal for a subscriber who signed up at DimaPay | Panier Box | MIT, linked by scheme transaction ID (plus Mastercard TLID) | As UC-2 | As UC-2 | No, out of scope | Pack, assumption |

#### What the 10-day gap runs into (checked in docs, 2026-11-03)

Authorisations expire. Checkout.com treats every payment as a final authorisation unless told otherwise. You can still capture after expiry, but the docs list the risks: a failed capture, higher interchange, scheme fees and chargebacks. Source: [Authorize a payment](https://www.checkout.com/docs/payments/manage-payments/authorize-a-payment).

| Scheme | Final authorisation lasts | Estimated authorisation lasts |
|---|---|---|
| Visa | 5 days for MITs, 10 days for CITs | 10 days, can't be extended. Unclear for MITs |
| Mastercard | 7 days | 30 days, reset by each incremental authorisation |
| Cartes Bancaires | 7 days | Listed as not applicable in an older doc |
| Amex | 7 days | 7 days |

Visa's own rule matches: 5 days for every MIT, 10 for online CITs ([Visa AI13522](https://usa.visa.com/content/dam/VCOM/regional/na/us/support-legal/documents/authorization-framework-will-be-updated-to-simplify-authorization-processing-time-frames.pdf)).

What it means:

* UC-2: fine only if renewals are authorised within 5 days of capture.
* UC-3: past every window.
* UC-4, and UC-1 with a late first box: past Mastercard, CB and Amex. Right at the limit on Visa.
* Overlap: with weekly renewals and a 10-day gap, one card carries two open authorisations on days 7 to 10 of each cycle. It works, since each box is its own payment and the link to the sign-up payment comes from its authorisation, not its capture. The costs: Marie sees two pending charges, and on a debit card the first hold eats into her balance, so the next renewal is more likely to decline for insufficient funds.
* Options and direction: D-004.

### Round 2

Drafted 2026-11-04. What we decided for each one is in "Round 2 decisions" below the table.

| ID | Use case | What we do at Checkout.com | Working assumption |
|---|---|---|---|
| UC-7 | Renewal soft decline, like insufficient funds | Retry within scheme limits: Visa 20 tries in 30 days, Mastercard 10 in 24 hours or 35 in 30 days. Checkout.com's Smart Dunning (beta) can run the schedule for MITs. Timeouts can get automatic downtime retries | Panier Box runs its own daily retries today, maybe through DimaPay |
| UC-8 | Renewal hard decline: lost, stolen, closed account, or Marie stopped payments at her bank | Never retry. Ask Marie for a new card (UC-11). A stop-payment code means she cancelled through her bank, so treat it as a cancellation | Today's 14-day retry hits these too |
| UC-9 | Marie cancels a box or the subscription before dispatch | Void if not captured. The Reverse a payment endpoint voids, refunds or cancels pending retries depending on status. Stop future MITs | "We refund them" in the pack is loose wording, or a real refund after an early capture |
| UC-10 | Skip a week or pause | No authorisation that week. The next MIT links back as usual | Skips and pauses exist, like in most meal kits |
| UC-11 | Marie changes her card | Set up the new card with SCA. A zero-amount card verification works, flagged Recurring with merchant_initiated false. Later MITs link to it | Possible on web and app |
| UC-12 | Card expires or is replaced | Real-Time Account Updater (Visa and Mastercard) and network tokens keep the card current. Enabled through the account manager | DimaPay may do this today |
| UC-13 | Item out of stock at packing | Partial capture with capture_type Final, which releases the rest | Substitutions or removals happen |
| UC-14 | Missing or damaged item after delivery | Partial refund on the captured payment. Refunds to an expired card still work | Customer service handles it |
| UC-15 | Dispute | Evidence by reason: recurring agreement and cancellation policy for "canceled recurring", proof of delivery for "not received". 3DS moves fraud liability to the issuer, but MITs have no 3DS, so fraud disputes on renewals likely stay with Panier Box. CB fraud code 45 can't be defended. Already-refunded payments resolve automatically | Disputes can rise after a PSP change if the bank statement name changes |

Sources: [Smart Dunning retries](https://www.checkout.com/docs/payments/manage-payments/retry-a-payment/smart-dunning-retries), [Automatic downtime retries](https://www.checkout.com/docs/payments/manage-payments/retry-a-payment/automatic-downtime-retries), [Reverse a payment](https://www.checkout.com/docs/payments/manage-payments/reverse-a-payment), [SCA compliance](https://www.checkout.com/docs/business-operations/ensure-regulatory-compliance/sca-compliance), [Real-Time Account Updater](https://www.checkout.com/docs/payments/store-and-manage-credentials/update-stored-credentials/real-time-account-updater), [Capture a payment](https://www.checkout.com/docs/payments/manage-payments/capture-a-payment), [Dispute reasons and evidence](https://www.checkout.com/docs/payments/process-disputes/dispute-reasons-and-recommended-evidence), [How disputes work](https://www.checkout.com/docs/business-operations/manage-disputes/how-disputes-work). Retry limits are from third-party summaries of the April 2026 Visa rules, for example [Paysight](https://www.paysight.io/blogs/soft-vs-hard-declines). Not checked against the scheme rules themselves.

#### Round 2 decisions (agreed 2026-11-04)

| ID | Decision | In the demo |
|---|---|---|
| UC-7 | 14 daily tries fit the soft-decline limits. Keep as is | Not built |
| UC-8 | Never retry hard declines | Not built |
| UC-9 | Void if not captured, refund if captured | Built: Reverse on each status card (D-035) |
| UC-10 | The pack doesn't mention skips. Added from how most meal kits work. A skipped week means no MIT that week, and the next one still links to the first CIT | Not built |
| UC-11 | New card: zero-amount verification with 3DS, as Recurring with merchant_initiated false. Next MITs use its payment ID as previous_payment_id, any amount | Not built |
| UC-12 | Account Updater and network tokens. No integration work, and it protects renewal revenue | Not built |
| UC-13 | Some items missing: partial capture. Whole box missing: void, since they capture at dispatch | Void built. Partial capture not built |
| UC-14 | Partial refund | Not built |
| UC-15 | MITs have no liability shift. CITs with 3DS (first box, pre-orders, add-ons) do get it. 3RI could add it for MITs | Not built, later phase |

## Decisions

### D-001: Use an existing sandbox

* Status: Decided, 2026-11-03. Area: process.
* Context: The demo needs a sandbox with cards, Google Pay and MB WAY enabled. I already had one where I can enable payment methods myself.
* Options: (A) Create a new sandbox through the public sign-up and request payment methods by email. (B) Use the existing sandbox and enable methods myself.
* Choice: B.
* Why: It's already set up and I control the configuration, so there's no waiting.
* Trade-off accepted: An existing sandbox may have settings a new merchant's sandbox wouldn't, so something could work in the demo that wouldn't for Panier Box.
* Mitigation: We avoid anything that depends on this account's settings. The keys and IDs the demo needs are shared privately (README).
* What would change my mind: a feature turns out to work only because of this account's settings.

### D-002: No separate smoke test

* Status: Decided, 2026-11-03. Area: process.
* Context: An optional test payment through Flow before the build, only to prove the setup works.
* Options: (A) Do it first. (B) Skip it and let the first build slice act as the smoke test.
* Choice: B, plus one early check that a Flow payment stores a reusable card (D-044). Every renewal depends on it.
* Why: The first day went to understanding the business, and the sandbox was one I already use.
* Trade-off accepted: A setup problem (keys, Flow loading, payment methods) would show up a day later.

### D-003: Flag the first subscription payment as a recurring CIT with 3DS

* Status: Decided, 2026-11-03. Confirmed in the design, 2026-11-04. Area: payments and code.
* Context: Marie's first payment has to set up the agreement that lets Panier Box charge her every week without her being there.
* Options: (A) `payment_type: Regular` with `store_for_future_use`, which saves a card for later purchases the customer starts herself. (B) `payment_type: Recurring`, customer-initiated by default, with 3DS on. The session request has no `merchant_initiated` field, so we omit it (updated 2026-11-04). Renewals then use `Recurring` with `merchant_initiated: true` and `previous_payment_id`.
* Choice: B.
* Why: The docs use B for a payment series. Recurring stores the card automatically and returns the payment instrument ID (`source.id`). SCA is required on the first payment in a series, and that's what puts the later MITs out of scope. Sources: [Recurring payments with stored card details](https://www.checkout.com/docs/payments/accept-payments/pay-with-stored-card-details/recurring-payments-with-stored-card-details), [Pay with stored card details](https://www.checkout.com/docs/payments/accept-payments/pay-with-stored-card-details), [SCA compliance](https://www.checkout.com/docs/business-operations/ensure-regulatory-compliance/sca-compliance).
* Trade-off accepted: Every new subscriber may see a 3DS step on the first payment, which works against the pack's wish to switch 3DS off for conversion. The docs' example also sets `challenge_requested_mandate`, which asks the issuer for a challenge. Decided 2026-11-04: keep it on the recurring CIT (D-022).
* What would change my mind: Panier Box says some customers buy one-off boxes with no subscription. Those payments would be Regular, and only the subscription sign-up would use Recurring. (They do in the demo: the one-off basket is Regular, D-007.)

### D-004: Make sure each box has a valid authorisation on dispatch day

* Status: Direction agreed, 2026-11-04: don't authorise 10 days ahead. The exact timing depends on Panier Box's weekly schedule. Area: payments and code.
* Context: The pack allows up to 10 days between authorise and capture. That's longer than most final authorisations last (see "What the 10-day gap runs into").
* Options:
  * (A) Keep it as is: authorise at order, capture late. Simplest, but late captures carry the risks in the docs.
  * (B) For MITs, authorise closer to dispatch, within 5 days of capture. Marie isn't present for MITs, so Panier Box controls the timing.
  * (C) Estimated authorisation. Dropped for renewals 2026-11-04: it's meant for long reservations where the amount gets adjusted or extended. Kept as a later-phase option for pre-orders only.
  * (D) Re-authorise before expiry: void, then authorise again as an MIT near dispatch, linked to the original. Works for CIT pre-orders too, because the card is stored. Not checked which payment type Checkout.com expects for this.
  * (E) Capture at order and refund on cancellation. No expiry problem, but Marie pays days before the box ships and every cancellation becomes a refund. Scheme rules on charging before goods ship not checked.
* Late capture still works (checked 2026-11-04). Checkout.com's auto-void runs at 7 or 30 days depending on the processing profile, so a day-10 capture is normally accepted. The cost is fees and weaker chargeback protection, not a lost payment.
* What late capture costs:
  * Mastercard Final Authorization Processing Integrity Fee: fires when a final auth isn't cleared within 7 days.
  * Visa Misuse of Authorization fee: approved auths not cleared or reversed in the allowed window.
  * Mastercard chargeback protection (reason 4808) needs clearing within 30 days, so a day-10 capture keeps it.
  * Customer side: a charge long after the hold dropped can look unfamiliar, which means more "don't recognise" disputes.
* Direction: B for renewals and scheduled boxes. Pre-orders, phase 1 (go-live in 4 weeks): keep final authorisation and capture late, and accept the integrity fee. Later phase: compare that fee with pre-auth or estimated auth costs, using real pre-order volume. D and E are not in phase 1. Store `expires_on` from each authorisation response so the order service knows every deadline. Check in the sandbox whether `expires_on` reflects the 5-day Visa MIT window.
* Sandbox finding (step 4.5, 2026-11-05): `expires_on` minus `requested_on` is 10 days for Visa, both the first payment and the renewal (`cit_visa_series`, `mit_voided`), and 7 days for Mastercard and CB (`cit_mc`, `cit_cb_series`, `mit_cb_w2`). So the sandbox does not show a shorter 5-day Visa window for MITs. Treat these as sandbox values, not the production scheme rules. The order service should read each payment's own `expires_on`, never assume a window per scheme.
* Trade-off: with B, Panier Box finds out later that a renewal failed, so there's less time to retry before dispatch.
* What would change my mind: Panier Box needs the early authorisation for a business reason (for example buying ingredients), or scheduled and pre-ordered boxes are a tiny share of volume.
* Sources: [Authorize a payment](https://www.checkout.com/docs/payments/manage-payments/authorize-a-payment), [Adjust an estimated authorization](https://www.checkout.com/docs/payments/manage-payments/authorize-a-payment/adjust-an-estimated-authorization), [Get payment actions](https://www.checkout.com/docs/payments/manage-payments/get-payment-actions).

### D-005: Demo scope

* Status: Decided, 2026-11-04. Area: code.
* Choice: two pages.
  * Page 1, checkout: a made-up Panier Box checkout page with their logo and look. A step bar shows the customer is on the last step (pay). Delivery details are faked. Flow is embedded here for the first CIT (Recurring, merchant_initiated false, 3DS).
  * Success page, Panier Box look: order ID, amount, payment ID, a few key fields. Failure goes back to checkout with a message and a retry.
  * Ops section at the bottom of the success page, same payment and amounts. Buttons:
    1. Dispatch: capture the first payment.
    2. Run next week's renewal: MIT, authorise only, like a real renewal.
    3. Reverse, per payment. On the uncaptured MIT it voids. On the captured first payment it refunds. That shows both outcomes without a second checkout.
  * After each action the page shows the payment status and actions in a clean UI (D-008 point 1).
* Payment methods: cards (CB is a scheme inside cards) and Google Pay are must. MB WAY (Portugal) on the one-off basket is nice to have. It doesn't support recurring. Alma was dropped (D-028). More methods can be switched on in Flow without new code.
* Apple Pay: out of scope, decided 2026-11-04. Flow web needs a public, verified domain for Apple Pay, plus an Apple sandbox tester account, which needs an Apple developer account. Google Pay needs no setup in sandbox.
* Redirect vs callback: cards without a redirect fire onPaymentCompleted on the checkout page, and our JS then sends the customer to the success page. Methods and 3DS flows that redirect land on success_url with cko-payment-id. Both end on the same success page, which reads cko-payment-id and asks the server for the payment.
* Webhooks (bonus in the pack): built, with simulated delivery (D-042). Captures, voids, refunds and MB WAY are asynchronous, and the Flow docs say to wait for the webhook before fulfilment. Receiver design in D-040.
* Two baskets on the checkout page (added later as a nice to have, D-007). One is a subscription first box plus add-ons, cards and wallets only. The other is a seasonal one-off box, where MB WAY also shows. Flow sessions take enabled_payment_methods and disabled_payment_methods, so this is one session field.
* Rules: secret key stays on the server. No raw card data anywhere, so SAQ A holds.
* Why: the MIT is one server call. Renewals are Panier Box's main revenue, so they're worth showing working.
* Out of the build: retries, Account Updater, network tokens, 3RI, migration, estimated auth.
* Depends on: a Flow payment returning a reusable card (D-044).

### D-006: Stack

* Status: Decided, 2026-11-04. Area: code.
* Choice: plain JavaScript front end, Go back end, web only. No iOS or Android build. Checkout.com Go SDK v3 for API calls (D-011).
* Why: Go is my strongest back-end language. I'm a beginner in React, so plain JS keeps the code mine and easy to explain. The pack says a web demo is enough.

### D-007: Scope tiers for the build (updated 2026-11-04)

* Must have:
  * Branded checkout page with Flow. Cards (incl. CB) and Google Pay. Google Pay first payments can be used for renewals: a successful wallet payment creates a long-term src_ instrument.
  * Payment session: Recurring and customer-initiated by default (no merchant_initiated on the session), 3DS on, authorise only, success and failure URLs, reference = order ID.
  * Short recurring terms on the checkout page, for example "EUR 30 every week until you cancel". Cheap, and it's dispute evidence (UC-15).
  * Success and failure pages.
  * Payment record stored on the server (D-008 point 2).
  * Ops section: capture, MIT, reverse, with idempotency keys. MIT request: source type id with the src_ ID, amount, currency, payment_type Recurring, merchant_initiated true, previous_payment_id, plus our processing_channel_id, reference, capture false and processing.preferred_scheme. Checkout resolves the Mastercard TLID from previous_payment_id.
  * Status card per payment: ID, CIT or MIT, amount, status, balances (authorised, captured, voided, refunded). Refreshed by a GET after each action.
  * In-memory store plus a JSON file.
  * README, keys in .env, nothing secret committed.
* Nice to have:
  * Webhook handler: verify signature, store, return 200, process after. Feeds the payment record.
  * Live log: "API says" next to "webhooks received", pushed to the browser as events arrive (server-sent events from Go). Fallback: refresh button.
  * One-off basket (seasonal box plus add-ons) with a country selector. FR shows Alma (removed 2026-11-05, D-028), PT shows MB WAY. Both need a new payment session. MB WAY requires billing country PT, so a French basket never shows it. Neither supports recurring, so neither shows on the subscription basket.
* Optional: polish on the status view and live log.
* Out: Apple Pay, mobile apps, retries, Account Updater, migration.

### D-008: Design decisions (2026-11-04)

1. Status view: must = status card per payment, refreshed after each action. Nice = live API and webhook log next to it (D-007).
2. Payment record (updated 2026-11-04 to match PRD.md): the browser only passes cko-payment-id to our server. The server's own GET payment call is the only thing that sets status and fills the record (source ID, payment ID, scheme, amounts). A webhook never writes the record. It only triggers that GET. Stored: order ID, CIT payment ID, latest MIT payment ID, source ID, preferred scheme, amounts, status. Writes are idempotent, so a duplicate webhook or a second GET doesn't double up. The GET runs after each action and on the success page, so the MIT button never depends on a webhook arriving.
3. Agreed: session fields as in D-007. More may turn up during the build.
4. Agreed: idempotency keys on capture, MIT and reverse. Demo moment: click twice, one action.
5. Agreed: in memory plus a JSON file.
6. Agreed: run locally. The Go server serves the front end too, so browser to server is same origin and needs nothing extra. Tunnel part replaced by D-042 (2026-11-06): no tunnel, webhooks simulated. Original: one tunnel with a free fixed domain gives a public HTTPS URL. Our Go route is /webhooks/checkout, so the URL in the sandbox webhook settings is https://<fixed-domain>/webhooks/checkout. Fallback: a free host if the tunnel gets messy.
7. The secret key needs scopes beyond payment-sessions for GET payment, capture, reverse and the MIT. Checked in the sandbox: every call in the demo works with the keys we use (steps 2.5 to 4.5).

### D-009: Edges ironed out (2026-11-04)

* Sessions: a new payment session for each basket switch and each country switch. Flow's update() only changes the amount on the Google Pay and Apple Pay sheet. payment_type, billing country and methods are fixed at creation. Sessions last 24 hours.
* Payment record (updated 2026-11-04 to match PRD.md): the GET sets status and fills the record. A webhook only triggers that GET. The browser only passes cko-payment-id. GET runs server to Checkout.com, so it works without webhooks.
* Status card, inspired by the Dashboard payment page but compact. Shown: amount and status badge, CIT or MIT, payment ID and reference, scheme badge with last 4 and card type, Google Pay if used, balances, Reverse button. In a Details fold: 3DS result, response code, authorisation expiry. No timeline. Agreed 2026-11-04.
* Webhooks: HMAC-SHA256 of the raw body with the signature key, hex in Cko-Signature. Verify before parsing, about 10 lines of Go, so it's in. Reply within 10 seconds. Delivery is at least once, up to 8 retries, and events can arrive out of order (for example payment_captured before payment_approved). So a webhook triggers a GET, and the GET sets the status. Agreed 2026-11-04.
* Event store: a set of seen event IDs plus the last 50 events, in memory only. Covers dedupe and the live log in a few lines. Not persisted: a restart forgets seen IDs, which is fine for a demo. In production Panier Box would persist it. Agreed 2026-11-04.
* Webhook events: payment_approved, payment_declined, payment_pending, payment_captured, payment_capture_declined, payment_voided, payment_void_declined, payment_refunded, payment_refund_declined, payment_expired.
* Flow script loads from checkout-web-components.checkout.com only. No bundling or self-hosting (PCI).
* Secrets: .env (gitignored) loaded with godotenv, the usual Go equivalent of dotenv (the SDK v3 is the other outside package, D-011). .env.example committed with empty values. Claude Code: a deny rule Read(./.env) in .claude/settings.json, plus a CLAUDE.md rule to never print or log keys. Deny rules don't cover grep -r or scripts that open files, so keys are never pasted into the chat either. I add the real keys to .env myself.
* Checkout page realism: step bar (Box, Delivery, Payment), order summary with box, add-ons and total. Pay button with the amount. Recurring terms next to it, in the tone French meal kits use: no commitment, pause or cancel any time. Screenshots used for layout ideas stay out of the repo (inspiration/, gitignored).
* Language and amounts, agreed 2026-11-04: English with French details. Subscription first box EUR 50 (box EUR 30 plus EUR 20 add-ons), renewals EUR 30, one-off seasonal box EUR 45.

### D-010: Build architecture (2026-11-04)

* Status: Drafted, 2026-11-04. Full spec in PRD.md. Area: code.
* Shape: one Go package, split by job (config, baskets, ids, store, cko, requests, mapping, handlers, webhooks, events). Static front end served by Go. Outside packages: Checkout.com Go SDK v3 and godotenv. Updated 2026-11-04.
* Server owns amounts and baskets. The browser only picks basket and country and passes back cko-payment-id.
* Idempotency keys come from stable IDs, never random: capture-{payment_id}, renewal-{order_id}-W{n}, reverse-{payment_id}. A double click sends the same key, so Checkout.com runs the action once.
* Captures and reversals are asynchronous (202). The UI polls the GET for up to 10 seconds, so it works without webhooks.
* MIT previous_payment_id always points at the first CIT, every week.
* Reverse: use the Reverse endpoint (void or refund, Checkout.com decides). Fallback to explicit void and refund if it isn't on the account.
* 3DS on the first CIT: superseded 2026-11-04 by D-022. The recurring CIT sends `challenge_requested_mandate`.
* Sandbox checks added to slice 2: capture stays false on the session (a dropped `false` would mean auto-capture), source.id returned, CB brand choice, and whether MB WAY needs capture true. Results in D-044.
* Claude Code guardrails: deny Read and Edit on .env in .claude/settings.json, ask before git commit or push. CLAUDE.md rule: never print or log keys, by any route.
* Sandbox setup: I enable payment methods myself (D-001). Updated 2026-11-04.

### D-011: Use the Checkout.com Go SDK v3

* Status: Decided, 2026-11-04. Area: code.
* Context: Go SDK v3 (github.com/checkout/checkout-sdk-go/v3) covers everything we call: payment sessions, get payment details, capture, void, refund, reverse and payments, each taking an idempotency key.
* Options: (A) Hand-rolled net/http client. (B) Go SDK v3.
* Choice: B.
* Why: typed requests, less code to write and explain, and it's what I'd recommend Panier Box use. In v3 the session's Capture is a *bool, so false is sent. Version 3.8.0 fixed that same bug on payment setups, where a plain bool dropped false and the API captured by default. The SDK supports a custom environment, so tests can point it at a fake server.
* Trade-off accepted:
  * Less control over the exact JSON. A dropped field could fail silently. Mitigation: contract tests check the real body for the fields that matter (capture false, payment_type, 3ds, merchant_initiated, previous_payment_id).
  * SDK structs have lagged behind the API reference before. Mitigation: check every struct we use against the API reference.
  * v3 needs the merchant-specific subdomain (CKO_SUBDOMAIN).
  * Telemetry is on by default (request latency only). Fine for a demo.
  * Check the default logger doesn't print headers before the first sandbox call.
  * Fast release pace: pin one version, don't upgrade mid-build.
* What would change my mind: the SDK sends a field the API rejects or misreads. Updated 2026-11-04: for session items (the SDK always sends `"price": 0`, D-016) the fallback is to drop `items` from the request, not to hand-roll the call. Items are optional and not core to the demo.

### D-012: Build in small tested steps

* Status: Decided, 2026-11-04. Area: code and process.
* Choice: five phases, small steps (PRD.md section 12). Each step: plan, code plus tests, run tests, manual check, my review, my commit. Not full TDD, but no step is done without passing tests.
* Test layers: unit (pure Go), handler (httptest with a fake PaymentsAPI), contract (SDK pointed at a fake Checkout.com), sandbox (build tag, I run them, real keys), JS helpers (node --test, no npm), manual browser checks.
* Why: small steps keep bugs small and easy to find, and each one is easy to review. I'm newer to unit testing, so each test has a one-line comment on what it proves.
* Trade-off: slower than one big build. Worth it for a short build where a late bug is expensive.

### D-013: Only public sources in the repo

* Status: Decided, 2026-11-04. Trimmed 2026-11-06. Area: process.
* Context: the repo is private and shared with the panel only.
* Choice: links in PRD.md and this log point to public docs and the API reference only. Working notes stay in a gitignored folder.

### D-014: Final scan, decisions vs architecture (2026-11-04)

| Decision | Covered in PRD.md | Notes |
|---|---|---|
| D-001 existing sandbox | README | Keys and IDs shared privately |
| D-002 no smoke test | Step 2.5 (real session) and 3.4 | |
| D-003 recurring CIT with 3DS | F1, step 2.3 contract test | `challenge_requested_mandate` on the recurring CIT (D-022) |
| D-004 authorisation timing | F6 authorise only. `expires_on` stored and shown | Visa 5-day MIT check in step 4.5 |
| D-005 pages and ops panel | Sections 8, 9, 10 | |
| D-006, D-011 stack and SDK | Section 3 | |
| D-007 scope tiers | Section 2 | |
| D-008 record, store, local run, scopes | Sections 4, 5, 6 | |
| D-009 sessions, status card, webhooks, secrets | F1, F8, F9, section 10, CLAUDE.md | |
| D-010 idempotency, polling, reverse | F5 to F7, section 11 | |
| D-044 webhook reply time, preferred_scheme, MB WAY | F6, F8 step 4, F9 | |
| UC-9 void or refund | F7 | |
| D-044 sandbox checks | Steps 3.4, 4.5 | |
| Gaps found and fixed | Data model (section 6), one-off orders have no renewal button, 3DS fail and MB WAY expiry (F10), store mutex and atomic writes, SDK logger check | |

### D-015: Flow styling and start again (2026-11-04)

* Status: Decided, 2026-11-04. Area: code.
* Flow styling: no live customisation demo, but Flow must match the page. One set of design tokens in CSS, mirrored in theme.js and passed to Flow as appearance. Checked by eye in steps 2.6 and 5.7.
* Start again: "Place another order" on success, failure and ops views. It goes to /, which always makes a new order and session. Delete data/orders.json to reset.
* Docs: added the Flow library reference, Appearance options and the Flow demo next-steps page to PRD.md section 14.

### D-016: SDK sends `"price": 0` on session items

* Status: Closed 2026-11-04: items dropped from the session. Area: code.
* Swagger check (step 2.2): session `items` define `name`, `quantity` and `unit_price` (required) plus optional `reference`, `total_amount`, `tax_amount`, `discount_amount`, `url` and others. There is no `price` field, so the SDK would send a field the API doesn't define.
* Outcome: no `items` on the session. They're optional and Flow doesn't show them. The order keeps them, the server checks they add up to the amount, and the checkout page shows them as the order summary (step 2.6).
* Context: In SDK v3.8.0, `payments.Product` has `Price int json:"price"` with no `omitempty`, so every session item goes out with `"price": 0` even though we never set it.
* Choice: check the payment session `items` schema in swagger.yaml at step 2.2. If the API rejects or misreads `price: 0`, stop and decide. The fallback is to drop `items` from the session. They're optional.
* Rule for all contract tests: check the full JSON body the SDK sends, not only the fields we care about. Flag every zero value we didn't set.
* Why: a zero value we didn't set fails silently, the same class of bug as the old `capture` one (D-011).
* Trade-off: without items the session has no basket detail. That's fine for the demo, since the page shows the basket from server data.

### D-017: Pin SDK v3.8.0 and godotenv v1.5.1

* Status: Decided, 2026-11-04. Area: code.
* Context: SDK v3.9.0 was released on 2026-11-04 at 12:10 UTC, the day the build started. v3.8.0 came out on 2026-10-22.
* Choice: `github.com/checkout/checkout-sdk-go/v3 v3.8.0` and `github.com/joho/godotenv v1.5.1`, the latest stable godotenv (later tags are pre-releases).
* Why: v3.8.0 has the payment setups `capture` fix (D-011). A diff with comments stripped shows the structs we use (sessions, nas payments, `ThreeDsRequestFlowHostedLinks`) are the same in v3.9.0. Its changes are a `customer_retry` type and a few `enabled` bools losing `omitempty`, none in fields we send. A release that's a few hours old isn't worth the risk.
* Trade-off: we miss the v3.9.0 fixes. They don't touch our fields.
* What would change my mind: we need a field or fix that only v3.9.0 has.

### D-018: go directive is `go 1.27`

* Status: Decided, 2026-11-04. Area: code.
* Context: the SDK needs only Go 1.14. But the `go` line in go.mod also controls behaviour: `net/http` method routing ("POST /x", `{id}`) only turns on at 1.22 or later. Below that, the patterns are treated as literal paths, with no error.
* Options: (A) `go 1.22`, the lowest version that gives method routing. (B) `go 1.14`, matching the SDK, with routing by hand. (C) `go 1.27`, the local toolchain.
* Choice: C.
* Why: it's the version we build and test with, and it has method routing.
* Trade-off: anyone running the repo needs Go 1.27 or later. README and PRD.md section 3 say so.

### D-019: Module path

* Status: Decided, 2026-11-04. Area: code.
* Choice: `github.com/alfredo-bertorelli-cko/panier-box-flow-demo`.
* Why: it matches the GitHub repo, so `go install` and imports work as is.

### D-020: Config never shows a value, even by accident

* Status: Decided, 2026-11-04. Area: code (step 0.2).
* Finding: godotenv v1.5.1 parse errors quote raw `.env` content. `unterminated quoted value` prints the value, and `unexpected character ... near` prints the rest of the file, keys included. One typo in `.env` plus `log.Fatal(err)` would print the secret key.
* Choice:
  * `loadDotEnv` drops godotenv's parse error and returns a fixed message. A missing `.env` is fine.
  * `Config` has `String`, `GoString` and `LogValue` methods that redact keys, subdomain and channel. Only the base URL and port print.
  * Format checks at start: `sk_sbox_` and `pk_sbox_` prefixes (refuses a production key), `pc_` prefix, http or https base URL with a host, port from 1 to 65535. Errors name the variable, never the value.
* Why: CLAUDE.md says no key in any log or error. These close the routes a careless `log.Printf("%+v", cfg)` or a bad `.env` line would open.
* Trade-off: a parse error doesn't say which line is wrong. You check quotes and names by eye.
* What would change my mind: Checkout.com changes the sandbox key prefixes.

### D-021: SDK v3.8.0 client setup, what it logs and sends

* Status: Decided, 2026-11-04. Area: code (step 2.1).
* Findings from the SDK source:
  * The default logger writes method and path only (`GET: /payments/pay_...`) to stderr. No headers, no body. We replace it with `safeLogger` anyway (prefix `cko-sdk: `, masks anything shaped like `sk_`, `pk_` or `Bearer`).
  * `CheckoutAPIError.Error()` is the HTTP status only. `error_codes` are in `.Data.ErrorCodes`.
  * Telemetry (on by default, D-011) adds a `Cko-Sdk-Telemetry` header from the second request: request IDs and the previous duration. No key. A test checks it.
  * The subdomain is required. The only opt-out, `WithLegacyDomain()`, is deprecated. The SDK prepends the subdomain to the host of any environment and checks it against `^(?:pl-)?[a-z0-9]+$`. config.go now applies the same rule at start.
  * `Build()` checks the secret key against `^sk_(sbox_)?[a-z2-7]{26}[a-z2-7*#$=]$`, so test keys must match it. The error is `Invalid secret key`, without the value.
  * The package at `payments/sessions` is named `payment_sessions`, and `nas.Api` has both `PaymentSessions` (Flow, what we use) and `Sessions` (standalone 3DS sessions, not ours).
  * The SDK's own default HTTP timeout is 10 seconds. We set 30.
* Choices:
  * Only the secret key goes to the SDK. The public key is for the browser only.
  * `newPaymentsAPI` refuses any environment where `IsSandbox()` is false.
  * Capture, reverse and MIT refuse an empty idempotency key before any request is sent.
  * Contract tests keep the real subdomain path: the environment base is `http://api.test`, and a custom `DialContext` sends every connection to the `httptest` server. Tests then assert the Host is `{subdomain}.api.test`. We didn't use the deprecated legacy domain in tests.
  * Contract tests list every header sent and flag any they don't expect (D-016). A GET sends exactly Accept, Accept-Encoding, Authorization, Content-Type and User-Agent, and no body.
* Trade-off: the header allow-list breaks if a later SDK adds a header. That's the point: we look before accepting it.

### D-022: Session request fields, and the mandate challenge on the recurring CIT

* Status: Decided, 2026-11-04. Area: payments and code (step 2.2).
* Choice: the recurring CIT (subscription) sends `3ds: {enabled: true, challenge_indicator: challenge_requested_mandate}`. The one-off session sends `3ds: {enabled: true}` only (API default `no_preference`).
* Why: the first payment sets up the mandate for every later MIT. If it wasn't strongly authenticated, issuers can decline the MITs with 20154 (3DS required), a soft decline. Asking for a challenge on that one payment protects every renewal after it. Supersedes the "no forced challenge" line in D-010.
* Trade-off: every new subscriber is more likely to see a 3DS challenge on sign-up, against the pack's conversion concern. One challenge at sign-up costs less than failing renewals.
* Swagger vs SDK: swagger types `3ds.enabled` as a string enum (`'true'`), used as the discriminator. The SDK sends a JSON boolean, and so does PRD F1. Confirmed in the sandbox, 2026-11-04 (step 2.5): the subscription session as built (boolean `enabled`, `attempt_n3d: false`, `challenge_requested_mandate`, `capture: false`) returned 201 and `ps_3LeuNCSNXoum9sCwUf4uc27WOVP`. So it's an OpenAPI quirk, and the SDK is right. Keys, subdomain, processing channel and the `payment-sessions` scope all work. Whether `capture` stays false on the payment is checked in step 3.4.
* Other checks against swagger: `payment_type` Regular or Recurring, the methods `card`, `googlepay`, `alma` and `mbway`, `locale: en-GB`, `display_name`, and required `billing.address.country`, `success_url` and `failure_url` all exist. `capture` defaults to true, so false is always sent. `attempt_n3d` (always sent as false by the SDK) is a defined boolean.
* Contract test findings (step 2.3): the only zero value the SDK adds to the session body is `"3ds.attempt_n3d": false`. Headers are Accept, Accept-Encoding, Authorization, Content-Length, Content-Type and User-Agent. No idempotency key on sessions, which is fine since each page load is a new order. The SDK keeps the raw response bytes in `HttpMetadata.ResponseBody`, while its typed struct drops unknown fields. So the 2.4 handler returns the raw body, not the struct, keeping "unchanged" (F1 step 4) true even if the API adds a field (D-023).
* Billing: FR is 12 rue Oberkampf, 75011 Paris. PT (one-off, for MB WAY) is Rua Augusta 100, 1100-053 Lisboa (D-007).
* What would change my mind: sandbox data shows the issuer challenge costs more sign-ups than the MIT declines it saves.

### D-023: Session endpoint returns the order summary and the session

* Status: Decided, 2026-11-04. Area: code (step 2.4).
* Context: the checkout page needs the Flow session and the order summary (ID, items, total, weekly price for the recurring terms). The session response doesn't carry our order.
* Options: (A) one response `{order, payment_session}`. (B) the raw session only, plus `GET /api/orders/{id}` for the summary.
* Choice: A.
* Why: one call, one route. The session is still unchanged inside `payment_session`, and the server owns every amount the page shows, `renewal_amount` included.
* Details:
  * `payment_session` is the raw response body as a `json.RawMessage`. It's the same JSON value, but not the same bytes: encoding/json compacts it (whitespace, `&` as `\u0026`). Flow parses JSON, so that's fine. A test checks the value, including a field the SDK struct doesn't know.
  * The order is stored only after the session succeeds, so a failed session leaves no orphan order. An order ID can be used up by a failure, which is fine.
  * Unknown body fields are ignored, not rejected, so a browser-sent `amount` has no effect. The body is limited to 1 KB.
  * Errors: 400 for bad input, 502 with `cko_status` and `error_codes` for a Checkout.com error, 500 otherwise. Always a plain `error` message.
  * `GET /api/payment-sessions` is a 404, not a 405. Go's ServeMux sends it to `GET /` (the static server), and 405 only happens when no pattern matches the method at all.
* Trade-off: the response isn't Checkout.com's body as is. The JS passes `payment_session` to Flow, not the whole response.

### D-024: Checkout page data and copy (step 2.6a)

* Status: Decided, 2026-11-04. Area: code and UI.
* UI spec: `inspiration/mockup/` (README, mockup.css, checkout.html). The `:root` tokens are copied into `web/styles.css` unchanged.
* Choices, agreed 2026-11-04:
  * Items: the box is "Weekly box". Add-ons carry `add_on: true` and show an "Add-on" tag. The "2 people, 3 recipes" detail is dropped as not relevant to the demo.
  * No delivery date anywhere (checkout summary and success page). Not relevant to the demo. PRD F4 and section 10 updated.
  * The order summary from `POST /api/payment-sessions` carries a `customer` block (name, email, address) from the same demo data as the session billing. So the Delivery panel shows Lisbon for the PT one-off, and the page and Checkout.com never disagree.
  * One-off copy: the same reserve banner with its amount. No recurring terms block, no "Then ... a week" line. The legal line ends after "privacy policy".
  * Demo strip wired now, not in 5.6: any basket or country change is a new order and session, and Flow is mounted again. Country is enabled for the one-off box only (later opened to subscriptions, D-043). The manual Alma and MB WAY checks stay in 5.6.
  * The "Edit" link on the Delivery panel is removed, so it doesn't raise questions.
* Flow docs checked (Customize, CheckoutWebComponents, FlowComponent, Get started, 2026-11-04): init options `publicKey`, `environment`, `locale`, `paymentSession`, `appearance`, `componentOptions`, callbacks `onPaymentCompleted(_self, paymentResponse)` and `onError(component, error)`, `create("flow").mount(el)` and `unmount()`. All 12 colour keys in the mockup exist. Fonts are set per element (`button`, `footnote`, `input`, `label`, `subheading`). Custom web fonts don't reach the card fields, so the system stack is right. The docs don't say which `borderRadius` index is which: checked by eye in 2.6b.
* Safety: text is built with `textContent` only (no `innerHTML` with data). A Go test reads the real `web/index.html` and fails if any script comes from anywhere other than the Flow URL or our own origin.
* The "terms" and "privacy policy" links in the legal line stay, as in the spec, but are inert (click does nothing). Agreed 2026-11-04.
* Manual check 2026-11-04: page matches the spec, Flow mounts with the card form, basket and country switches mount a new Flow, POST body is basket and country only. Open at the time: Google Pay did not show. Fixed 2026-11-05: a sandbox configuration point, after which Flow showed Card (with the CB badge) and Google Pay.
* Small non-visual CSS addition: `[hidden] { display: none !important; }`, because `.terms { display: flex }` would otherwise beat the browser's own rule for `hidden`.

### D-025: Flow appearance mirrors the page tokens (step 2.6b)

* Status: Decided, 2026-11-05. Area: UI.
* Choice: `web/theme.js` writes out every value, mapped to the `:root` tokens as the spec's "Flow:" notes suggest. `web/theme.test.js` parses `styles.css` and fails if any value and its token disagree, if a value is neither mirrored nor explained, or if a key isn't a documented Flow key (Flow ignores unknown keys silently).
* Values with no token, taken from spec rules: button `17px` (`.btn`), `fontWeight 400` for input and footnote (`body`), `500` for subheading (method rows), `letterSpacing 0`.
* Can't mirror: the spec's 48 px input and 52 px button heights. Flow has no height keys, so Flow's own heights show.
* Manual check, 2026-11-05: colours, fonts and radius approved. `borderRadius[0]` is the fields and `[1]` the pay button, as the spec assumed. Cream `colorBackground` stays.
* Focus ring, 2026-11-05: the focus ring looked too thick. Flow has no documented focus width key (`colorOutline` is colour only, `borderWidth` is the resting border). Trying a softer ring colour instead: `colorOutline: #4F8461` (about 80 % of the brand green) in place of `--c-brand`. Contrast on the white field is 4.37:1, above the WCAG 1.4.11 minimum of 3:1 for focus indicators, and a test enforces it. Not a `:root` token, so it's an explained exception in theme.js. If it doesn't work by eye, go back to `--c-brand`.
* Finding: a unitless `lineHeight` (the `--lh` token, 1.45) made Flow's card inputs about three times too tall, although the docs list the type as "number or string". All `lineHeight` values removed, so Flow's own line heights apply (as in 2.6a). A test keeps them out. If line height is ever needed, use pixel strings like the docs examples (`'24px'`).

### D-026: Mapping GET payment to the status card (step 3.1)

* Status: Decided, 2026-11-05. Area: payments and code.
* Scheme: from `processing.scheme`, the network the payment was authorised on. On a co-badged card it's the scheme Marie chose, so `RecordCIT` stores it and renewals send it as `processing.preferred_scheme` (D-044). `source.scheme` and `source.local_schemes` only describe the card. Values are `cartes_bancaires`, `visa` and `mastercard`, the same spelling as `preferred_scheme` (2026-11-05). Swagger `PaymentDetails.processing` has it, and SDK v3.8.0 maps it as `GetPaymentResponse.ProcessingData.Scheme`. Display fallback (2026-11-05): a separate `card_schemes` field holds the card's own brands (`source.scheme` plus `source.local_schemes`, normalised, de-duplicated) for the badges. The card shows the processed scheme when there is one, else the card brands. `card_schemes` never drives renewals. Why `processing.scheme` can be empty: on a redirect flow (3DS) the first response is a 202 with a redirect link and no processing yet. GET and webhooks have `processing.scheme` once the payment is authorised. After a failed 3DS it's likely empty (to confirm with a sandbox payment). Fixture `ref_cit_3ds_failed` assumes that shape.
* Response code and summary: GET payment details has none at the top level, so they come from the `Authorization` action (newest first, so after a capture it isn't the first one). The SDK calls that action type `AuthorizationYes`. Corrected in 3.1b: the actions come from GET payment actions, not `actions[]` on the details (D-027).
* Kind (CIT or MIT) from the reference we build: `ORD-n` is CIT, `ORD-n-Wk` is MIT, anything else is unknown.
* Missing `source`, `3ds`, `processing` or `actions` map to empty fields, never a panic. An empty `source.id` is returned as empty, so 3.2 can disable renewals (D-044).
* SDK lag noted, not needed: swagger `processing.scheme_transaction_link_id` (Mastercard TLID) is missing from SDK v3.8.0, present in v3.9.0. Checkout.com resolves the TLID from `previous_payment_id` (D-007).
* Fixtures: `testdata/payments/ref_*.json` are built from the API reference `PaymentDetails` schema with fake IDs, one per state. Real ones go in `testdata/sandbox/` through `TestSandboxDumpPayment`, which refuses to save anything key-shaped. `TestMapSandboxFixtures` checks every real file automatically. Real fixtures for every state saved 2026-11-05, see D-027.

### D-027: Response code from GET payment actions, real shapes (step 3.1b)

* Status: Decided, 2026-11-05. Area: payments and code.
* Context: 12 real sandbox payments dumped (Visa, MC, CB co-badged both ways, CB/MC, Google Pay, declined, failed 3DS, captured, refunded, voided, MB WAY). None had `actions`. The API reference says `PaymentDetails.actions` is "returned when a session ID is used to get the payment details". We GET by `pay_` ID, so the response code was always empty.
* Choice: `PaymentsAPI.GetActions` calls `GET /payments/{id}/actions` (SDK `GetPaymentActionsWithContext`, which decodes the bare array). `mapPayment` takes the details plus the actions. A sync is two GETs, both reads. `processing.partner_response_code` ("51") is the issuer's code, not 20051, so it isn't used.
* Real shapes found, now in the `ref_*` fixtures:
  * `processing.scheme` is uppercase (`VISA`, `CARTES_BANCAIRES`). `normaliseScheme` already lowercases it.
  * Co-badged card: `source.scheme`, `source.local_schemes` and `source.id` are the same whichever brand Marie picks. Only `processing.scheme` changes. So `processing.scheme` drives `preferred_scheme` on the MIT and the "paid via" badge. `card_schemes` (`source.scheme` plus `local_schemes`) is the card's brands. `source.scheme_local` is deprecated in the SDK, not used.
  * `processing.preferred_scheme` echoes the request, even on a failed 3DS with no `processing.scheme`. Not proof of the network, not used.
  * Failed 3DS: no `processing.scheme`, no balances, `three_ds` N, but `source.id` is set. So 3.2 records the CIT only when `approved` is true.
  * MB WAY: `source` is only `{type: "mbway"}`, which the SDK decodes as a plain map. `Method.Type` reads `type` from it.
  * 3DS on the CB test cards came back `A` (attempted), shown as is.
* Why: response code is on the status card, and a silent empty field was the failure mode. Fixtures now match what the sandbox sends, and `TestSandboxFixtureShapes` pins each real payment.
* Real actions (re-dump, 2026-11-05):
  * Failed 3DS still has an Authorization action: 20151 "Cardholder failed 3D-Secure authentication".
  * Declined: Authorization 20051. Approved cards: one Authorization 10000, `authorization_type` Final.
  * MB WAY: a single Capture action, no Authorization. The code falls back to the newest action.
  * Refunded: Refund 10000, Refund 50003 (a failed first try from the Dashboard), Capture, Authorization. The card shows the authorisation's 10000, as `TestAuthorizationResult` expects.
* Trade-off: one more API call per sync.

### D-028: Alma removed from scope (updates D-007)

* Status: Decided, 2026-11-05. Area: scope.
* Context: Alma on the one-off FR basket. Flow never redirected, and the payment returned `request_invalid` with `payment_attempt_failed` and `payment_flow_invalid` (request_id 4ad2f094-a24f-4c77-8e7a-434256d89f57).
* Choice: remove Alma completely. One-off FR offers card and Google Pay. One-off PT keeps MB WAY. The FR or PT switch stays.
* Why:
  * Alma only supports auto capture on Checkout.com. We authorise at order and capture at dispatch, and `capture` applies to every method in a Flow session, so Alma can't auto-capture while cards stay on manual capture.
  * EUR 50 minimum. The seasonal box is EUR 45.
  * No recurring support, so it never fits the subscription.
* Trade-off: no French local method beyond CB. A BNPL needs its own capture model, so it would need its own session or checkout step.

### D-029: GET payment handler (step 3.2)

* Status: Decided, 2026-11-05. Area: code and payments.
* Flow: check the ID, GET details, GET actions, find the order by reference, upsert the status card, then record the CIT if allowed. Reply 200 `{order, payment}` with `Cache-Control: no-store`, since the page polls it.
* Record the CIT only when the payment is a CIT (reference `ORD-n`), `approved` is true, the order is a subscription, and there is a `src_` ID and a scheme. Why: a failed 3DS still returns `source.id` (D-027), a one-off never renews, and a renewal fetched first must not become the CIT, or `previous_payment_id` would point at it.
* `renewals_ready` is true only with a recorded CIT, card and scheme. The page disables the renewal button otherwise (D-044).
* Errors: a failed details or actions read is 502 with Checkout.com's status and codes, and nothing is stored (2026-11-05: a retry is cheap). Unknown reference is 404. Bad ID is 400 and never reaches the API.
* Payment ID check tightened to the API reference pattern `^pay_\w{26}$`. Why: the ID comes from the URL, and `%2F` decodes to a slash that the SDK would put into the Checkout.com path. Also guards the capture and reverse keys. Fake IDs in tests shortened to 26 characters to match.
* `source_id`: first kept on the server. Updated in 4.1a (2026-11-05): the back office band shows it, to make the point that one stored card is reused for every renewal. It's a Checkout.com token for a stored card, not card data, and only works with our secret key.

### D-030: Success page and failure banner (step 3.3)

* Status: Decided, 2026-11-05. Area: UI.
* Success page from the top half of `success.html`. Changes to the mockup (2026-11-05):
  * No delivery dates anywhere (D-024): no "First delivery" tile, no date block in the aside.
  * Plan is the box name ("Weekly box", "Christmas box, one-off"), without "2 people, 3 recipes".
  * Next payment has no date: "€30.00 a week, from your next box". One-off: "None, this is a one-off order".
  * The fourth tile is "Delivering to" (postcode and city), so the 2 by 2 grid stays full.
  * The demo strip shows this order. A change opens checkout with `?basket=&country=`, which starts a new order and session. The success page itself never changes.
* Status decides the page: Authorized, Captured, Voided and Refunded show the order. Declined, Expired and Canceled go to `/?payment=failed&cko-payment-id=`, so there is one failure UI. Pending shows a short message and a Refresh button. Polling comes with the ops panel.
* Hero wording follows the money: Authorized "reserved", Captured "paid" (MB WAY, D-044), Voided "released", Refunded "refunded".
* Failure banner from `failure.html`. The detail follows the response code: 20151 "couldn't confirm it was you", other codes "declined", none (Flow onError) a plain line. Help line for every failure: "Try again below, or use another card or payment method." After a redirect it adds the previous attempt (order, payment ID, status), and the retry uses the failed order's basket. The URL is reset to `/`, so a reload starts clean (F11).
* Shared DOM helpers moved to `web/page.js` (summary, demo strip, banner), used by both pages. Pure logic stays in `lib.js`.

### D-031: Back office band, read only (step 4.1a)

* Status: Decided, 2026-11-05. Area: UI.
* Band from the bottom half of `success.html`: title, order bar, status cards. Actions come in 4.1b and 4.3, Reverse in 4.4, the live log in 5.4. The grid keeps its right column empty for the log, so nothing moves later.
* Order bar shows `source_id` (D-029 updated): one stored card reused for every renewal. Scheme is the recorded renewal scheme, or on a one-off (nothing recorded) the scheme of its paid first payment. Fix after the manual check: one-offs showed "None" for every card. Next renewal reads "Off, no stored card" when `renewals_ready` is false, "None, one-off order" on a one-off.
* Cards newest first: highest renewal week on top, the first box last. Several attempts on one order (a decline, then a retry in the same session) show newest first.
* Scheme badge from `processing.scheme` (VISA, MC, CB), the card's first brand when nothing was processed. 3DS in words: Y authenticated, A attempted, N not authenticated. The mockup's "Y, frictionless" needs `3ds.challenged`, which we don't store. Times in Paris time.
* A renewal's Details fold shows "Linked to" the first CIT, the `previous_payment_id` every MIT sends (F6).

### D-032: Capture, the Dispatch button (step 4.1b)

* Status: Decided, 2026-11-05. Area: payments and code.
* Route `POST /api/orders/{orderId}/capture`. The browser names the order only. The server picks the payment and the amount.
* Target: the order's newest CIT that is Authorized with something left on the balances (authorised minus captured minus voided). Newest, because a decline or a voided attempt and a retry can leave several CITs on one order. Renewals are never captured from here.
* Dispatch is allowed on one-off card orders too (2026-11-05): the Christmas box ships as well. MB WAY shows "Already captured at payment." (D-044). Reasons say "first payment" on a subscription and "payment" on a one-off.
* Request: `amount` (what's left to capture) and `reference` (the order ID), checked against swagger `CaptureRequest` and SDK `nas.CaptureRequest`. `capture_type` left out, so Final. Key `capture-{payment_id}`. The contract test checks path, body and `Cko-Idempotency-Key` on the real SDK request.
* After the 202, one GET of details and actions, then store and reply `{order, payment}`. The capture is asynchronous, so the card can still say Authorized: the button turns off with "Capture sent at 10:03" until the page reloads (polling is 4.2). A second click in that window sends the same key, and swagger says an already captured payment answers 200.
* The order view carries `can_capture` and `capture_reason` from the same rule, so page and server never disagree. Nothing to capture is a 409 with that reason. A Checkout.com refusal is a 502 with status and error codes, shown in the action panel.
* `syncPayment` (the GET's fetch, store and record step) is now shared by the GET handler and capture, and will be by webhooks.
* Sandbox (4.5): confirmed, see D-036.

### D-033: Polling after an action (step 4.2)

* Status: Decided, 2026-11-05. Area: UI.
* `pollUntil` in `lib.js`: GET `/api/payments/{id}` every second, at most 10 tries, until the status is no longer the one before the action (F5). The page redraws after each read, so balances update as they land. After 10 tries: "Still processing at Checkout.com. Reload in a moment."
* A failed GET stops polling and shows the error (2026-11-05): a 502 usually repeats, and each try is two Checkout.com reads, so at most 20 per poll.
* One poll at a time. Dispatch only polls when its reply still says Authorized. Renewal (4.3) and Reverse (4.4) reuse the helper.
* Webhooks (5.x) will make polling mostly unneeded, but it stays: the demo must work without webhooks (F10). The tunnel is gone and webhooks are simulated (D-042), so this now means with the simulator off.
* Sandbox finding (2026-11-05): after Dispatch the capture handler's own GET already returned Captured, so the page never polled. The sandbox applied the capture within the few hundred ms between the 202 and our GET. Polling stays as the safety net: the API reference says card captures are asynchronous, and nothing promises that timing in production.

### D-034: Renewal MIT (step 4.3)

* Status: Decided, 2026-11-05. Area: payments and code.
* Route `POST /api/orders/{orderId}/renewals`. One MIT for the order's next week, built from stored data only: source `{type: id}` with the CIT's `src_`, EUR 30.00, reference `ORD-n-Wk`, `payment_type` Recurring, `merchant_initiated` true, `previous_payment_id` always the first CIT (also W3 and later), `capture` false, `processing.preferred_scheme` from the CIT's `processing.scheme`. Key `renewal-ORD-n-Wk`. Golden file `testdata/mit_renewal_w2.json`.
* API reference check: `merchant_initiated: true` requires `payment_type` not Regular. We send Recurring.
* SDK v3.8.0 adds two things on its own (both kept, 2026-11-05). Both are valid by the schema, recorded in the golden file. Confirmed in the sandbox (4.5, 2026-11-05): renewals with both are approved.
  * `processing` carries six zero amounts (`tax_amount`, `discount_amount`, `duty_amount`, `shipping_amount`, `shipping_tax_amount`, `original_order_amount`): no `omitempty` on them. The API reference marks them optional, `minimum: 0`.
  * `"payment_plan": {}`: a struct value, which `omitempty` never drops. The API reference makes it optional (`oneOf` PlanRecurring, PlanInstallment, all fields optional). Not built: a real `PlanRecurring` (`amount_variability`, `days_between_payments: 7`) could describe the subscription, not built.
* The week moves on only when the MIT is approved. A decline shows its card and keeps the week. Running it again sends the same key, so Checkout.com replays the decline. Real retries (another day, a new key) are not built.
* Double click: `AdvanceRenewalWeek(order, fromWeek)` only advances from the week that was renewed. Two overlapping clicks both send `renewal-ORD-n-W2` (Checkout.com charges once), and the week ends on 3, not 4. Tested with a real overlap. Sandbox (4.5): two renewal requests at once on ORD-1159 both answered 200, gave one W3 payment, and the order moved to week 4.
* Sandbox (4.5): the CB renewal ran on CB (`mit_cb_w2`: `processing.scheme` CARTES_BANCAIRES) on the same `src_` as the first payment, and renewals carry no `3ds`. `TestSandboxRenewalSeries` pins it.
* Blocked with a 409 and the mockup's reasons: "One-off orders have no renewals." and "No stored card on the first payment, renewals off." The order view carries `can_renew` and `renew_reason`.
* The success page hero keeps describing the payment in its URL. A renewal only adds a card in the back office band.

### D-035: Reverse on each status card (step 4.4a)

* Status: Decided, 2026-11-05. Area: payments and code.
* Per-card actions: each status card gets its own buttons, worked out by the server from that payment's balances. 4.4a is Reverse, 4.4b adds Capture. A selector, or acting on the newest payment only, was rejected: the second can't do the F7 story (void the renewal, refund the first box).
* Route `POST /api/payments/{id}/reverse`: `POST /payments/{id}/reversals`, body `{reference}` (the payment's own reference), key `reverse-{payment_id}`. Checkout.com voids if Authorized and refunds if captured, like our `reverseAction`: void what's authorised and not captured, refund what's captured and not refunded. MB WAY (captured at payment) gets a refund (D-044).
* SDK vs API reference: the SDK `PaymentReversalRequest` has `amount`, the API reference request doesn't (a reversal is the full payment). Never set, `omitempty`, so never sent. The contract test checks the body is only `reference`.
* Only payments on our orders (`OrderWithPayment`). Anything else is a 404, never reversed. Nothing to reverse is a 409. A double click sends the same key, and an already reversed payment answers 200.
* Reversing the first payment doesn't stop renewals (2026-11-05): Reverse is a payment action, not a subscription cancel.
* F7 fallback to the void and refund endpoints not needed: the sandbox allows reversals (4.5, 2026-11-05). `mit_voided` has a `Void` action, `mit_cb_series_refunded` a `Refund`.
* Each card in the order view carries `reverse_action` and `reverse_reason`, view only.
* The reversed card stays where it is on screen while it updates (2026-11-05). Each redraw replaces every card, so the page measures that card before and after and scrolls by the difference. Dispatch and renewal don't pin a card.

### D-036: Capture on each status card (step 4.4b)

* Status: Decided, 2026-11-05. Area: payments and code.
* Route `POST /api/payments/{id}/capture`: any payment on one of our orders, a renewal included. Same request as Dispatch: what's left to capture and the payment's own reference (ORD-1042-W2 for a renewal), key `capture-{payment_id}`. Dispatch and the card button share one function, so the first box captured from either sends one key and is captured once.
* One rule, `canCapture`: Authorized with something left on the balances. Dispatch's `captureTarget` uses it for the first box. The card shows Capture only then: no button on declined, voided, refunded, captured or MB WAY payments (2026-11-05).
* Dispatch panel kept for the story, a warehouse scan dispatching the first box.
* Sandbox (4.5, 2026-11-05): two capture requests at once, on a renewal and on Dispatch, all answered 200, and each payment has exactly one Capture action. The idempotency key holds at Checkout.com, not only our 409.
* An Authorized card shows Capture and Reverse (void). While either runs, both are off, so they can't race. The card stays in place while it updates, as for Reverse.

### D-037: Webhook signature check (step 5.1)

* Status: Decided, 2026-11-05. Area: security and code.
* Checked against "Set up your webhook receiver" and the two support articles ("Verify a webhook was sent by Checkout.com", "Webhook signature verification failures", both dated 2026-05-01): header `Cko-Signature`, HMAC-SHA256 of the raw body, keyed with the webhook's own signature key (Dashboard "Generate key"), no timestamp or prefix.
* Gap, not a disagreement: the docs don't say how the signature is encoded. Hex per PRD F8. Was to be confirmed with the first real webhook in 5.5. Replaced by D-042: no real webhooks, so hex stays unconfirmed. Our simulator signs in hex too, so it can't confirm it. If it fails with a correct key, check the encoding first, then that nothing changed the raw body.
* `verifySignature(body, header, key)`: decode the hex header, compare with `hmac.Equal` on bytes (constant time, case of hex doesn't matter). Empty key, empty or non-hex header: fail. Never logs the header, key or body.
* The handler (5.3) must verify the exact raw bytes before any JSON decoding. Re-encoding changes key order, spacing or number precision, the top cause of failures in the support article.
* The optional `Authorization` header Checkout.com can add to each webhook is not used: the signature already proves the sender.
* Test vector computed outside Go (Python `hmac`), so the test checks our code against an independent implementation.

### D-038: Live log shows actions, webhooks, and reads that change something (step 5.4)

* Status: Decided, 2026-11-05. Area: UI and demo.
* Shows Panier Box's traffic with Checkout.com, never browser-to-server calls: every action (POST session, captures, payments for the MIT, reversals) with its HTTP status, every webhook received, and one muted read line (`↳ GET payment: Captured`) only when a read changes a card's status. One line per sync, not one per GET.
* Why: the demo shows both mechanisms working in tandem. We act, Checkout.com answers 202, a read confirms the new status, and the webhook arrives later. Usually it changes nothing because the read was first, and sometimes it is what changes the status. The rule worth explaining: status always comes from a read, never from the 202 or the webhook body (D-009).
* Rejected: actions and webhooks only (cards change with no log line saying why), every GET (polling and the actions read bury the story).
* Build: a logging wrapper around `PaymentsAPI` for outbound calls (handlers unchanged, no call can be missed), `syncPayment` for status changes, the webhook handler for events. IDs, statuses and HTTP codes only. Never headers, bodies or keys.

### D-039: One event log for webhooks and the live log (step 5.2)

* Status: Decided, 2026-11-05. Area: code.
* One in-memory `eventLog` holds both what F8 asks for (webhook event IDs seen, the last 50 events) and the live log lines from D-038 (actions, webhooks, reads), so 5.4 doesn't rework a webhook-only store.
* Dedupe: `SeenWebhook` checks and records an event ID under one lock, so two copies of one event arriving together are processed once. Checkout.com retries a webhook without a 2XX, so duplicates are normal.
* Last 50 entries, newest first. An entry has IDs, statuses, HTTP codes and error codes, and no field for headers, bodies or keys.
* Trade-off: lost on restart, and the seen-ID set has no cap. Fine for a demo. Panier Box would persist the seen IDs, or a retried webhook after a restart is processed twice. Processing is a GET and an upsert, so a repeat is harmless here.

### D-040: Webhook handler (step 5.3)

* Status: Decided, 2026-11-05. Area: payments and code.
* Route `POST /webhooks/checkout`. Order: read the raw body (1 MB, else 413), verify the signature on those bytes (401, no body), decode only `id`, `type`, `data.id`, `data.reference` (signed but not an event: 400), dedupe by event ID (repeat: 200), live log line, 200, then the work in a goroutine.
* The background work has its own context with a 30-second timeout: the request's context is cancelled once we answer. It skips payments that aren't ours before any GET: the payment is stored, or `data.reference` names one of our orders (a `payment_approved` can arrive before the success page's GET). Then `syncPayment`, the same read, store and CIT record as everywhere else. Status comes from that GET, never from the webhook body (D-009).
* Live log lines for every webhook, plus "bad signature, ignored" (401) and "duplicate, ignored". Both show the security and the dedupe in the demo. Server log: event ID, type and payment ID only. A test captures the log and checks it holds no signature, key or body.
* `app.background` (a WaitGroup) counts webhook work still running, so tests wait for it. Not wired into a graceful shutdown: a demo server stopped mid-GET loses one refresh, and the next read fixes it.
* Not done: the optional `Authorization` header check (D-037), and a queue. A goroutine per webhook is fine at demo volume. Panier Box at scale would queue them (D-044).

### D-041: Live log server side (step 5.4a)

* Status: Decided, 2026-11-05. Area: code.
* Actions: a wrapper around `PaymentsAPI` (`loggedPayments`), set up in `main.go`, logs each session, capture, reversal and MIT with path, Checkout.com's HTTP status, IDs, and the error codes on a refusal ("no response" when no answer came). Handlers don't change, and none can call Checkout.com without the log seeing it. Reads pass through unlogged.
* Reads: `syncPayment` logs `GET /payments/{id}` only when the stored status changed, a first sight included. The store decides "changed" under its lock (`UpsertPaymentChanged`), so a webhook GET and a page poll landing together log once.
* Every line carries its order, from the reference (ORD-1042-W2 is on ORD-1042). For a webhook, from `data.reference`, else the stored payment.
* Stream: `GET /api/events?order=` over server-sent events. Backlog of that order's lines, then new ones, plus "bad signature" lines on every page (a forged body is never read, so it has no order, and showing it proves the check). Heartbeat every 20 seconds. A stream that falls 32 lines behind misses lines rather than blocking the server.
* `GET /api/orders/{id}` serves the stored order. After a webhook the page re-reads it: the webhook's GET already stored the new status, so no second call to Checkout.com.
* The card payment itself never appears in our log. Flow sends it from the browser to Checkout.com (SAQ A). Our first sight is the read after `onPaymentCompleted` or the redirect.
* Page (step 5.4b): the mockup's dark panel in the band's right column, newest line at the top (the mockup had oldest first), at most 50. Webhooks orange, 2xx green, refusals red, read lines muted and indented under what caused them. Badge "Live", or "Reconnecting…" with a grey dot. EventSource reconnects by itself and the server resends the backlog, so the list is cleared on each (re)open.
* A read line means a stored status changed, so the page re-reads `GET /api/orders/{id}` (debounced 300 ms) and redraws. That's how a status set by a webhook reaches the cards without a reload.
* Bug found in the 5.4b manual check: the browser ran the new `success.js` against a cached `lib.js` from before 5.4b, so `lib.eventsUrl` didn't exist. The page showed "We couldn't load your order" and the log stayed on "Connecting…". Cause: our static files had no `Cache-Control`, so the browser guessed a freshness time. Fix: `Cache-Control: no-cache` on every front-end file and page (revalidate, 304 when unchanged). A test checks it on every file.
* Trade-off: in memory, so a restart clears the log. Fine for a demo.

### D-042: Simulated webhooks replace the tunnel (step 5.5)

* Status: Decided, 2026-11-06. Area: scope and code. Replaces the tunnel parts of D-008 (point 6) and D-037 (hex check).
* Dropped: the tunnel and real sandbox webhooks. A tunnel would expose a local machine to the internet, which isn't worth it for a demo. Pulling events from the API is out too.
* Replaced by: `webhookSimulator` (`simulator.go`), a `PaymentsAPI` wrapper chained as sim, then live log, then SDK. After an action Checkout.com accepted, it waits `SIMULATED_WEBHOOK_DELAY_MS` (800), signs the body (HMAC-SHA256 hex, `CKO_WEBHOOK_SIGNATURE_KEY`) and posts it over real HTTP to our own `/webhooks/checkout`. The handler runs unchanged: signature, dedupe, live log, background GET. Status still only comes from that GET (D-009).
* Mapping: capture → `payment_captured`. MIT → `payment_approved` or `payment_declined`, none if Pending. Reverse → `payment_voided` or `payment_refunded` from Checkout.com's `action_type`, not our guess. The CIT isn't our action (Flow sends it), so its `payment_approved` or `payment_declined` goes on our first sight of it in `syncPayment`. Refused actions and reads send nothing.
* Once per action: the event ID is `evt_sim_` plus a hash of type, payment ID and `action_id`, so a replayed action (idempotency key) gets the same ID, and the simulator never posts an ID twice. A first sight happens once per stored payment, also after a restart.
* Live log tag: the box reads "webhook". The README explains the simulation. It comes from the `evt_sim_` prefix, set only after the signature passed, so it can't be forged without the key. Real IDs are `evt_` plus 26 characters, no clash.
* Key: no Dashboard webhook, so `CKO_WEBHOOK_SIGNATURE_KEY` is a random local value. Switch: `SIMULATE_WEBHOOKS=false`. Without it, GET and polling still keep the cards right.
* Trade-off: it proves our receiver (signature on raw bytes, dedupe, async GET), not Checkout.com's delivery, the hex encoding (D-037), real retries or event order. In production, point a Dashboard webhook at Panier Box's public endpoint and the same handler takes over. Only the sender changes.

### D-043: Subscriptions also in PT, without MB WAY (step 5.6)

* Status: Decided, 2026-11-06. Area: scope and code. Nice to have.
* Choice: the demo strip's country select now works for the subscription too. A PT subscription is the same as the French one (Recurring CIT, 3DS with `challenge_requested_mandate`, authorise only, card and Google Pay, renewals by MIT), with billing country PT and the Lisbon address (Rua Augusta 100).
* No MB WAY on it: MB WAY has no recurring support (D-044), and renewals run on the stored card or wallet. `paymentMethods` already adds `mbway` only for a one-off in PT, so no change there. A table test checks the PT subscription session has no `mbway`.
* Changed: `Countries` on the subscription basket and `COUNTRIES` in `lib.js`. The server still refuses any other country.
* Sandbox check (2026-11-06): subscription in FR and in PT, and the one-off, each paid and run through Dispatch, void, refund and renewal. All worked, with the Lisbon address on the PT orders and no MB WAY on the PT subscription.
* Panier Box doesn't sell in Portugal yet. This shows what a PT launch looks like: same subscription flow, same integration, only the billing country changes, and MB WAY stays for one-off boxes.

### D-044: Facts from the pack and the sandbox that the build relies on

* Status: Recorded 2026-11-06, from checks on 2026-11-04 and 2026-11-05. Area: payments and code.
* Reusable card: a Flow payment with `payment_type: Recurring` has to return a stored card (`source.id`, a `src_` ID), or no renewal can run. The docs say Flow returns the instrument ID for recurring payment types. Sandbox, 2026-11-05: every approved card and Google Pay CIT returned one. If it's missing, the page turns renewals off with a reason (D-029).
* Brand choice on co-badged cards: EU rules require it. Flow shows the choice on co-badged CB/Visa and CB/Mastercard test cards, and `processing.scheme` follows the customer's pick. Renewals send that scheme as `processing.preferred_scheme`, so each MIT runs on the network Marie chose (D-026, D-034).
* MB WAY: no recurring support, so one-off orders only, and only in PT (D-043). It captures at payment: our `capture: false` session was accepted, and the payment came back Captured (`total_captured` 4500, `available_to_void` 0, refund link only). So no `capture: true` fallback is needed. Ops buttons follow the balances, not the method, and a cancel is a refund through Reverse (D-035).
* Webhook reply time: the pack says the order service can take 30 seconds per notification. Checkout.com needs a 2XX within 10 seconds, or it retries. So the receiver checks the signature, records the event, answers 200 and does the work after (D-040). At Panier Box's volume the work would go on a queue.
* Sources: [Remember Me (instrument ID on recurring)](https://www.checkout.com/docs/payments/accept-payments/accept-a-payment-on-your-mobile-app/extend-your-flow-for-mobile-integration/enable-remember-me), [Cartes Bancaires](https://beta.checkout.com/docs/payments/payment-methods/cartes-bancaires), [MB WAY](https://www.checkout.com/docs/payments/add-payment-methods/mb-way), [Webhook acknowledgement](https://support.checkout.com/hc/en-us/articles/29686471128722-How-to-acknowledge-my-server-received-a-webhook).
