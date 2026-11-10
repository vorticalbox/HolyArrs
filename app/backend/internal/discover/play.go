package discover

import (
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
)

// browserPlay is the containers a browser can play without a transcoder.
var browserPlay = map[string]string{
	".mp4":  "video/mp4",
	".m4v":  "video/mp4",
	".webm": "video/webm",
}

// playFile streams a title's present main file, or reports whether it can
// play when probe=1. The path must stay inside the library folder.
func (h *TitleHandlers) playFile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		webserver.Fail(w, http.StatusNotFound, "no such title")
		return
	}
	var one int
	err = h.Store.Read().QueryRowContext(r.Context(), "SELECT 1 FROM titles WHERE id = ?", id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		webserver.Fail(w, http.StatusNotFound, "no such title")
		return
	}
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	var rel, root string
	err = h.Store.Read().QueryRowContext(r.Context(), `
		SELECT f.relative_path, lf.path
		FROM files f
		JOIN versions v ON v.id = f.version_id
		JOIN library_folders lf ON lf.id = v.library_folder_id
		WHERE v.title_id = ? AND f.role = 'main' AND f.state = 'present'
		ORDER BY f.size DESC, f.id
		LIMIT 1`, id).Scan(&rel, &root)
	probe := r.URL.Query().Get("probe") == "1"
	if errors.Is(err, sql.ErrNoRows) {
		if probe {
			webserver.WriteJSON(w, http.StatusOK, map[string]bool{"playable": false})
			return
		}
		webserver.Fail(w, http.StatusNotFound, "no file to play")
		return
	}
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	full, ok := insideLibrary(root, rel)
	ext := strings.ToLower(filepath.Ext(full))
	contentType, playable := browserPlay[ext]
	if !ok || !playable {
		if probe {
			webserver.WriteJSON(w, http.StatusOK, map[string]bool{"playable": false})
			return
		}
		if !ok {
			webserver.Fail(w, http.StatusNotFound, "no file to play")
			return
		}
		webserver.Fail(w, http.StatusUnsupportedMediaType, "this file cannot play in the browser")
		return
	}
	if probe {
		webserver.WriteJSON(w, http.StatusOK, map[string]bool{"playable": true})
		return
	}
	f, err := os.Open(full)
	if err != nil {
		webserver.Fail(w, http.StatusNotFound, "no file to play")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private")
	http.ServeContent(w, r, filepath.Base(full), time.Time{}, f)
}

// insideLibrary is the file path when rel is a regular file inside root.
func insideLibrary(root, rel string) (string, bool) {
	root = filepath.Clean(root)
	rel = filepath.Clean(filepath.FromSlash(rel))
	if rel == "." || !filepath.IsLocal(rel) {
		return "", false
	}
	full := filepath.Join(root, rel)
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", false
	}
	stay, err := filepath.Rel(absRoot, absFull)
	if err != nil || !filepath.IsLocal(stay) {
		return "", false
	}
	info, err := os.Lstat(absFull)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return absFull, true
}
