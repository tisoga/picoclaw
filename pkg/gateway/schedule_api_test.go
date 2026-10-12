package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/cron"
)

func TestScheduleAPI_CRUDAndAuthentication(t *testing.T) {
	service := cron.NewCronService(filepath.Join(t.TempDir(), "cron", "jobs.json"), nil)
	handler := scheduleAPIHandler(service, "secret")

	unauthorized := httptest.NewRecorder()
	handler(unauthorized, httptest.NewRequest(http.MethodGet, scheduleAPIPath, nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	body := []byte(`{
		"name":"Daily update",
		"enabled":true,
		"schedule":{"kind":"cron","expr":"0 8 * * *"},
		"payload":{"kind":"agent_turn","message":"Send an update","channel":"telegram","to":"123"}
	}`)
	create := httptest.NewRequest(http.MethodPost, scheduleAPIPath, bytes.NewReader(body))
	create.Header.Set("Authorization", "Bearer secret")
	created := httptest.NewRecorder()
	handler(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", created.Code, created.Body.String())
	}
	var job cron.CronJob
	if err := json.NewDecoder(created.Body).Decode(&job); err != nil {
		t.Fatal(err)
	}
	if job.Payload.Channel != "telegram" || job.State.NextRunAtMS == nil {
		t.Fatalf("unexpected created job: %#v", job)
	}

	updateBody := []byte(`{
		"name":"Paused update",
		"enabled":false,
		"schedule":{"kind":"every","everyMs":60000},
		"payload":{"kind":"agent_turn","message":"Send it","channel":"whatsapp","to":"1@lid"}
	}`)
	update := httptest.NewRequest(
		http.MethodPut,
		scheduleAPIPath+"/"+job.ID,
		bytes.NewReader(updateBody),
	)
	update.Header.Set("Authorization", "Bearer secret")
	updated := httptest.NewRecorder()
	handler(updated, update)
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", updated.Code, updated.Body.String())
	}
	got, ok := service.GetJob(job.ID)
	if !ok || got.Enabled || got.Payload.Channel != "whatsapp" ||
		got.State.NextRunAtMS != nil {
		t.Fatalf("unexpected updated job: %#v", got)
	}

	run := httptest.NewRequest(http.MethodPost, scheduleAPIPath+"/"+job.ID+"/run", nil)
	run.Header.Set("Authorization", "Bearer secret")
	runResponse := httptest.NewRecorder()
	handler(runResponse, run)
	if runResponse.Code != http.StatusAccepted {
		t.Fatalf("run status = %d, body=%s", runResponse.Code, runResponse.Body.String())
	}

	remove := httptest.NewRequest(http.MethodDelete, scheduleAPIPath+"/"+job.ID, nil)
	remove.Header.Set("Authorization", "Bearer secret")
	removed := httptest.NewRecorder()
	handler(removed, remove)
	if removed.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body=%s", removed.Code, removed.Body.String())
	}
	if len(service.ListJobs(true)) != 0 {
		t.Fatal("job was not deleted")
	}
}

func TestScheduleAPI_RejectsInvalidInput(t *testing.T) {
	service := cron.NewCronService(filepath.Join(t.TempDir(), "jobs.json"), nil)
	handler := scheduleAPIHandler(service, "secret")
	request := httptest.NewRequest(
		http.MethodPost,
		scheduleAPIPath,
		bytes.NewBufferString(`{
			"name":"Bad",
			"schedule":{"kind":"cron","expr":"not cron"},
			"payload":{"message":"test","channel":"telegram","to":"123"}
		}`),
	)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if len(service.ListJobs(true)) != 0 {
		t.Fatal("invalid job was saved")
	}
}
