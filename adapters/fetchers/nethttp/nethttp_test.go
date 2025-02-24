package nethttp_test

import (
	"net/http"
	"net/url"
	"testing"
)

func TestProxyConnection(t *testing.T) {
	proxyURL, err := url.Parse("https://spz9ibbams:de3t9gZJ0d6paAuQe_@gate.smartproxy.com:10001")
	if err != nil {
		t.Fatalf("Failed to parse proxy URL: %v", err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}

	req, err := http.NewRequest("GET", "https://www.google.com", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status OK; got %v", resp.Status)
	}
}
