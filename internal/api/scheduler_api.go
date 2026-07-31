package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	gocron "github.com/go-co-op/gocron/v2"
	"rtkron/internal/store"
	"rtkron/internal/worker"
)

// RegisterSchedulerRoutes registers the scheduler management endpoints used by
// the embedded dashboard (config, jobs list, run now, delete).
func RegisterSchedulerRoutes(mux *http.ServeMux, db *store.SQLiteStore, wp *worker.WorkerPool) {
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		handleConfig(w, r)
	})
	mux.HandleFunc("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
		handleJobs(w, r, db, wp)
	})
	mux.HandleFunc("/api/jobs/", func(w http.ResponseWriter, r *http.Request) {
		handleJobAction(w, r, db, wp)
	})
}

// GET /api/config
func handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"title": "RTKron Codeg Manager",
	})
}

// jobResp is the JSON shape consumed by the embedded dashboard (app.js).
type jobResp struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Tags           []string `json:"tags"`
	Schedule       string   `json:"schedule"`
	ScheduleDetail string   `json:"scheduleDetail"`
	NextRun        string   `json:"nextRun"`
	LastRun        string   `json:"lastRun"`
	NextRuns       []string `json:"nextRuns"`
}

// GET /api/jobs
func handleJobs(w http.ResponseWriter, r *http.Request, db *store.SQLiteStore, wp *worker.WorkerPool) {
	w.Header().Set("Content-Type", "application/json")

	stored, err := db.GetEnabledJobs()
	if err != nil {
		http.Error(w, "failed to load jobs", http.StatusInternalServerError)
		return
	}
	byID := make(map[string]store.Job, len(stored))
	for _, j := range stored {
		byID[j.ID] = j
	}

	// live scheduler jobs keyed by their tag (the RTkron jobID string)
	live := map[string]gocron.Job{}
	if wp.Scheduler() != nil {
		for _, j := range wp.Scheduler().Jobs() {
			for _, tag := range j.Tags() {
				live[tag] = j
			}
		}
	}

	out := make([]jobResp, 0, len(byID))
	for _, sj := range stored {
		resp := jobResp{
			ID:       sj.ID,
			Name:     sj.ID,
			Tags:     []string{sj.ID},
			Schedule: sj.CronExpr,
		}
		if sj.Owner != "" {
			resp.Name = sj.Owner
		}
		if lj, ok := live[sj.ID]; ok {
			if nr, err := lj.NextRun(); err == nil && !nr.IsZero() {
				resp.NextRun = nr.UTC().Format(time.RFC3339)
			}
			if lr, err := lj.LastRun(); err == nil && !lr.IsZero() {
				resp.LastRun = lr.UTC().Format(time.RFC3339)
			}
		}
		// fall back to the persisted last run timestamp
		if resp.LastRun == "" && sj.LastRun.Valid {
			resp.LastRun = sj.LastRun.String
		}
		resp.NextRuns = computeNextRuns(sj.CronExpr, 5)
		if resp.NextRun == "" && len(resp.NextRuns) > 0 {
			resp.NextRun = resp.NextRuns[0]
		}
		out = append(out, resp)
	}

	_ = json.NewEncoder(w).Encode(out)
}

// computeNextRuns parses a cron expression and returns the next n run times.
func computeNextRuns(cronExpr string, n int) []string {
	if strings.TrimSpace(cronExpr) == "" {
		return nil
	}
	schedule, err := cron.ParseStandard(cronExpr)
	if err != nil {
		return nil
	}
	runs := make([]string, 0, n)
	next := time.Now()
	for i := 0; i < n; i++ {
		next = schedule.Next(next)
		runs = append(runs, next.UTC().Format(time.RFC3339))
	}
	return runs
}

// POST /api/jobs/{id}/run and DELETE /api/jobs/{id}
func handleJobAction(w http.ResponseWriter, r *http.Request, db *store.SQLiteStore, wp *worker.WorkerPool) {
	p := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
	parts := strings.Split(p, "/")
	id := parts[0]
	if id == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch {
	case r.Method == http.MethodDelete && action == "":
		if !jobExists(db, id) {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}
		wp.RemoveScheduledJob(id)
		writeJSON(w, map[string]string{"result": "deleted", "id": id})

	case r.Method == http.MethodPost && action == "run":
		if !jobExists(db, id) {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}
		if err := wp.RunJobNow(id); err != nil {
			http.Error(w, "failed to run job", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"result": "queued", "id": id})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func jobExists(db *store.SQLiteStore, id string) bool {
	jobs, err := db.GetEnabledJobs()
	if err != nil {
		return false
	}
	for _, j := range jobs {
		if j.ID == id {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
