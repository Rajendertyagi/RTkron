package worker

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	gocron "github.com/go-co-op/gocron/v2"
	"rtkron/internal/codeg"
	"rtkron/internal/config"
	"rtkron/internal/store"
)

type WorkerPool struct {
	Store     *store.SQLiteStore
	Client    *codeg.Client
	Config    *config.Config
	Events    chan interface{}
	workers   int
	ctx       context.Context
	cancel    context.CancelFunc
	scheduler gocron.Scheduler
}

func NewWorkerPool(ctx context.Context, s *store.SQLiteStore, c *codeg.Client, cfg *config.Config) (*WorkerPool, error) {
	poolCtx, cancel := context.WithCancel(ctx)
	wp := &WorkerPool{
		Store:   s,
		Client:  c,
		Config:  cfg,
		Events:  make(chan interface{}, cfg.EventQueueSize),
		workers: cfg.WorkerCount,
		ctx:     poolCtx,
		cancel:  cancel,
	}
	scheduler, err := gocron.NewScheduler()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create scheduler: %w", err)
	}
	wp.scheduler = scheduler
	return wp, nil
}

func (w *WorkerPool) Start() {
	for i := 1; i <= w.workers; i++ {
		go w.workerLoop(i)
	}
	log.Printf("started %d workers", w.workers)

	w.scheduler.Start()
	log.Printf("scheduler started")
}

func (w *WorkerPool) Stop() {
	if w.scheduler != nil {
		_ = w.scheduler.StopJobs()
	}
	w.cancel()
}

func (w *WorkerPool) Enqueue(event interface{}) bool {
	select {
	case w.Events <- event:
		return true
	default:
		return false
	}
}

func (w *WorkerPool) SchedulePromptCron(cronExpr string, jobID string, payload map[string]interface{}) error {
	if w.scheduler == nil {
		return fmt.Errorf("scheduler not initialized")
	}

	var pl map[string]interface{}
	bs, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(bs))
	dec.UseNumber()
	if err := dec.Decode(&pl); err != nil {
		return fmt.Errorf("copy payload: %w", err)
	}

	_, err = w.scheduler.NewJob(
		gocron.CronJob(cronExpr, false),
		gocron.NewTask(func() {
			ev := map[string]interface{}{
				"event_id":     fmt.Sprintf("cron-%s-%d", jobID, time.Now().Unix()),
				"type":         "scheduled_prompt",
				"scheduled_id": jobID,
				"payload":      pl,
			}
			if !w.Enqueue(ev) {
				_ = w.Store.InsertDeadLetter(stringOrJSON(ev), 0, "queue_full_on_schedule")
			}
		}),
		gocron.WithTags(jobID),
		gocron.WithSingletonMode(gocron.LimitModeReschedule),
	)
	if err != nil {
		return err
	}
	return nil
}

func (w *WorkerPool) RemoveScheduledJob(jobID string) {
	if w.scheduler == nil {
		return
	}
	w.scheduler.RemoveByTags(jobID)
}

func (w *WorkerPool) Scheduler() gocron.Scheduler {
	return w.scheduler
}

func (w *WorkerPool) workerLoop(id int) {
	log.Printf("worker-%d running", id)
	for {
		select {
		case <-w.ctx.Done():
			log.Printf("worker-%d stopping", id)
			return
		case e := <-w.Events:
			ctx, cancel := context.WithTimeout(w.ctx, 30*time.Second)
			w.processEvent(ctx, e)
			cancel()
		}
	}
}

func (w *WorkerPool) processEvent(ctx context.Context, evt interface{}) {
	// normalize to a map[string]interface{}
	var evMap map[string]interface{}
	var reservedKey string
	var reserved bool

	// Accept either the envelope from webhook handler or a plain event map
	switch t := evt.(type) {
	case map[string]interface{}:
		// check for reserved envelope shape
		if rk, ok := t["reserved_idempotency_key"].(string); ok && rk != "" {
			reserved = true
			reservedKey = rk
			// extract inner event if present (map or struct)
			if inner, ok := t["event"]; ok && inner != nil {
				if m, ok := inner.(map[string]interface{}); ok {
					evMap = m
				} else if bs, err := json.Marshal(inner); err == nil {
					_ = json.Unmarshal(bs, &evMap)
				}
			}
			// fallback: try to parse raw_body
			if len(evMap) == 0 {
				if raw, ok := t["raw_body"].(string); ok && raw != "" {
					_ = json.Unmarshal([]byte(raw), &evMap)
				}
			}
		} else {
			evMap = t
		}
	case []byte:
		_ = json.Unmarshal(t, &evMap)
	default:
		bs, err := json.Marshal(t)
		if err != nil {
			log.Printf("processEvent: cannot marshal event: %v", err)
			return
		}
		_ = json.Unmarshal(bs, &evMap)
	}

	// extract common fields
	eventID, _ := evMap["event_id"].(string)
	evType, _ := evMap["type"].(string)

	// fallback event id if missing
	if eventID == "" {
		if p, ok := evMap["payload"].(map[string]interface{}); ok {
			if id, ok := p["id"].(string); ok && id != "" {
				eventID = id
			}
		}
		if eventID == "" {
			eventID = fmt.Sprintf("evt-local-%d", time.Now().UnixNano())
		}
	}

	// Determine idempotency key to use
	var idKey string
	if reserved {
		idKey = reservedKey
	} else {
		idKey = "event:" + eventID
	}

	// If not reserved by the webhook handler, ensure idempotency now.
	if !reserved {
		ok, err := w.Store.EnsureIdempotency(idKey)
		if err != nil {
			log.Printf("processEvent: idempotency check failed for %s: %v", idKey, err)
			_ = w.Store.InsertDeadLetter(stringOrJSON(evMap), 0, fmt.Sprintf("idempotency error: %v", err))
			return
		}
		if !ok {
			log.Printf("processEvent: duplicate event %s; skipping", idKey)
			return
		}
	} else {
		// reserved: webhook handler already inserted idempotency row with status 'received'
		log.Printf("processEvent: processing reserved event %s (id=%s)", idKey, eventID)
	}

	// route by type
	var procErr error
	switch evType {
	case "permission_request":
		procErr = w.handlePermissionRequest(ctx, evMap)
	case "turn_complete":
		procErr = w.handleTurnComplete(ctx, evMap)
	case "scheduled_prompt":
		// scheduled prompts come from scheduler; reuse turn_complete handler
		procErr = w.handleTurnComplete(ctx, evMap)
	default:
		_ = w.Store.InsertAudit(eventID, "unhandled_event", []byte(stringOrJSON(evMap)))
		procErr = nil
	}

	// On failure, write to deadletter and leave status for inspection.
	if procErr != nil {
		log.Printf("processEvent: processing failed for %s: %v", idKey, procErr)
		_ = w.Store.InsertDeadLetter(stringOrJSON(evMap), 1, procErr.Error())
		return
	}

	// mark idempotency status = done
	if err := w.Store.MarkIdempotencyDone(idKey); err != nil {
		log.Printf("processEvent: failed to mark idempotency done for %s: %v", idKey, err)
		// best-effort: still record audit
		_ = w.Store.InsertAudit(eventID, "processed_but_mark_done_failed", []byte(stringOrJSON(evMap)))
		return
	}

	// final audit for successful processing
	_ = w.Store.InsertAudit(eventID, "processed", []byte(stringOrJSON(evMap)))
}

func (w *WorkerPool) handlePermissionRequest(ctx context.Context, ev map[string]interface{}) error {
	eventID, _ := ev["event_id"].(string)
	sessionID, _ := ev["session_id"].(string)

	if err := w.Store.InsertAudit(eventID, "permission_request_received", []byte(stringOrJSON(ev))); err != nil {
		log.Printf("handlePermissionRequest: audit insert failed: %v", err)
	}

	if !w.Config.AutoApprove {
		log.Printf("handlePermissionRequest: auto-approve disabled; event=%s", eventID)
		return nil
	}

	var pendingRequestID string
	if w.Client != nil && sessionID != "" {
		resp, err := w.Client.AcpGetSessionSnapshot(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("acp_get_session_snapshot: %w", err)
		}
		var snap map[string]interface{}
		if err := json.Unmarshal(resp, &snap); err == nil {
			if v, ok := snap["pending_request_id"].(string); ok {
				pendingRequestID = v
			}
		}
	}

	if pendingRequestID != "" && w.Client != nil {
		_, err := w.Client.AcpRespondPermission(ctx, pendingRequestID, "approve", "auto-approved by local config")
		if err != nil {
			return fmt.Errorf("acp_respond_permission: %w", err)
		}
		audit, _ := json.Marshal(map[string]string{"pending_request_id": pendingRequestID})
		_ = w.Store.InsertAudit(eventID, "auto_approved", audit)
		return nil
	}

	if w.Client == nil || pendingRequestID == "" {
		log.Printf("handlePermissionRequest: no client or pendingRequestID; recorded audit only for event=%s", eventID)
		return nil
	}

	return nil
}

func (w *WorkerPool) handleTurnComplete(ctx context.Context, ev map[string]interface{}) error {
	eventID, _ := ev["event_id"].(string)
	connectionID, _ := ev["connection_id"].(string)
	sessionID, _ := ev["session_id"].(string)
	turnID, _ := ev["turn_id"].(string)

	if turnID != "" {
		turnKey := "turn:" + turnID
		ok, err := w.Store.EnsureIdempotency(turnKey)
		if err != nil {
			return fmt.Errorf("turn idempotency check failed: %w", err)
		}
		if !ok {
			log.Printf("handleTurnComplete: duplicate turn %s; skipping", turnID)
			return nil
		}
		defer func() {
			_ = w.Store.MarkIdempotencyDone(turnKey)
		}()
	}

	var inst *store.WorkflowInstance
	var err error
	if sessionID != "" {
		inst, err = w.Store.GetInstanceBySession(sessionID)
	} else if connectionID != "" {
		inst, err = w.Store.GetInstanceByConnection(connectionID)
	} else {
		inst = nil
	}
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("load instance: %w", err)
	}
	if inst == nil {
		_ = w.Store.InsertAudit(eventID, "turn_complete_no_instance", []byte(stringOrJSON(ev)))
		return nil
	}

	nextNode, err := computeNextNode(inst, ev)
	if err != nil {
		inst.Retries++
		_ = w.Store.UpdateInstance(inst)
		return fmt.Errorf("compute next node: %w", err)
	}
	if nextNode == "" {
		inst.Status = "completed"
		inst.CurrentNode = ""
		_ = w.Store.UpdateInstance(inst)
		audit, _ := json.Marshal(map[string]string{"instance_id": inst.ID})
		_ = w.Store.InsertAudit(eventID, "workflow_completed", audit)
		return nil
	}

	payload := map[string]interface{}{
		"workflow_id": inst.WorkflowID,
		"instance_id": inst.ID,
		"node":        nextNode,
		"session_id":  sessionID,
		"connection":  connectionID,
		"trigger":     "turn_complete",
	}

	if w.Client == nil {
		inst.CurrentNode = nextNode
		inst.Status = "waiting_for_turn_complete"
		_ = w.Store.UpdateInstance(inst)
		_ = w.Store.InsertAudit(eventID, "prompt_skipped_no_client", []byte(stringOrJSON(payload)))
		return nil
	}

	promptPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal prompt payload: %w", err)
	}
	resp, err := w.Client.SendPrompt(ctx, promptPayload)
	if err != nil {
		inst.Retries++
		_ = w.Store.UpdateInstance(inst)
		if inst.Retries >= 3 {
			_ = w.Store.InsertDeadLetter(stringOrJSON(ev), inst.Retries, err.Error())
			inst.Status = "failed"
			_ = w.Store.UpdateInstance(inst)
			return fmt.Errorf("send_prompt failed after retries: %w", err)
		}
		time.AfterFunc(time.Duration(inst.Retries)*time.Second*5, func() {
			_ = w.Enqueue(map[string]interface{}{
				"event_id":      fmt.Sprintf("%s-retry-%d", eventID, inst.Retries),
				"type":          "turn_complete",
				"connection_id": connectionID,
				"session_id":    sessionID,
				"turn_id":       turnID,
				"payload":       ev["payload"],
			})
		})
		return fmt.Errorf("send_prompt transient error: %w", err)
	}

	inst.CurrentNode = nextNode
	inst.Status = "waiting_for_turn_complete"
	inst.Retries = 0
	_ = w.Store.UpdateInstance(inst)
	_ = w.Store.InsertAudit(eventID, "prompt_dispatched", resp)
	return nil
}

func computeNextNode(inst *store.WorkflowInstance, ev map[string]interface{}) (string, error) {
	if inst.CurrentNode == "" {
		return "node1", nil
	}
	if inst.CurrentNode == "node1" {
		return "node2", nil
	}
	return "", nil
}

func stringOrJSON(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		bs, _ := json.Marshal(v)
		return string(bs)
	}
}