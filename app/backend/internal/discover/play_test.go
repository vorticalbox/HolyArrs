package discover

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/accounts"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

func TestPlayFileStreamsMp4AndRefusesTheRest(t *testing.T) {
	s, err := database.Open(t.TempDir(), database.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	root := t.TempDir()
	body := []byte("hello-movie-file")
	if err := os.MkdirAll(filepath.Join(root, "Wolf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Wolf", "Wolf.mp4"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	mkvRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mkvRoot, "Wolf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mkvRoot, "Wolf", "Wolf.mkv"), []byte("mkv"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.mp4")
	if err := os.WriteFile(outside, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	mp4 := seedPlayFile(t, s, root, "Wolf/Wolf.mp4")
	mkv := seedPlayFile(t, s, mkvRoot, "Wolf/Wolf.mkv")
	escaped := seedPlayFile(t, s, root+"-escape", "../secret.mp4")
	h := &TitleHandlers{Store: s}

	rec := playReq(t, h, mp4, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("mp4 status %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("ranges %q", rec.Header().Get("Accept-Ranges"))
	}
	if got := rec.Body.String(); got != string(body) {
		t.Fatalf("body %q", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/titles/"+itoa(mp4)+"/file", nil)
	req.SetPathValue("id", itoa(mp4))
	req.Header.Set("Range", "bytes=0-4")
	rec = httptest.NewRecorder()
	h.playFile(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("range status %d %s", rec.Code, rec.Body)
	}
	if got, err := io.ReadAll(rec.Body); err != nil || string(got) != "hello" {
		t.Fatalf("range body %q %v", got, err)
	}

	rec = playReq(t, h, mp4, "?probe=1")
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"playable\":true}\n" {
		t.Fatalf("probe %d %s", rec.Code, rec.Body)
	}

	rec = playReq(t, h, mkv, "")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("mkv %d %s", rec.Code, rec.Body)
	}
	rec = playReq(t, h, mkv, "?probe=1")
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"playable\":false}\n" {
		t.Fatalf("mkv probe %d %s", rec.Code, rec.Body)
	}

	rec = playReq(t, h, escaped, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("escape %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal(err)
	}

	rec = playReq(t, h, 999999, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing %d", rec.Code)
	}
}

func TestPlayFileUnsigned(t *testing.T) {
	if accounts.RoutePerms["GET /api/v1/titles/{id}/file"] != webserver.NeedSignedIn {
		t.Fatalf("perm %q", accounts.RoutePerms["GET /api/v1/titles/{id}/file"])
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/titles/{id}/file", func(http.ResponseWriter, *http.Request) {
		t.Fatal("unsigned request reached the file")
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/titles/1/file", nil)
	(&accounts.Auth{}).Middleware(mux).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
}

func playReq(t *testing.T, h *TitleHandlers, id int64, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/titles/"+itoa(id)+"/file"+query, nil)
	req.SetPathValue("id", itoa(id))
	rec := httptest.NewRecorder()
	h.playFile(rec, req)
	return rec
}

func seedPlayFile(t *testing.T, s *database.Store, root, rel string) int64 {
	t.Helper()
	var titleID int64
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		folder := &mediafiles.LibraryFolder{Path: root, Kind: "movie", Label: rel, Health: "ok", SameDiskAs: []int64{}}
		folderID, err := database.Insert(tx, "library_folders", folder, database.System)
		if err != nil {
			return err
		}
		rules := &mediafiles.PickRules{Name: rel, Kinds: []string{"movie"}, Revision: 1, Body: mediafiles.PickRulesBody{}}
		rulesID, err := database.Insert(tx, "pick_rules", rules, database.System)
		if err != nil {
			return err
		}
		title := mediafiles.Title{Kind: "movie", Name: rel, SortName: rel, Aliases: []string{}, SourceIDs: map[string]string{}, MetadataSource: "local", NewPartsPolicy: "all", ReleaseGate: "released", GateDriver: "none", WhyAdded: "by_hand", Artwork: []string{}, State: "active", Status: "released", MusicTypes: []string{}}
		if _, err = database.Insert(tx, "titles", &title, database.System); err != nil {
			return err
		}
		titleID = title.ID
		item := &mediafiles.Item{TitleID: title.ID, Kind: "movie", Numbers: mediafiles.Numbers{}, KeepUpToDate: true, SourceIDs: map[string]string{}}
		if _, err = database.Insert(tx, "items", item, database.System); err != nil {
			return err
		}
		version := mediafiles.Version{TitleID: title.ID, Label: "HD", PickRulesID: rulesID, LibraryFolderID: folderID, FolderName: "Wolf", KeepUpToDate: true, IsMain: true, AudioLanguages: []string{"en"}}
		if _, err = database.Insert(tx, "versions", &version, database.System); err != nil {
			return err
		}
		file := mediafiles.File{VersionID: version.ID, SlotIDs: []int64{}, RelativePath: rel, Size: 16, AddedAt: time.Now().UTC(), Quality: mediafiles.Quality{}, Languages: []string{}, MediaInfo: map[string]any{}, Origin: "adopted", PlacedBy: "none", Role: "main", State: "present"}
		_, err = database.Insert(tx, "files", &file, database.System)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return titleID
}
