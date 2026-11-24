// Standalone standard-library client: no TeamX, DSH or platform account.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 3 {
		os.Exit(2)
	}
	if os.Args[1] == "read" {
		_, err := os.ReadFile(os.Args[2])
		if err != nil {
			fmt.Println("READ_DENIED")
			os.Exit(1)
		}
		fmt.Println("READ_ALLOWED")
		return
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	if cert := os.Getenv("SRT_TEST_CA"); cert != "" {
		roots, _ := x509.SystemCertPool()
		if roots == nil {
			roots = x509.NewCertPool()
		}
		pem, err := os.ReadFile(cert)
		if err != nil || !roots.AppendCertsFromPEM(pem) {
			os.Exit(2)
		}
		t.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	if os.Args[1] == "proxy" {
		proxy, err := url.Parse(os.Getenv("HTTP_PROXY"))
		if err != nil {
			os.Exit(2)
		}
		t.Proxy = http.ProxyURL(proxy)
	}
	defer t.CloseIdleConnections()
	c := &http.Client{Transport: t, Timeout: 3 * time.Second}
	r, _ := http.NewRequest("GET", os.Args[2], nil)
	r.Header.Set("Authorization", "Bearer "+os.Getenv("SRT_TEST_TOKEN"))
	response, err := c.Do(r)
	result := map[string]any{}
	if err != nil {
		result["connection_failed"] = true
	} else {
		defer response.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		result["status"], result["body"] = response.StatusCode, string(body)
	}
	_ = json.NewEncoder(os.Stdout).Encode(result)
}
