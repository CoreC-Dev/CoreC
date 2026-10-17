package route

import (
	"errors"
	"time"
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
