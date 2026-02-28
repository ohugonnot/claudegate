package webhook

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"time"
)

// Send envoie le payload JSON vers callbackURL de façon asynchrone.
// 3 tentatives max avec backoff exponentiel (1s, 2s, 4s). Timeout 30s par requête.
func Send(callbackURL string, payload []byte) {
	go send(callbackURL, payload)
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
