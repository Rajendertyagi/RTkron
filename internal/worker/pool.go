package worker

import (
    "context"
    "log"
    "time"

    "rtkron/internal/codeg"
    "rtkron/internal/config"
    "rtkron/internal/store"
)

// WorkerPool ties together store and client to process events.
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
    ctx, cancel := context.WithCancel(ctx)
    wp := &WorkerPool{
        Store:   s,
        Client:  c,
        Config:  cfg,
        Events:  make(chan interface{}, cfg.EventQueueSize),
        workers: cfg.WorkerCount,
        ctx:     ctx,
        cancel:  cancel,
    }
    return wp
}

func (w *WorkerPool) Start() {
    for i := 0; i < w.workers; i++ {
        go w.workerLoop(i + 1)
    }
    log.Printf("started %d workers", w.workers)
}

func (w *WorkerPool) Stop() {
    w.cancel()
}

func (w *WorkerPool) Enqueue(evt interface{}) bool {
    select {
    case w.Events <- evt:
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
            log.Printf("worker-%d stopping", id)
            return
        case e := <-w.Events:
            // process with timeout
            ctx, cancel := context.WithTimeout(w.ctx, 30*time.Second)
            w.processEvent(ctx, e)
            cancel()
        }
    }
}

func (w *WorkerPool) processEvent(ctx context.Context, evt interface{}) {
    // Minimal example: expect evt to be map[string]interface{} or a typed struct
    // Implement actual processing: idempotency, auto-approve, chain dispatch, audit, deadletter
    log.Printf("processing event: %+v", evt)
    // Example: mark done in store if possible
    _ = w.Store // use store for DB ops
    _ = w.Client
    // TODO: implement real logic
}
