package gateway

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/adhocore/gronx"
	"github.com/sipeed/picoclaw/pkg/cron"
)

const scheduleAPIPath = "/internal/schedules"

type scheduleMutation struct {
	Name     string            `json:"name"`
	Enabled  *bool             `json:"enabled,omitempty"`
	Schedule cron.CronSchedule `json:"schedule"`
	Payload  cron.CronPayload  `json:"payload"`
}

func registerScheduleAPI(service *cron.CronService, token string, cm interface {
	HandleFunc(string, func(http.ResponseWriter, *http.Request))
}) {
	cm.HandleFunc(scheduleAPIPath+"/", scheduleAPIHandler(service, token))
	cm.HandleFunc(scheduleAPIPath, scheduleAPIHandler(service, token))
}

func scheduleAPIHandler(service *cron.CronService, token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		authorization := r.Header.Get("Authorization")
		given := ""
		if strings.HasPrefix(authorization, "Bearer ") {
			given = strings.TrimPrefix(authorization, "Bearer ")
		}
		if token == "" ||
			subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
			writeScheduleError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		id := strings.TrimPrefix(r.URL.Path, scheduleAPIPath)
		id = strings.Trim(id, "/")
		runNow := strings.HasSuffix(id, "/run")
		if runNow {
			id = strings.TrimSuffix(id, "/run")
			id = strings.TrimSuffix(id, "/")
		}
		switch {
		case r.Method == http.MethodGet && id == "":
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": service.ListJobs(true)})
		case r.Method == http.MethodPost && id == "":
			var input scheduleMutation
			if err := decodeScheduleMutation(r, &input); err != nil {
				writeScheduleError(w, http.StatusBadRequest, err.Error())
				return
			}
			enabled := true
			if input.Enabled != nil {
				enabled = *input.Enabled
			}
			job, err := service.AddJobEnabled(
				input.Name,
				input.Schedule,
				input.Payload.Message,
				input.Payload.Channel,
				input.Payload.To,
				enabled,
			)
			if err != nil {
				writeScheduleError(w, http.StatusInternalServerError, err.Error())
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(job)
		case r.Method == http.MethodPut && id != "":
			existing, ok := service.GetJob(id)
			if !ok {
				writeScheduleError(w, http.StatusNotFound, "scheduled job not found")
				return
			}
			var input scheduleMutation
			if err := decodeScheduleMutation(r, &input); err != nil {
				writeScheduleError(w, http.StatusBadRequest, err.Error())
				return
			}
			existing.Name = input.Name
			existing.Schedule = input.Schedule
			existing.Payload = input.Payload
			existing.Payload.Kind = "agent_turn"
			existing.DeleteAfterRun = input.Schedule.Kind == "at"
			if input.Enabled != nil {
				existing.Enabled = *input.Enabled
			}
			if err := service.UpdateJob(existing); err != nil {
				writeScheduleError(w, http.StatusInternalServerError, err.Error())
				return
			}
			updated, _ := service.GetJob(id)
			_ = json.NewEncoder(w).Encode(updated)
		case r.Method == http.MethodPost && id != "" && runNow:
			if _, ok := service.GetJob(id); !ok {
				writeScheduleError(w, http.StatusNotFound, "scheduled job not found")
				return
			}
			if !service.RunJobNow(id) {
				writeScheduleError(w, http.StatusConflict, "scheduled job is already running")
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "started"})
		case r.Method == http.MethodDelete && id != "":
			if !service.RemoveJob(id) {
				writeScheduleError(w, http.StatusNotFound, "scheduled job not found")
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		default:
			writeScheduleError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func decodeScheduleMutation(r *http.Request, input *scheduleMutation) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(input); err != nil {
		return errors.New("invalid request body")
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Payload.Message = strings.TrimSpace(input.Payload.Message)
	input.Payload.Channel = strings.ToLower(strings.TrimSpace(input.Payload.Channel))
	input.Payload.To = strings.TrimSpace(input.Payload.To)
	input.Payload.Kind = "agent_turn"
	if input.Name == "" || input.Payload.Message == "" ||
		input.Payload.Channel == "" || input.Payload.To == "" {
		return errors.New("name, message, channel, and destination are required")
	}
	switch input.Schedule.Kind {
	case "cron":
		input.Schedule.Expr = strings.TrimSpace(input.Schedule.Expr)
		if input.Schedule.Expr == "" || !gronx.IsValid(input.Schedule.Expr) {
			return errors.New("invalid cron expression")
		}
		input.Schedule.AtMS = nil
		input.Schedule.EveryMS = nil
	case "at":
		if input.Schedule.AtMS == nil || *input.Schedule.AtMS <= 0 {
			return errors.New("one-time schedule requires atMs")
		}
		input.Schedule.EveryMS = nil
		input.Schedule.Expr = ""
	case "every":
		if input.Schedule.EveryMS == nil || *input.Schedule.EveryMS < 1000 {
			return errors.New("interval must be at least one second")
		}
		input.Schedule.AtMS = nil
		input.Schedule.Expr = ""
	default:
		return errors.New("schedule kind must be cron, at, or every")
	}
	return nil
}

func writeScheduleError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
