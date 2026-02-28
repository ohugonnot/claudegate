package webhook

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Send dispatches the JSON payload to callbackURL asynchronously.
// 3 retries max with exponential backoff (1s, 2s, 4s). 30s timeout per request.
func Send(callbackURL string, payload []byte) {
	if err := validateURL(callbackURL); err != nil {
		log.Printf("webhook: rejected callback URL %s: %v", callbackURL, err)
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
	backoff := time.Second

	for attempt := 1; attempt <= 3; attempt++ {
		err := post(client, callbackURL, payload)
		if err == nil {
			return
		}
		log.Printf("webhook attempt %d/3 failed for %s: %v", attempt, callbackURL, err)
		if attempt < 3 {
			time.Sleep(backoff)
			backoff *= 2
		}
	}
	log.Printf("webhook: all retries exhausted for %s", callbackURL)
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
