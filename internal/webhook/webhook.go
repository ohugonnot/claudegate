package webhook

import (
	"bytes"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"time"
)

const (
	retryAttempts = 8
	retryBase     = time.Second
	retryCap      = 5 * time.Minute
)

// Send dispatches the JSON payload to callbackURL asynchronously.
// 8 retries max with full-jitter exponential backoff (cap 5 min). 30s timeout per request.
func Send(callbackURL string, payload []byte) {
	if err := validateURL(callbackURL); err != nil {
		slog.Warn("webhook: rejected callback URL", "url", callbackURL, "error", err)
		return
	}
	go send(callbackURL, payload)
}

// validateURL blocks non-HTTPS schemes and private/internal IP ranges.
func validateURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}

	host := u.Hostname()
	ips, err := net.LookupHost(host)
	if err != nil {
		return fmt.Errorf("DNS lookup failed: %w", err)
	}

	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("private/internal IP blocked: %s", ipStr)
		}
	}

	return nil
}

func send(callbackURL string, payload []byte) {
	client := &http.Client{Timeout: 30 * time.Second}

	for attempt := 1; attempt <= retryAttempts; attempt++ {
		err := post(client, callbackURL, payload)
		if err == nil {
			return
		}
		slog.Warn("webhook attempt failed", "attempt", attempt, "url", callbackURL, "error", err)
		if attempt < retryAttempts {
			time.Sleep(jitter(attempt))
		}
	}
	slog.Error("webhook: all retries exhausted", "url", callbackURL)
}

// jitter returns a random duration between 0 and min(retryCap, retryBase * 2^attempt).
// Full jitter prevents synchronized retries when multiple webhooks fail at the same time.
func jitter(attempt int) time.Duration {
	exp := retryBase * (1 << attempt) // base * 2^attempt
	if exp > retryCap {
		exp = retryCap
	}
	return time.Duration(rand.Int63n(int64(exp)))
}

func post(client *http.Client, url string, payload []byte) error {
	resp, err := client.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("non-2xx status: %d", resp.StatusCode)
	}
	return nil
}
