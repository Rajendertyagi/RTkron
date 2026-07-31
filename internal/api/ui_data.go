package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"
)

// RegisterUIDataRoutes registers the UI JSON endpoints on the provided mux.
func RegisterUIDataRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		handleStats(w, r, db)
	})
	mux.HandleFunc("/api/activity", func(w http.ResponseWriter, r *http.Request) {
		handleActivity(w, r, db)
	})
}

// StatsResponse is the JSON shape returned by /api/stats
type StatsResponse struct {
	TotalJobsScheduled   int64  `json:"total_jobs_scheduled"`
	SuccessfulWebhooks   int64  `json:"successful_webhooks"`
	FailedWebhooks       int64  `json:"failed_webhooks"`
	TotalAudits          int64  `json:"total_audits"`
	DeadLetterCount      int64  `json:"deadletter_count"`
	ActiveInstances      int64  `json:"active_instances"`
	ServerTimeUTC        string `json:"server_time_utc"`
	DatabasePathProvided bool   `json:"db_path_provided"`
}

// ActivityItem represents a single activity feed item
type ActivityItem struct {
	Kind      string `json:"kind"`
	ID        int64  `json:"id"`
	EventID   string `json:"event_id"`
	Action    string `json:"action"`
	Payload   string `json:"payload"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error"`
	CreatedAt string `json:"created_at"`
}

func handleStats(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	w.Header().Set("Content-Type", "application/json")

	var resp StatsResponse

	_ = db.QueryRow("SELECT COUNT(1) FROM jobs").Scan(&resp.TotalJobsScheduled)
	_ = db.QueryRow("SELECT COUNT(1) FROM idempotency WHERE status = 'done'").Scan(&resp.SuccessfulWebhooks)
	_ = db.QueryRow("SELECT COUNT(1) FROM deadletter").Scan(&resp.FailedWebhooks)
	_ = db.QueryRow("SELECT COUNT(1) FROM audit").Scan(&resp.TotalAudits)
	_ = db.QueryRow("SELECT COUNT(1) FROM deadletter").Scan(&resp.DeadLetterCount)
	_ = db.QueryRow("SELECT COUNT(1) FROM instances WHERE status IN ('running','waiting','waiting_for_turn_complete')").Scan(&resp.ActiveInstances)

	resp.ServerTimeUTC = time.Now().UTC().Format(time.RFC3339)
	resp.DatabasePathProvided = false

	_ = json.NewEncoder(w).Encode(resp)
}

func handleActivity(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	w.Header().Set("Content-Type", "application/json")

	query := `
	SELECT 'audit' AS kind, id, event_id, action, payload AS payload, NULL AS attempts, NULL AS last_error, created_at
	  FROM audit
	 UNION ALL
	SELECT 'deadletter' AS kind, id, NULL AS event_id, 'deadletter' AS action, event_json AS payload, attempts, last_error, created_at
	  FROM deadletter
	 ORDER BY created_at DESC
	 LIMIT 10;
	`

	rows, err := db.Query(query)
	if err != nil {
		http.Error(w, "query error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := make([]ActivityItem, 0, 10)
	for rows.Next() {
		var it ActivityItem
		var attempts sql.NullInt64
		var lastErr sql.NullString
		var payload sql.NullString
		var eventID sql.NullString
		var action sql.NullString
		var createdAt sql.NullString
		var kind sql.NullString
		var id sql.NullInt64

		if err := rows.Scan(&kind, &id, &eventID, &action, &payload, &attempts, &lastErr, &createdAt); err != nil {
			continue
		}
		if kind.Valid {
			it.Kind = kind.String
		}
		it.ID = id.Int64
		if eventID.Valid {
			it.EventID = eventID.String
		}
		if action.Valid {
			it.Action = action.String
		}
		if payload.Valid {
			it.Payload = payload.String
		}
		if attempts.Valid {
			it.Attempts = int(attempts.Int64)
		}
		if lastErr.Valid {
			it.LastError = lastErr.String
		}
		if createdAt.Valid {
			it.CreatedAt = createdAt.String
		} else {
			it.CreatedAt = time.Now().UTC().Format(time.RFC3339)
		}
		items = append(items, it)
	}

	_ = json.NewEncoder(w).Encode(items)
}