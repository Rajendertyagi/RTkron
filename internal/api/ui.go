package api

import (
    "embed"
    "io/fs"
    "net/http"
)

//go:embed static/*
var staticFS embed.FS

// RegisterUIHandlers serves the forked gocron-ui dashboard (RTKron fork)
// directly from the root path "/".
func RegisterUIHandlers(mux *http.ServeMux) {
    sub, err := fs.Sub(staticFS, "static")
    if err != nil {
        panic(err)
    }
    mux.Handle("/", http.FileServer(http.FS(sub)))
}
