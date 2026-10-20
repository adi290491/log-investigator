// Package loggen deterministically generates the synthetic incident dataset:
// gateway -> orders -> payments -> (DB), 13:00-14:20 UTC on 2026-10-21, with a
// payments DB pool shrink at 14:02 that cascades upstream.
package loggen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"time"

	"log-investigator/internal/logstore"
)

const Seed = 42

// Fixed timeline. Everything is UTC.
var (
	Day         = time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	WindowStart = at(13, 0, 0)
	WindowEnd   = at(14, 20, 0) // exclusive; incident still ongoing

	OrdersDeployAt    = at(13, 15, 0)
	RateLimitFrom     = at(13, 40, 0)
	RateLimitTo       = at(13, 48, 0) // exclusive: minutes 13:40-13:47
	PaymentsDeployAt  = at(14, 2, 0)
	PoolWarnFrom      = at(14, 3, 0)
	PaymentErrorsFrom = at(14, 4, 0)
	OrderErrorsFrom   = at(14, 4, 0)
	GatewayErrorsFrom = at(14, 5, 0)
)

func at(h, m, s int) time.Time {
	return Day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s)*time.Second)
}

// Incident messages. Tests assert none of these appear before their start.
const (
	MsgPoolNearCapacity = "connection pool near capacity"
	MsgPoolExhausted    = "connection pool exhausted, waited 1500ms"
	MsgPaymentTimeout   = "payment call timed out after 2000ms"
	MsgUpstreamTimeout  = "upstream timeout (orders)"
	MsgRateLimited      = "client rate limited"
)

const (
	routeCheckout  = "POST /api/checkout"
	routeGetOrder  = "GET /api/orders/{id}"
	oldPoolSize    = 50
	newPoolSize    = 5
	pPaymentFail   = 0.70
	pOrderTimeout  = 0.40 // of all orders; drawn only among failed payments
	pCheckout      = 0.50 // share of gateway traffic that is a checkout
	pGatewayOther  = 0.30 // failure rate of non-checkout requests once orders is saturated
	pNoisePerBlock = 0.6
)

// Dataset is the full generated output, each slice sorted by time.
type Dataset struct {
	Gateway  []logstore.Entry
	Orders   []logstore.Entry
	Payments []logstore.Entry
	Deploys  []logstore.Deploy
}

type gen struct {
	rng                       *rand.Rand
	gateway, orders, payments []logstore.Entry
}

// Generate builds the dataset. It is a pure function of Seed.
func Generate() Dataset {
	g := &gen{rng: rand.New(rand.NewSource(Seed))}
	for m := WindowStart; m.Before(WindowEnd); m = m.Add(time.Minute) {
		g.minute(m)
	}
	g.noise()

	ds := Dataset{
		Gateway:  sortEntries(g.gateway),
		Orders:   sortEntries(g.orders),
		Payments: sortEntries(g.payments),
		Deploys: []logstore.Deploy{
			{Time: OrdersDeployAt, Service: "orders", Version: "v3.8.1", Change: "bump logging library"},
			{Time: PaymentsDeployAt, Service: "payments", Version: "v2.14.0", Change: "reduced DB pool size 50 -> 5"},
		},
	}
	return ds
}

func sortEntries(es []logstore.Entry) []logstore.Entry {
	sort.SliceStable(es, func(i, j int) bool { return es[i].Time.Before(es[j].Time) })
	return es
}

func (g *gen) between(lo, hi int) int { return lo + g.rng.Intn(hi-lo+1) }

func (g *gen) ms(t time.Time, ms int) time.Time { return t.Add(time.Duration(ms) * time.Millisecond) }

func (g *gen) traceID() string { return fmt.Sprintf("%016x", g.rng.Uint64()) }

func (g *gen) minute(m time.Time) {
	// Payments pool stats, once per minute.
	size, inUse := oldPoolSize, g.between(5, 15)
	if !m.Before(PaymentsDeployAt) {
		size, inUse = newPoolSize, g.between(3, 4)
	}
	if !m.Before(PoolWarnFrom) {
		inUse = newPoolSize
	}
	g.payments = append(g.payments, logstore.Entry{
		Time: g.ms(m, g.rng.Intn(60000)), Service: "payments", Level: "info", Msg: "pool stats",
		PoolInUse: inUse, PoolSize: size,
	})

	// Red herring: bursty client hitting the rate limiter. Gateway-only.
	if !m.Before(RateLimitFrom) && m.Before(RateLimitTo) {
		for i, n := 0, g.between(28, 32); i < n; i++ {
			g.gateway = append(g.gateway, logstore.Entry{
				Time: g.ms(m, g.rng.Intn(60000)), Service: "gateway", Level: "warn", Msg: MsgRateLimited,
				TraceID: g.traceID(), Route: routeGetOrder, Status: 429, LatencyMS: g.between(1, 3),
			})
		}
	}

	// Traffic for this minute. Outcome counts are stratified per minute so the
	// incident rates match the brief on any seed; which requests fail is random.
	n := g.between(18, 22)
	nCheckout := int(float64(n)*pCheckout + g.rng.Float64())
	isCheckout := g.pick(n, nCheckout)

	var payFail, orderTimeout, otherFail []bool
	if !m.Before(PaymentErrorsFrom) {
		payFail = g.pick(nCheckout, round(pPaymentFail*float64(nCheckout)))
		orderTimeout = g.subset(payFail, round(pOrderTimeout*float64(nCheckout)))
	}
	if !m.Before(GatewayErrorsFrom) {
		otherFail = g.pick(n-nCheckout, round(pGatewayOther*float64(n-nCheckout)))
	}

	var ci, oi int
	for i := range n {
		start := g.ms(m, g.rng.Intn(60000))
		if isCheckout[i] {
			g.checkout(start, flagAt(payFail, ci), flagAt(orderTimeout, ci))
			ci++
		} else {
			g.getOrder(start, flagAt(otherFail, oi))
			oi++
		}
	}
}

func round(f float64) int { return int(f + 0.5) }

// flagAt reads bs[i], treating a nil slice as all false.
func flagAt(bs []bool, i int) bool { return bs != nil && bs[i] }

// pick returns n flags with exactly k set, in random positions.
func (g *gen) pick(n, k int) []bool {
	bs := make([]bool, n)
	for i := range min(k, n) {
		bs[i] = true
	}
	g.rng.Shuffle(n, func(i, j int) { bs[i], bs[j] = bs[j], bs[i] })
	return bs
}

// subset returns flags with k of the set flags in from kept, chosen at random.
func (g *gen) subset(from []bool, k int) []bool {
	var idx []int
	for i, b := range from {
		if b {
			idx = append(idx, i)
		}
	}
	g.rng.Shuffle(len(idx), func(i, j int) { idx[i], idx[j] = idx[j], idx[i] })
	out := make([]bool, len(from))
	for _, i := range idx[:min(k, len(idx))] {
		out[i] = true
	}
	return out
}

// checkout is a request that flows gateway -> orders -> payments.
func (g *gen) checkout(start time.Time, payFailed, orderTimedOut bool) {
	trace := g.traceID()
	created := g.ms(start, g.between(3, 8))
	requested := g.ms(created, g.between(5, 12))
	g.orders = append(g.orders,
		logstore.Entry{Time: created, Service: "orders", Level: "info", Msg: "order created", TraceID: trace},
		logstore.Entry{Time: requested, Service: "orders", Level: "info", Msg: "payment requested", TraceID: trace},
	)

	payLatency := g.between(12, 30)
	switch {
	case !start.Before(PaymentErrorsFrom):
		payLatency = g.between(900, 1450) // the charges that do get a connection are slow
	case !requested.Before(PoolWarnFrom):
		// Pool pinned at 5/5: latency climbs through the minute.
		into := int(requested.Sub(PoolWarnFrom) / time.Second)
		payLatency = 200 + into*15 + g.between(0, 120)
	case !requested.Before(PaymentsDeployAt):
		payLatency = g.between(25, 90)
	}

	if !requested.Before(PoolWarnFrom) && g.rng.Float64() < 0.4 {
		g.payments = append(g.payments, logstore.Entry{
			Time: g.ms(requested, g.between(1, 5)), Service: "payments", Level: "warn", Msg: MsgPoolNearCapacity,
			TraceID: trace, PoolInUse: newPoolSize, PoolSize: newPoolSize,
		})
	}

	if payFailed {
		g.payments = append(g.payments, logstore.Entry{
			Time: g.ms(requested, 1500), Service: "payments", Level: "error", Msg: MsgPoolExhausted,
			TraceID: trace, LatencyMS: 1500, PoolInUse: newPoolSize, PoolSize: newPoolSize,
		})
	} else {
		g.payments = append(g.payments, logstore.Entry{
			Time: g.ms(requested, payLatency), Service: "payments", Level: "info", Msg: "charge succeeded",
			TraceID: trace, LatencyMS: payLatency,
		})
	}

	downstream := int(requested.Sub(start)/time.Millisecond) + payLatency
	if payFailed {
		downstream = int(requested.Sub(start)/time.Millisecond) + 1500
	}
	if orderTimedOut {
		g.orders = append(g.orders, logstore.Entry{
			Time: g.ms(requested, 2000), Service: "orders", Level: "error", Msg: MsgPaymentTimeout,
			TraceID: trace, LatencyMS: 2000,
		})
		downstream = int(requested.Sub(start)/time.Millisecond) + 2000
	}

	latency := max(g.between(40, 80), downstream+g.between(8, 20))
	end := g.ms(start, latency)
	if orderTimedOut && !end.Before(GatewayErrorsFrom) {
		g.gateway = append(g.gateway, g.upstreamTimeout(start, trace, routeCheckout))
		return
	}
	// Before 14:05 a timed-out order still completes at the gateway, just slowly.
	g.gateway = append(g.gateway, logstore.Entry{
		Time: end, Service: "gateway", Level: "info", Msg: "request completed",
		TraceID: trace, Route: routeCheckout, Status: 200, LatencyMS: latency,
	})
}

// getOrder is a gateway request that reads from orders; orders does not log reads.
func (g *gen) getOrder(start time.Time, failed bool) {
	trace := g.traceID()
	if failed {
		g.gateway = append(g.gateway, g.upstreamTimeout(start, trace, routeGetOrder))
		return
	}
	latency := g.between(40, 80)
	g.gateway = append(g.gateway, logstore.Entry{
		Time: g.ms(start, latency), Service: "gateway", Level: "info", Msg: "request completed",
		TraceID: trace, Route: routeGetOrder, Status: 200, LatencyMS: latency,
	})
}

func (g *gen) upstreamTimeout(start time.Time, trace, route string) logstore.Entry {
	status := 502
	if g.rng.Intn(2) == 1 {
		status = 503
	}
	latency := g.between(2010, 2100)
	return logstore.Entry{
		Time: g.ms(start, latency), Service: "gateway", Level: "error", Msg: MsgUpstreamTimeout,
		TraceID: trace, Route: route, Status: status, LatencyMS: latency,
	}
}

// noise adds at most one unrelated error per service per 15-minute block.
func (g *gen) noise() {
	for b := WindowStart; b.Before(WindowEnd); b = b.Add(15 * time.Minute) {
		span := min(15*time.Minute, WindowEnd.Sub(b))
		for _, svc := range []string{"gateway", "orders", "payments"} {
			if g.rng.Float64() >= pNoisePerBlock {
				continue
			}
			t := g.ms(b, g.rng.Intn(int(span/time.Millisecond)))
			if svc == "gateway" && !t.Before(RateLimitFrom) && t.Before(RateLimitTo) {
				continue // keep the red herring free of errors
			}
			e := logstore.Entry{Time: t, Service: svc, Level: "error", TraceID: g.traceID()}
			switch svc {
			case "gateway":
				e.Msg, e.Route, e.Status, e.LatencyMS = "request validation failed", routeCheckout, 400, g.between(2, 6)
				g.gateway = append(g.gateway, e)
			case "orders":
				e.Msg = "failed to publish order event, will retry"
				g.orders = append(g.orders, e)
			case "payments":
				e.Msg, e.LatencyMS = "fraud check returned malformed response", g.between(30, 60)
				g.payments = append(g.payments, e)
			}
		}
	}
}

// Files returns the output file names and their exact contents.
func (ds Dataset) Files() map[string][]byte {
	return map[string][]byte{
		"gateway.jsonl":   encodeEntries(ds.Gateway),
		"orders.jsonl":    encodeEntries(ds.Orders),
		"payments.jsonl":  encodeEntries(ds.Payments),
		"deploys.jsonl":   encodeDeploys(ds.Deploys),
		"GROUND_TRUTH.md": groundTruth(ds),
	}
}

// Write generates the dataset into dir.
func Write(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, b := range Generate().Files() {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// tsLayout is RFC3339 with fixed millisecond width, so lines align.
const tsLayout = "2006-01-02T15:04:05.000Z07:00"

// A depth-0 "ts" field shadows the embedded struct's Time (same JSON name),
// so these encode with a fixed-width timestamp first and every other field as-is.
type wireEntry struct {
	TS string `json:"ts"`
	logstore.Entry
}

type wireDeploy struct {
	TS string `json:"ts"`
	logstore.Deploy
}

func encodeLines[T any](items []T) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep "50 -> 5" readable
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			panic(err) // plain structs of strings and ints cannot fail to encode
		}
	}
	return buf.Bytes()
}

func encodeEntries(es []logstore.Entry) []byte {
	w := make([]wireEntry, len(es))
	for i, e := range es {
		w[i] = wireEntry{TS: e.Time.UTC().Format(tsLayout), Entry: e}
	}
	return encodeLines(w)
}

func encodeDeploys(ds []logstore.Deploy) []byte {
	w := make([]wireDeploy, len(ds))
	for i, d := range ds {
		w[i] = wireDeploy{TS: d.Time.UTC().Format(tsLayout), Deploy: d}
	}
	return encodeLines(w)
}
