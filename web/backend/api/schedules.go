package api

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/pid"
)

const gatewaySchedulesPath = "/internal/schedules"

func (h *Handler) registerScheduleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/schedules", h.proxyScheduleRequest)
	mux.HandleFunc("POST /api/schedules", h.proxyScheduleRequest)
	mux.HandleFunc("POST /api/schedules/{id}/run", h.proxyScheduleRequest)
	mux.HandleFunc("PUT /api/schedules/{id}", h.proxyScheduleRequest)
	mux.HandleFunc("DELETE /api/schedules/{id}", h.proxyScheduleRequest)
}

func (h *Handler) proxyScheduleRequest(w http.ResponseWriter, r *http.Request) {
	pidData := pid.ReadPidFileWithCheck(filepath.Dir(h.configPath))
	if pidData == nil {
		http.Error(w, `{"error":"gateway is not running"}`, http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		http.Error(w, `{"error":"failed to read request"}`, http.StatusBadRequest)
		return
	}
	targetPath := gatewaySchedulesPath
	if id := strings.TrimSpace(r.PathValue("id")); id != "" {
		targetPath += "/" + url.PathEscape(id)
		if strings.HasSuffix(r.URL.Path, "/run") {
			targetPath += "/run"
		}
	}
	host := pidData.Host
	if parsed := net.ParseIP(strings.Trim(host, "[]")); parsed == nil || !parsed.IsLoopback() {
		host = "127.0.0.1"
	}
	target := fmt.Sprintf("http://%s%s", net.JoinHostPort(host, fmt.Sprint(pidData.Port)), targetPath)
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if err != nil {
		http.Error(w, `{"error":"failed to create gateway request"}`, http.StatusInternalServerError)
		return
	}
	req.Header.Set("Authorization", "Bearer "+pidData.Token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, `{"error":"gateway schedule service is unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}
