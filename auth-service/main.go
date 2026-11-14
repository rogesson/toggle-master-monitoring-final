package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func main() {
	shutdownTelemetry, err := setupTelemetry(context.Background(), "auth-service")
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTelemetry(ctx); err != nil {
			log.Printf("telemetry shutdown failed: %v", err)
		}
	}()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8001"
	}
	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"service": "auth-service", "status": "ok"})
	})
	// /authorize provides the auth dependency used by the distributed evaluation flow.
	http.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		user := strings.TrimSpace(r.URL.Query().Get("user_id"))
		if user == "" {
			user = "anonymous"
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"service": "auth-service", "authorized": true, "user_id": user})
	})
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"service": "auth-service"})
	})
	log.Print("auth-service starting")
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           otelhttp.NewHandler(http.DefaultServeMux, "http.server"),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}
