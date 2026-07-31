package worker

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sync"
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

	// per-instance locking to serialize processing for the same workflow instance
	locksMu       sync.Mutex
	instanceLocks map[string]*sync.Mutex
}

func NewWorkerPool(ctx context.Context, s *store.SQLiteStore, c *codeg.Client, cfg *config.Config) (*WorkerPool, error) {
	poolCtx, cancel := context.WithCancel(ctx)
	wp := &WorkerPool{
		Store:         s,
		Client:        c,
		Config:        cfg,
		Events:        make(chan interface{}, cfg.EventQueueSize),
		workers:       cfg.WorkerCount,
		ctx:           poolCtx,
		cancel:        cancel,
		instanceLocks: make(map[string]*sync.Mutex),
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
			_ = w.Store.UpdateJobLastRun(jobID, time.Now())
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

	// Persist the job definition so it can be rehydrated on next startup.
	jobBS, err := json.Marshal(pl)
	if err != nil {
		return fmt.Errorf("marshal persisted payload: %w", err)
	}
	if err := w.Store.SaveJob(&store.Job{
		ID:         jobID,
		WorkflowID: jobID,
		CronExpr:   cronExpr,
		Enabled:    true,
		Payload:    jobBS,
	}); err != nil {
		return fmt.Errorf("persist job: %w", err)
	}
	return nil
}

func (w *WorkerPool) RemoveScheduledJob(jobID string) {
	if w.scheduler != nil {
		w.scheduler.RemoveByTags(jobID)
	}
	_ = w.Store.DeleteJob(jobID)
}

// RehydrateScheduler reloads persisted enabled jobs from the store and
// re-registers them with the gocron scheduler (e.g. after a restart).
func (w *WorkerPool) RehydrateScheduler() error {
	if w.scheduler == nil {
		return fmt.Errorf("scheduler not initialized")
	}
	jobs, err := w.Store.GetEnabledJobs()
	if err != nil {
		return fmt.Errorf("load enabled jobs: %w", err)
	}
	for _, j := range jobs {
		var payload map[string]interface{}
		if len(j.Payload) > 0 {
			if err := json.Unmarshal(j.Payload, &payload); err != nil {
				log.Printf("rehydrate: skipping job %s: bad payload: %v", j.ID, err)
				continue
			}
		}
		if err := w.SchedulePromptCron(j.CronExpr, j.ID, payload); err != nil {
			log.Printf("rehydrate: failed to schedule job %s: %v", j.ID, err)
			continue
		}
		log.Printf("rehydrated job %s (%s)", j.ID, j.CronExpr)
	}
	return nil
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
	connectionID, _ := evMap["connection_id"].(string)
	sessionID, _ := evMap["session_id"].(string)
	turnID, _ := evMap["turn_id"].(string)

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
		procErr = w.handleTurnComplete(ctx, eventID, connectionID, sessionID, turnID, evMap)
	case "scheduled_prompt":
		procErr = w.handleScheduledPrompt(ctx, evMap)
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
	connectionID, _ := ev["connection_id"].(string)
	sessionID, _ := ev["session_id"].(string)

	_ = w.Store.InsertAudit(eventID, "permission_request_received", []byte(stringOrJSON(ev)))

	// If connection_id is whitelisted in auto_approve_rules, auto-approve.
	if connectionID != "" {
		rule, err := w.Store.GetAutoApproveRule(connectionID)
		if err != nil && err != sql.ErrNoRows {
			// DB error: record deadletter and bail
			_ = w.Store.InsertDeadLetter(stringOrJSON(ev), 0, fmt.Sprintf("auto-approve lookup error: %v", err))
			return fmt.Errorf("auto-approve lookup failed: %w", err)
		}
		if rule != nil {
			// Auto-approve path: extract pending_request_id from the session snapshot,
			// then actually approve it so the agent is unblocked.
			var pendingRequestID string
			if w.Client != nil && sessionID != "" {
				resp, err := w.Client.AcpGetSessionSnapshot(ctx, sessionID)
				if err != nil {
					// snapshot fetch failure is non-fatal for approval; record audit
					_ = w.Store.InsertAudit(eventID, "snapshot_fetch_failed", []byte(err.Error()))
				} else {
					_ = w.Store.InsertAudit(eventID, "snapshot_fetched", resp)
					var snap map[string]interface{}
					if err := json.Unmarshal(resp, &snap); err == nil {
						if v, ok := snap["pending_request_id"].(string); ok {
							pendingRequestID = v
						}
					}
				}
			}

			if pendingRequestID != "" && w.Client != nil {
				_, err := w.Client.AcpRespondPermission(ctx, pendingRequestID, "approve", "auto-approved by policy rule")
				if err != nil {
					_ = w.Store.InsertDeadLetter(stringOrJSON(ev), 0, fmt.Sprintf("acp_respond_permission error: %v", err))
					return fmt.Errorf("acp_respond_permission: %w", err)
				}
				audit, _ := json.Marshal(map[string]string{"connection_id": connectionID, "pending_request_id": pendingRequestID, "rule_id": fmt.Sprintf("%d", rule.ID)})
				_ = w.Store.InsertAudit(eventID, "auto_approved", audit)
			} else {
				_ = w.Store.InsertAudit(eventID, "auto_approved", []byte(fmt.Sprintf(`{"connection_id":"%s","rule_id":%d,"approved":true,"note":"no pending_request_id found in snapshot"}`, connectionID, rule.ID)))
			}
			// mark idempotency for webhook-originated events is handled by processEvent/reserved key logic
			_ = w.Store.MarkIdempotencyDone("event:" + eventID)
			return nil
		}
	}

	// Not whitelisted: normal flow (no auto-approve). Insert audit and leave idempotency for worker flow.
	_ = w.Store.InsertAudit(eventID, "permission_request_pending", []byte(stringOrJSON(ev)))
	return nil
}

func (w *WorkerPool) handleScheduledPrompt(ctx context.Context, ev map[string]interface{}) error {
	// Extract canonical fields
	eventID, _ := ev["event_id"].(string)
	scheduledID, _ := ev["scheduled_id"].(string)
	payloadIface := ev["payload"]

	// Ensure we have an event id; if not, synthesize one deterministically from scheduled_id + timestamp
	if eventID == "" {
		if scheduledID != "" {
			eventID = fmt.Sprintf("scheduled-%s-%d", scheduledID, time.Now().UnixNano())
		} else {
			eventID = fmt.Sprintf("scheduled-unknown-%d", time.Now().UnixNano())
		}
	}

	// Use the same event-level idempotency key pattern as webhooks/other events.
	idKey := "event:" + eventID

	// Reserve idempotency for this scheduled run.
	ok, err := w.Store.EnsureIdempotency(idKey)
	if err != nil {
		_ = w.Store.InsertDeadLetter(stringOrJSON(ev), 0, fmt.Sprintf("ensure idempotency error: %v", err))
		return fmt.Errorf("ensure idempotency failed for scheduled prompt %s: %w", eventID, err)
	}
	if !ok {
		// duplicate scheduled run (already reserved/processed)
		log.Printf("handleScheduledPrompt: duplicate scheduled event %s; skipping", idKey)
		return nil
	}

	// Convert payload to map[string]interface{} if needed
	var payload map[string]interface{}
	switch p := payloadIface.(type) {
	case map[string]interface{}:
		payload = p
	case string:
		_ = json.Unmarshal([]byte(p), &payload)
	default:
		// best-effort marshal/unmarshal
		bs, _ := json.Marshal(p)
		_ = json.Unmarshal(bs, &payload)
	}

	// Build prompt payload for the external client
	promptPayload := map[string]interface{}{
		"scheduled_id": scheduledID,
		"event_id":     eventID,
		"payload":      payload,
		"trigger":      "scheduled_prompt",
	}

	// If no client configured, record an audit and mark idempotency done
	if w.Client == nil {
		_ = w.Store.InsertAudit(eventID, "scheduled_prompt_no_client", []byte(stringOrJSON(promptPayload)))
		_ = w.Store.MarkIdempotencyDone(idKey)
		return nil
	}

	// Optionally create an external idempotency key for the remote request
	externalIdem := fmt.Sprintf("scheduled:%s:event:%s", scheduledID, eventID)

	// Dispatch to Codeg (or external service)
	bs, err := json.Marshal(promptPayload)
	if err != nil {
		_ = w.Store.MarkIdempotencyDone(idKey)
		return fmt.Errorf("marshal scheduled prompt payload: %w", err)
	}
	resp, err := w.Client.AcpPrompt(ctx, bs, externalIdem)
	if err != nil {
		// On failure, write to deadletter and leave idempotency row for inspection
		_ = w.Store.InsertDeadLetter(stringOrJSON(ev), 0, fmt.Sprintf("acp_prompt error: %v", err))
		// Do not mark idempotency done so operators can retry manually if desired
		log.Printf("handleScheduledPrompt: dispatch failed for %s: %v", eventID, err)
		return fmt.Errorf("dispatch scheduled prompt failed: %w", err)
	}

	// Success: audit, mark idempotency done
	_ = w.Store.InsertAudit(eventID, "scheduled_prompt_dispatched", resp)
	if err := w.Store.MarkIdempotencyDone(idKey); err != nil {
		log.Printf("handleScheduledPrompt: failed to mark idempotency done for %s: %v", idKey, err)
		// still return success because prompt was dispatched
	}
	return nil
}

func (w *WorkerPool) handleTurnComplete(ctx context.Context, eventID, connectionID, sessionID, turnID string, ev map[string]interface{}) error {
	// If there's no turnID, fall back to existing behavior (no turn-level idempotency)
	if turnID == "" {
		// simple audit and return
		_ = w.Store.InsertAudit(eventID, "turn_complete_no_turnid", []byte(stringOrJSON(ev)))
		return nil
	}

	// Determine current attempt number from instance (if available) or default to 1
	attempt := 1
	turnKeyBase := "turn:" + turnID

	// Try to load instance to get retry count (best-effort)
	var inst *store.WorkflowInstance
	var err error
	if sessionID != "" {
		inst, err = w.Store.GetInstanceBySession(sessionID)
	} else if connectionID != "" {
		inst, err = w.Store.GetInstanceByConnection(connectionID)
	}
	if err != nil && err != sql.ErrNoRows {
		// DB error
		return fmt.Errorf("load instance: %w", err)
	}
	if inst != nil {
		// attempt number is current retries + 1
		attempt = inst.Retries + 1
	}

	// Acquire per-instance lock if we have an instance ID to serialize mutations.
	var instLock *sync.Mutex
	if inst != nil && inst.ID != "" {
		instLock = w.getInstanceLock(inst.ID)
		instLock.Lock()
		defer instLock.Unlock()
	}

	// From here on, it's safe to mutate inst fields and write them back without races
	// because the per-instance lock is held (if inst was found). For events without an instance,
	// we proceed without the lock.

	// Compose an attempt-specific idempotency key so retries are distinct
	turnAttemptKey := fmt.Sprintf("%s:attempt:%d", turnKeyBase, attempt)

	// Reserve idempotency for this attempt (insert row if not exists)
	ok, err := w.Store.EnsureIdempotency(turnAttemptKey)
	if err != nil {
		// DB error while reserving; write to deadletter and bail
		_ = w.Store.InsertDeadLetter(stringOrJSON(ev), attempt-1, fmt.Sprintf("reserve idempotency error: %v", err))
		return fmt.Errorf("reserve idempotency failed: %w", err)
	}
	if !ok {
		// Already reserved (concurrent attempt); skip processing this envelope
		log.Printf("handleTurnComplete: attempt key already reserved %s; skipping", turnAttemptKey)
		return nil
	}

	// At this point we have reserved the attempt-specific idempotency key.
	// Proceed with processing the turn. Do NOT mark the key done until success.

	// Try to find an active instance (we already attempted above)
	if inst == nil {
		// nothing to chain; record audit and mark attempt done
		_ = w.Store.InsertAudit(eventID, "turn_complete_no_instance", []byte(stringOrJSON(ev)))
		// mark this attempt idempotency done
		_ = w.Store.MarkIdempotencyDone(turnAttemptKey)
		return nil
	}

	// compute next node (use your existing workflow logic)
	nextNode, err := computeNextNode(inst, ev)
	if err != nil {
		// transient compute error: increment retries on instance and schedule retry
		inst.Retries++
		_ = w.Store.UpdateInstance(inst)

		// schedule retry attempt; the next attempt key is NOT reserved here -
		// handleTurnComplete reserves it when the retry envelope is processed
		w.scheduleRetry(eventID, connectionID, sessionID, turnID, turnKeyBase, inst.Retries, ev)

		// mark this attempt done (we reserved it earlier; leave it done so we don't reprocess same attempt)
		_ = w.Store.MarkIdempotencyDone(turnAttemptKey)
		return fmt.Errorf("compute next node transient error: %w", err)
	}

	if nextNode == "" {
		// workflow complete
		inst.Status = "completed"
		inst.CurrentNode = ""
		_ = w.Store.UpdateInstance(inst)
		audit, _ := json.Marshal(map[string]string{"instance_id": inst.ID})
		_ = w.Store.InsertAudit(eventID, "workflow_completed", audit)
		_ = w.Store.MarkIdempotencyDone(turnAttemptKey)
		// remove lock entry to avoid map growth for completed instances
		go w.removeInstanceLock(inst.ID)
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
		_ = w.Store.MarkIdempotencyDone(turnAttemptKey)
		return nil
	}

	promptPayload, err := json.Marshal(payload)
	if err != nil {
		_ = w.Store.MarkIdempotencyDone(turnAttemptKey)
		return fmt.Errorf("marshal prompt payload: %w", err)
	}
	resp, err := w.Client.AcpPrompt(ctx, promptPayload, turnAttemptKey)
	if err != nil {
		inst.Retries++
		_ = w.Store.UpdateInstance(inst)
		if inst.Retries >= 3 {
			_ = w.Store.InsertDeadLetter(stringOrJSON(ev), inst.Retries, err.Error())
			inst.Status = "failed"
			_ = w.Store.UpdateInstance(inst)
			_ = w.Store.MarkIdempotencyDone(turnAttemptKey)
			return fmt.Errorf("acp_prompt failed after retries: %w", err)
		}

		// schedule retry attempt; the next attempt key is NOT reserved here -
		// handleTurnComplete reserves it when the retry envelope is processed
		w.scheduleRetry(eventID, connectionID, sessionID, turnID, turnKeyBase, inst.Retries, ev)

		// mark this attempt done (we reserved it earlier; leave it done so we don't reprocess same attempt)
		_ = w.Store.MarkIdempotencyDone(turnAttemptKey)
		return fmt.Errorf("acp_prompt transient error: %w", err)
	}

	inst.CurrentNode = nextNode
	inst.Status = "waiting_for_turn_complete"
	inst.Retries = 0
	_ = w.Store.UpdateInstance(inst)
	_ = w.Store.InsertAudit(eventID, "prompt_dispatched", resp)
	_ = w.Store.MarkIdempotencyDone(turnAttemptKey)
	return nil
}

func (w *WorkerPool) getInstanceLock(instanceID string) *sync.Mutex {
	if instanceID == "" {
		return &sync.Mutex{}
	}
	w.locksMu.Lock()
	defer w.locksMu.Unlock()
	m, ok := w.instanceLocks[instanceID]
	if !ok {
		m = &sync.Mutex{}
		w.instanceLocks[instanceID] = m
	}
	return m
}

func (w *WorkerPool) removeInstanceLock(instanceID string) {
	if instanceID == "" {
		return
	}
	w.locksMu.Lock()
	defer w.locksMu.Unlock()
	delete(w.instanceLocks, instanceID)
}

func (w *WorkerPool) scheduleRetry(eventID, connectionID, sessionID, turnID, turnKeyBase string, retries int, ev map[string]interface{}) {
	nextAttempt := retries + 1
	nextKey := fmt.Sprintf("%s:attempt:%d", turnKeyBase, nextAttempt)
	delay := time.Duration(retries) * time.Second * 5
	time.AfterFunc(delay, func() {
		envelope := map[string]interface{}{
			"reserved_idempotency_key": nextKey,
			"event": map[string]interface{}{
				"event_id":      fmt.Sprintf("%s-retry-%d", eventID, retries),
				"type":          "turn_complete",
				"connection_id": connectionID,
				"session_id":    sessionID,
				"turn_id":       turnID,
				"payload":       ev["payload"],
			},
		}
		_ = w.Enqueue(envelope)
	})
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