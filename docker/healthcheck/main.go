// Command healthcheck exits 0 if GET <url> answers 200, for HEALTHCHECK in images that have
// no shell or curl. The URL is the first argument, else http://127.0.0.1:8080/healthz.
package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	url := "http://127.0.0.1:8080/healthz"
	if len(os.Args) > 1 {
		url = os.Args[1]
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(url)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, url+": "+resp.Status)
		os.Exit(1)
	}
}
