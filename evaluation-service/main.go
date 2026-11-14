package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

var dependencyClient = &http.Client{
	Timeout:   4 * time.Second,
	Transport: otelhttp.NewTransport(http.DefaultTransport),
}

var allowedDependencyTargets = map[string]struct{}{
	"auth-service.toggle-master.svc.cluster.local:8001":      {},
	"flag-service.toggle-master.svc.cluster.local:8002":      {},
	"targeting-service.toggle-master.svc.cluster.local:8003": {},
	"analytics-service.toggle-master.svc.cluster.local:8005": {},
}

func callDependency(ctx context.Context, method, endpoint string, payload interface{}) (map[string]interface{}, error) {
	parsedEndpoint, err := url.ParseRequestURI(endpoint)
	if err != nil || !parsedEndpoint.IsAbs() || parsedEndpoint.Scheme != "http" || parsedEndpoint.User != nil || parsedEndpoint.Fragment != "" {
		return nil, fmt.Errorf("invalid dependency endpoint")
	}
	if _, allowed := allowedDependencyTargets[parsedEndpoint.Host]; !allowed {
		return nil, fmt.Errorf("dependency endpoint is not allow-listed")
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	// #nosec G704 -- the host is validated against the fixed Kubernetes service allowlist above.
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	// #nosec G704 -- request destination was checked against the allowlist above.
	response, err := dependencyClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("dependency returned HTTP %d", response.StatusCode)
	}
	result := map[string]interface{}{}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func respond(w http.ResponseWriter, status int, payload map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func main() {
	shutdownTelemetry, err := setupTelemetry(context.Background(), "evaluation-service")
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
		port = "8004"
	}
	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		respond(w, http.StatusOK, map[string]string{"service": "evaluation-service", "status": "ok"})
	})
	http.HandleFunc("/evaluate", func(w http.ResponseWriter, r *http.Request) {
		userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
		if userID == "" {
			userID = "demo-user"
		}
		flagKey := strings.TrimSpace(r.URL.Query().Get("key"))
		if flagKey == "" {
			flagKey = "demo-flag"
		}

		base := "http://%s.toggle-master.svc.cluster.local:%s"
		authorization, err := callDependency(r.Context(), http.MethodGet,
			fmt.Sprintf(base+"/authorize?user_id=%s", "auth-service", "8001", url.QueryEscape(userID)), nil)
		if err != nil {
			respond(w, http.StatusBadGateway, map[string]string{"service": "evaluation-service", "error": "authorization dependency failed"})
			return
		}
		flag, err := callDependency(r.Context(), http.MethodGet,
			fmt.Sprintf(base+"/flags/%s", "flag-service", "8002", url.PathEscape(flagKey)), nil)
		if err != nil {
			respond(w, http.StatusBadGateway, map[string]string{"service": "evaluation-service", "error": "flag dependency failed"})
			return
		}
		targeting, err := callDependency(r.Context(), http.MethodGet,
			fmt.Sprintf(base+"/target?user_id=%s", "targeting-service", "8003", url.QueryEscape(userID)), nil)
		if err != nil {
			respond(w, http.StatusBadGateway, map[string]string{"service": "evaluation-service", "error": "targeting dependency failed"})
			return
		}
		_, err = callDependency(r.Context(), http.MethodPost,
			fmt.Sprintf(base+"/events", "analytics-service", "8005"),
			map[string]string{"type": "flag-evaluation", "user_id": userID, "key": flagKey})
		if err != nil {
			respond(w, http.StatusBadGateway, map[string]string{"service": "evaluation-service", "error": "analytics dependency failed"})
			return
		}
		writeResult := map[string]interface{}{
			"service":       "evaluation-service",
			"result":        flag["enabled"],
			"user_id":       userID,
			"authorization": authorization,
			"targeting":     targeting,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(writeResult)
	})
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		respond(w, http.StatusOK, map[string]string{"service": "evaluation-service"})
	})
	log.Print("evaluation-service starting")
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
