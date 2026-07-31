package worker

import (
    "context"
    "database/sql"
    "encoding/json"
    "fmt"
    "log"
    "time"

    "rtkron/internal/codeg"
    "rtkron/internal/config"
    "rtkron/internal/store"
)

type WorkerPool struct {
    Store   *store.SQLiteStore
    Client  *codeg.Client
    Config  *config.Config
    Events  chan interface{}
    workers int
    ctx     context.Context
    cancel  context.CancelFunc
}

func NewWorkerPool(ctx context.Context, s *store.SQLiteStore, c *codeg.Client, cfg *config.Config) *WorkerPool {
    poolCtx, cancel := context.WithCancel(ctx)
    return &WorkerPool{
        Store:   s,
        Client:  c,
        Config:  cfg,
        Events:  make(chan interface{}, cfg.EventQueueSize),
        workers: cfg.WorkerCount,
        ctx:     poolCtx,
        cancel:  cancel,
    }
}

func (w *WorkerPool) Start() {
    log.Printf("started %d workers", w.workers)
    for i := 1; i <= w.workers; i++ {
        go w.workerLoop(i)
    }
}

func (w *WorkerPool) Stop() {
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

func (w *WorkerPool) workerLoop(id int) {
    log.Printf("worker-%d running", id)
    for {
        select {
        case <-w.ctx.Done():
            log.Printf("worker-%d shutting down", id)
            return
        case ev := <-w.Events:
            w.processEvent(w.ctx, ev)
        }
    }
}

// processEvent implements the core routing logic for incoming events.
func (w *WorkerPool) processEvent(ctx context.Context, evt interface{}) {
    var evMap map[string]interface{}
    switch t := evt.(type) {
    case map[string]interface{}:
        evMap = t
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

    eventID, _ := evMap["event_id"].(string)
    evType, _ := evMap["type"].(string)
    connectionID, _ := evMap["connection_id"].(string)
    sessionID, _ := evMap["session_id"].(string)
    turnID, _ := evMap["turn_id"].(string)

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

    idKey := "event:" + eventID

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

    switch evType {
    case "permission_request":
        if err := w.handlePermissionRequest(ctx, eventID, connectionID, sessionID, evMap); err != nil {
            log.Printf("processEvent: permission_request failed for %s: %v", eventID, err)
            _ = w.Store.InsertDeadLetter(stringOrJSON(evMap), 1, err.Error())
            return
        }
        _ = w.Store.MarkIdempotencyDone(idKey)
    case "turn_complete":
        if err := w.handleTurnComplete(ctx, eventID, connectionID, sessionID, turnID, evMap); err != nil {
            log.Printf("processEvent: turn_complete failed for %s: %v", eventID, err)
            _ = w.Store.InsertDeadLetter(stringOrJSON(evMap), 1, err.Error())
            return
        }
        _ = w.Store.MarkIdempotencyDone(idKey)
    default:
        _ = w.Store.InsertAudit(eventID, "unhandled_event", []byte(stringOrJSON(evMap)))
        _ = w.Store.MarkIdempotencyDone(idKey)
    }
}

func (w *WorkerPool) handlePermissionRequest(ctx context.Context, eventID, connectionID, sessionID string, ev map[string]interface{}) error {
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
        if err := w.Store.InsertAudit(eventID, "auto_approved", []byte(fmt.Sprintf(`{"pending_request_id":"%s"}`, pendingRequestID))); err != nil {
            log.Printf("handlePermissionRequest: audit insert after approve failed: %v", err)
        }
        return nil
    }

    if w.Client == nil || pendingRequestID == "" {
        log.Printf("handlePermissionRequest: no client or pendingRequestID; recorded audit only for event=%s", eventID)
        return nil
    }

    return nil
}

func (w *WorkerPool) handleTurnComplete(ctx context.Context, eventID, connectionID, sessionID, turnID string, ev map[string]interface{}) error {
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
        _ = w.Store.InsertAudit(eventID, "workflow_completed", []byte(fmt.Sprintf(`{"instance_id":"%s"}`, inst.ID)))
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

    idempotencyKey := fmt.Sprintf("job:%s:instance:%s:node:%s", inst.WorkflowID, inst.ID, nextNode)
    if w.Client == nil {
        inst.CurrentNode = nextNode
        inst.Status = "waiting_for_turn_complete"
        _ = w.Store.UpdateInstance(inst)
        _ = w.Store.InsertAudit(eventID, "prompt_skipped_no_client", []byte(stringOrJSON(payload)))
        return nil
    }

    resp, err := w.Client.AcpPrompt(ctx, payload, idempotencyKey)
    if err != nil {
        inst.Retries++
        _ = w.Store.UpdateInstance(inst)
        if inst.Retries >= 3 {
            _ = w.Store.InsertDeadLetter(stringOrJSON(ev), inst.Retries, err.Error())
            inst.Status = "failed"
            _ = w.Store.UpdateInstance(inst)
            return fmt.Errorf("acp_prompt failed after retries: %w", err)
        }
        time.AfterFunc(time.Duration(inst.Retries)*time.Second*5, func() {
            _ = w.Enqueue(map[string]interface{}{
                "event_id":     fmt.Sprintf("%s-retry-%d", eventID, inst.Retries),
                "type":         "turn_complete",
                "connection_id": connectionID,
                "session_id":   sessionID,
                "turn_id":      turnID,
                "payload":      ev["payload"],
            })
        })
        return fmt.Errorf("acp_prompt transient error: %w", err)
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
