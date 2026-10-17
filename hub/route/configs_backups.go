package route

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// ConfigBackupInfo is the wire shape for one persisted configuration backup
// file, as returned by GET /configs/backups. It deliberately carries only
// metadata — never the backup content, which contains secrets and is served
// solely through GET /configs/backups/{name} (mirroring the security posture
// of GET /configs/raw).
type ConfigBackupInfo struct {
	Name      string    `json:"name"`
	Timestamp time.Time `json:"timestamp"`
	Size      int64     `json:"size"`
}

// Sentinel errors classifying config-backup failures. The executor (the only
// implementer of the backup function variables) wraps them into returned
// errors; handlers map each one onto a precise HTTP status via errors.Is.
// They live here — not in hub/executor — because the wiring direction is
// executor → route (route must not import executor, or the assignment pattern
// used for ReloadFunc and friends would become an import cycle).
var (
	// ErrNoConfigPath is returned when the core started without a persisted
	// config file path (empty-config startup), so there is no on-disk file
	// to back up, list, or restore. Handlers surface it as 503.
	ErrNoConfigPath = errors.New("no active config path; config backups unavailable")
	// ErrConfigFileMissing is returned when a config path is set but the
	// file does not exist on disk yet (nothing to snapshot manually).
	// Handlers surface it as 404.
	ErrConfigFileMissing = errors.New("active config file not found on disk")
	// ErrInvalidBackupName is returned when a requested backup name is not a
	// strict "<unix-nano>.yaml" file name — including any path separator or
	// traversal fragment. The check happens before any filesystem path is
	// joined, so callers can never read outside the backup directory.
	// Handlers surface it as 400.
	ErrInvalidBackupName = errors.New("invalid backup name")
	// ErrBackupNotFound is returned when a well-formed backup name does not
	// exist in the backup set. Handlers surface it as 404.
	ErrBackupNotFound = errors.New("config backup not found")
	// ErrBackupInvalid wraps the parse/validation error of a backup whose
	// content no longer passes validation, raised before anything is
	// applied, so the active configuration is left untouched. Handlers
	// surface it as 400 with the underlying message.
	ErrBackupInvalid = errors.New("config backup failed validation")
)

// getConfigBackups handles GET /configs/backups. It returns metadata for
// the retained server-side config backups (newest first): name, timestamp,
// size. The response deliberately carries metadata only — backup content
// contains the same secrets as the active config and is served solely
// through GET /configs/backups/{name}.
//
// When the core started without a persisted config file path (empty-config
// startup), there is nothing to back up and the endpoint answers 503 with a
// clear message instead of an empty, misleading list.
func getConfigBackups(w http.ResponseWriter, r *http.Request) {
	if ListConfigBackupsFunc == nil {
		renderInternalError(w, r, fmt.Errorf("config backups endpoint not wired"))
		return
	}
	backups, err := ListConfigBackupsFunc()
	if err != nil {
		if errors.Is(err, ErrNoConfigPath) {
			renderError(w, r, http.StatusServiceUnavailable, err.Error())
			return
		}
		slog.Error("failed to list config backups",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"error", err)
		renderInternalError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, map[string]any{"backups": backups})
}

// getConfigBackupContent handles GET /configs/backups/{name}. It returns
// the raw YAML content of one backup, mirroring the security posture of
// GET /configs/raw: the content includes secrets in plaintext (api.secret,
// driver/transport credentials) and is visible only to authenticated
// callers — the same operators who can already fetch the full active
// config. This adds no new plaintext exposure surface.
//
// The name must be an exact entry of the backup set (strict
// "<unix-nano>.yaml"); traversal fragments and arbitrary paths are rejected
// by the executor's name validation before any file is opened.
func getConfigBackupContent(w http.ResponseWriter, r *http.Request) {
	if GetConfigBackupFunc == nil {
		renderInternalError(w, r, fmt.Errorf("config backup endpoint not wired"))
		return
	}
	name := chi.URLParam(r, "name")
	yamlText, err := GetConfigBackupFunc(name)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidBackupName):
			// Rejected before any filesystem path was joined — likely a
			// traversal or fuzzing attempt; keep it visible in the logs.
			slog.Warn("config backup request rejected: invalid backup name",
				"method", r.Method,
				"path", r.URL.Path,
				"remote", r.RemoteAddr,
				"name", name)
			renderError(w, r, http.StatusBadRequest, err.Error())
		case errors.Is(err, ErrBackupNotFound):
			renderError(w, r, http.StatusNotFound, err.Error())
		case errors.Is(err, ErrNoConfigPath):
			renderError(w, r, http.StatusServiceUnavailable, err.Error())
		default:
			slog.Error("failed to read config backup",
				"method", r.Method,
				"path", r.URL.Path,
				"remote", r.RemoteAddr,
				"error", err)
			renderInternalError(w, r, err)
		}
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, yamlText); err != nil {
		slog.Error("failed to write config backup response", "error", err)
	}
}

// createConfigBackup handles POST /configs/backups. It takes a manual
// snapshot of the current on-disk config file (e.g. right before an
// operator hand-edits it or starts a risky batch of Dashboard changes) and
// returns 201 with the new backup's file name.
func createConfigBackup(w http.ResponseWriter, r *http.Request) {
	if CreateConfigBackupFunc == nil {
		renderInternalError(w, r, fmt.Errorf("config backup creation endpoint not wired"))
		return
	}
	name, err := CreateConfigBackupFunc()
	if err != nil {
		switch {
		case errors.Is(err, ErrNoConfigPath):
			renderError(w, r, http.StatusServiceUnavailable, err.Error())
		case errors.Is(err, ErrConfigFileMissing):
			renderError(w, r, http.StatusNotFound, err.Error())
		default:
			slog.Error("config backup creation failed",
				"method", r.Method,
				"path", r.URL.Path,
				"remote", r.RemoteAddr,
				"error", err)
			renderInternalError(w, r, err)
		}
		return
	}
	slog.Info("config backup created manually",
		"method", r.Method,
		"action", "backup",
		"path", r.URL.Path,
		"name", name,
		"remote", r.RemoteAddr)
	render(w, r, http.StatusCreated, map[string]string{"name": name})
}

// restoreConfigBackup handles POST /configs/backups/{name}/restore. The
// executor runs the backup content through the exact same chain as
// PUT /configs (parse → validate → ApplyConfig → persist) — a backup is
// never blindly copied over the active file. Validation failures therefore
// leave the running configuration completely untouched (400); apply
// failures after a successful validation surface as masked 500s like
// PUT /configs.
func restoreConfigBackup(w http.ResponseWriter, r *http.Request) {
	if RestoreConfigBackupFunc == nil {
		renderInternalError(w, r, fmt.Errorf("config backup restore endpoint not wired"))
		return
	}
	name := chi.URLParam(r, "name")
	if err := RestoreConfigBackupFunc(name); err != nil {
		switch {
		case errors.Is(err, ErrInvalidBackupName):
			slog.Warn("config backup restore rejected: invalid backup name",
				"method", r.Method,
				"path", r.URL.Path,
				"remote", r.RemoteAddr,
				"name", name)
			renderError(w, r, http.StatusBadRequest, err.Error())
		case errors.Is(err, ErrBackupNotFound):
			renderError(w, r, http.StatusNotFound, err.Error())
		case errors.Is(err, ErrNoConfigPath):
			renderError(w, r, http.StatusServiceUnavailable, err.Error())
		case errors.Is(err, ErrBackupInvalid):
			// Validation failure: nothing was applied. The message is safe
			// to return verbatim (same class as POST /configs/validate
			// errors, which are already exposed to the caller).
			slog.Info("config backup restore failed validation",
				"method", r.Method,
				"path", r.URL.Path,
				"name", name,
				"remote", r.RemoteAddr,
				"error", err)
			renderError(w, r, http.StatusBadRequest, err.Error())
		default:
			slog.Error("config backup restore failed",
				"method", r.Method,
				"path", r.URL.Path,
				"name", name,
				"remote", r.RemoteAddr,
				"error", err)
			renderInternalError(w, r, err)
		}
		return
	}
	slog.Info("config backup restored",
		"method", r.Method,
		"action", "restore",
		"path", r.URL.Path,
		"name", name,
		"remote", r.RemoteAddr)
	renderNoContent(w)
}
