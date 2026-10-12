package api

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestScheduleProxyUsesGatewayPIDToken(t *testing.T) {
	var authorization string
	var targetPath string
	gateway := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		targetPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jobs":[]}`))
	}))
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gateway.Listener = listener
	gateway.Start()
	defer gateway.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	pidBody, _ := json.Marshal(map[string]any{
		"pid": os.Getpid(), "token": "runtime-secret", "version": "test",
		"port": port, "host": "127.0.0.1",
	})
	if err := os.WriteFile(filepath.Join(dir, ".picoclaw.pid"), pidBody, 0o600); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/schedules", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if authorization != "Bearer runtime-secret" {
		t.Fatalf("Authorization = %q", authorization)
	}
	if got := response.Body.String(); got != `{"jobs":[]}` {
		t.Fatalf("body = %q", got)
	}

	runResponse := httptest.NewRecorder()
	mux.ServeHTTP(
		runResponse,
		httptest.NewRequest(http.MethodPost, "/api/schedules/job-1/run", nil),
	)
	if runResponse.Code != http.StatusOK {
		t.Fatalf("run status = %d, body=%s", runResponse.Code, runResponse.Body.String())
	}
	if targetPath != gatewaySchedulesPath+"/job-1/run" {
		t.Fatalf("run target path = %q", targetPath)
	}
}
