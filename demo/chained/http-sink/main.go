// http-sink: a tiny HTTP server that logs every POST body to stdout.
// Used as the receiving endpoint in scenarios 3, 4, and 6.
package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ts := time.Now().Format("15:04:05.000")
		fmt.Printf("[%s] %s %s | body=%s\n", ts, r.Method, r.URL.Path, string(body))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	addr := ":" + port
	log.Printf("http-sink: listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
