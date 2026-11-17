// Package worldrpc provides the local, single-request JSON-RPC gameplay API.
package worldrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
)

// Listen deliberately accepts numeric loopback addresses (or localhost mapped
// to 127.0.0.1), without DNS resolution or wildcard/non-local binds.
func Listen(ctx context.Context, address string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("RPC listen address must be a loopback host:port")
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	n, err := strconv.Atoi(port)
	if ip == nil || !ip.IsLoopback() || err != nil || n < 0 || n > 65535 {
		return nil, errors.New("RPC listen address must be a loopback IP and port between 0 and 65535")
	}
	return (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(n)))
}

type request struct {
	Version string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      json.RawMessage `json:"id"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type response struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Handler serves configured module methods and session.logout at POST /rpc using
// single-request JSON-RPC 2.0. Requests must be JSON from local processes;
// browser-origin requests are rejected and CORS is disabled.
// The caller owns HTTP serving, timeouts, and shutdown.
func Handler(session Session) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpc" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		_, origin := r.Header["Origin"]
		_, fetch := r.Header["Sec-Fetch-Site"]
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		ip := net.ParseIP(host)
		if origin || fetch || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
			http.Error(w, "local process requests only", http.StatusForbidden)
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// A 128 KiB Lua source can occupy up to six JSON bytes per source byte.
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
				return
			}
			writeError(w, nil, -32700, "Parse error", nil)
			return
		}
		if !json.Valid(body) {
			writeError(w, nil, -32700, "Parse error", nil)
			return
		}
		var req request
		if err := json.Unmarshal(body, &req); err != nil || len(bytes.TrimSpace(body)) == 0 || bytes.TrimSpace(body)[0] != '{' || req.Version != "2.0" || req.Method == "" || !validID(req.ID) {
			writeError(w, nil, -32600, "Invalid Request", nil)
			return
		}
		result, fault := routeMethod(r.Context(), session, req.Method, req.Params)
		if req.ID == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode(response{Version: "2.0", ID: req.ID, Result: result, Error: fault})
	})
}

func validID(id json.RawMessage) bool {
	if id == nil {
		return true
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(id))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return false
	}
	switch value.(type) {
	case nil, string, json.Number:
		return true
	default:
		return false
	}
}

func writeError(w http.ResponseWriter, id json.RawMessage, code int, message string, data any) {
	if id == nil {
		id = json.RawMessage("null")
	}
	_ = json.NewEncoder(w).Encode(response{Version: "2.0", ID: id, Error: &rpcError{Code: code, Message: message, Data: data}})
}
