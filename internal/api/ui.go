package api

import (
    "embed"
    "html/template"
    "net/http"

    "rtkron/internal/store"
)

//go:embed templates/*
var templatesFS embed.FS

var tmpl = template.Must(template.ParseFS(templatesFS, "templates/*.html"))

type UIHandler struct {
    store *store.SQLiteStore
}

func RegisterUIHandlers(mux *http.ServeMux, s *store.SQLiteStore) {
    h := &UIHandler{store: s}
    mux.HandleFunc("/", h.handleDashboard)
}

func (h *UIHandler) handleDashboard(w http.ResponseWriter, r *http.Request) {
    if r.URL.Path != "/" {
        http.NotFound(w, r)
        return
    }

    // Example: fetch data for the dashboard if needed
    // workflows, err := h.store.ListWorkflows()

    data := map[string]interface{}{
        "Title": "RTKron Control Center",
    }
    
    if err := tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
        http.Error(w, "Template rendering error", http.StatusInternalServerError)
    }
}
