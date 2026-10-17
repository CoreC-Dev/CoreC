package executor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/hub/route"
)

// --- helpers ---

// backupTestConfig writes content to a config file under dir and returns its
// path.
func backupTestConfig(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	return p
}

// failingTransportEngine is a mock engine whose AddTransport always fails,
// used to prove the auto-snapshot never happens on the ApplyConfig failure
// path (a failed change must not produce or touch backups).
type failingTransportEngine struct {
	mockEngineForExecutor
}

func (m *failingTransportEngine) AddTransport(core.TransportConfig) error {
	return errors.New("transport apply failed")
}

// --- auto-snapshot on Reload ---

// TestReloadAutoSnapshot_CreatesBackupBeforePersist proves the core flow:
// a payload reload (dashboard PUT /configs) snapshots the on-disk config
// BEFORE overwriting it, with 0o600 permissions, and the persisted file
// afterwards holds the new payload.
func TestReloadAutoSnapshot_CreatesBackupBeforePersist(t *testing.T) {
	dir := t.TempDir()
	original := "global:\n  log-level: info\n"
	cfgPath := backupTestConfig(t, dir, original)
	eng := &mockEngineForExecutor{}
	Init(eng, &core.Config{}, cfgPath)

	newPayload := "global:\n  log-level: debug\n"
	if err := Reload("", newPayload); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	// Backup set now holds exactly one file, with the pre-change content.
	backups, err := ListConfigBackups()
	if err != nil {
		t.Fatalf("ListConfigBackups failed: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected 1 backup after reload, got %d", len(backups))
	}
	b := backups[0]
	if !strings.HasSuffix(b.Name, ".yaml") {
		t.Errorf("backup name %q should end in .yaml", b.Name)
	}
	if b.Timestamp.IsZero() {
		t.Errorf("backup timestamp should not be zero")
	}
	if b.Size != int64(len(original)) {
		t.Errorf("backup size = %d, want %d", b.Size, len(original))
	}

	// Metadata matches the file on disk: content, permissions, size.
	backupPath := filepath.Join(dir, "backups", b.Name)
	data, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("failed to read backup file: %v", err)
	}
	if string(data) != original {
		t.Errorf("backup content = %q, want the pre-change config %q", string(data), original)
	}
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("failed to stat backup file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("backup permissions = %o, want 600 (config contains credentials)", perm)
	}
	if info.Size() != b.Size {
		t.Errorf("listed size %d != on-disk size %d", b.Size, info.Size())
	}

	// The active file was replaced by the new payload.
	active, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("failed to read active config: %v", err)
	}
	if string(active) != newPayload {
		t.Errorf("active config after reload = %q, want %q", string(active), newPayload)
	}
	// And the in-memory config followed.
	if cfg := CurrentConfig(); cfg == nil || cfg.Global.LogLevel != "debug" {
		t.Errorf("current config log-level after reload = %+v, want debug", cfg)
	}
}

// TestReloadAutoSnapshot_NoDiskFile proves the snapshot is skipped when the
// active config file does not exist yet (first persist has nothing to back
// up) and that this is not an error.
func TestReloadAutoSnapshot_NoDiskFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml") // never written
	eng := &mockEngineForExecutor{}
	Init(eng, &core.Config{}, cfgPath)

	if err := Reload("", "global:\n  log-level: debug\n"); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	backups, err := ListConfigBackups()
	if err != nil {
		t.Fatalf("ListConfigBackups failed: %v", err)
	}
	if len(backups) != 0 {
		t.Errorf("expected no backups when no prior on-disk file existed, got %d", len(backups))
	}
}

// TestReloadAutoSnapshot_NoConfigPath proves the empty-config startup story:
// reloads still work (in-memory only), and every backup entry point returns
// a clear route.ErrNoConfigPath error instead of guessing a location.
func TestReloadAutoSnapshot_NoConfigPath(t *testing.T) {
	eng := &mockEngineForExecutor{}
	Init(eng, &core.Config{}, "")

	if err := Reload("", "global:\n  log-level: debug\n"); err != nil {
		t.Fatalf("Reload without config path should still apply, got: %v", err)
	}

	if _, err := ListConfigBackups(); !errors.Is(err, route.ErrNoConfigPath) {
		t.Errorf("ListConfigBackups error = %v, want ErrNoConfigPath", err)
	}
	if _, err := GetConfigBackup("1234567890123456789.yaml"); !errors.Is(err, route.ErrNoConfigPath) {
		t.Errorf("GetConfigBackup error = %v, want ErrNoConfigPath", err)
	}
	if _, err := CreateConfigBackup(); !errors.Is(err, route.ErrNoConfigPath) {
		t.Errorf("CreateConfigBackup error = %v, want ErrNoConfigPath", err)
	}
	if err := RestoreConfigBackup("1234567890123456789.yaml"); !errors.Is(err, route.ErrNoConfigPath) {
		t.Errorf("RestoreConfigBackup error = %v, want ErrNoConfigPath", err)
	}
}

// TestReloadAutoSnapshot_RetentionLimit proves at most configBackupMax
// backups are retained and the OLDEST are evicted.
func TestReloadAutoSnapshot_RetentionLimit(t *testing.T) {
	dir := t.TempDir()
	cfgPath := backupTestConfig(t, dir, "global:\n  log-level: info\n")
	eng := &mockEngineForExecutor{}
	Init(eng, &core.Config{}, cfgPath)

	const reloads = configBackupMax + 2 // 12 reloads → 12 snapshots
	var namesAfterFirst, namesAfterSecond []route.ConfigBackupInfo
	for i := 0; i < reloads; i++ {
		if err := Reload("", "global:\n  log-level: debug\n"); err != nil {
			t.Fatalf("reload %d failed: %v", i+1, err)
		}
		backups, err := ListConfigBackups()
		if err != nil {
			t.Fatalf("list after reload %d failed: %v", i+1, err)
		}
		switch i {
		case 0:
			namesAfterFirst = append(namesAfterFirst, backups...)
		case 1:
			namesAfterSecond = append(namesAfterSecond, backups...)
		}
	}

	final, err := ListConfigBackups()
	if err != nil {
		t.Fatalf("final list failed: %v", err)
	}
	if len(final) != configBackupMax {
		t.Fatalf("expected %d retained backups, got %d", configBackupMax, len(final))
	}

	// The first (and oldest of the first two) snapshots must have been evicted.
	for _, old := range append(namesAfterFirst, namesAfterSecond[0]) {
		found := false
		for _, b := range final {
			if b.Name == old.Name {
				found = true
				break
			}
		}
		if found {
			t.Errorf("oldest backup %q should have been evicted (retention=%d)", old.Name, configBackupMax)
		}
	}
	// Newest first ordering.
	for i := 1; i < len(final); i++ {
		if final[i-1].Timestamp.Before(final[i].Timestamp) {
			t.Errorf("backups not sorted newest-first: [%d]=%v < [%d]=%v", i-1, final[i-1].Timestamp, i, final[i].Timestamp)
		}
	}
}

// TestReloadAutoSnapshot_ValidationFailure proves an invalid payload never
// creates a backup nor touches the active file (fail before apply).
func TestReloadAutoSnapshot_ValidationFailure(t *testing.T) {
	dir := t.TempDir()
	original := "global:\n  log-level: info\n"
	cfgPath := backupTestConfig(t, dir, original)
	eng := &mockEngineForExecutor{}
	Init(eng, &core.Config{}, cfgPath)

	// api.secret shorter than 8 chars with listen set → hard validation error.
	invalid := "global:\n  api:\n    listen: 0.0.0.0:9090\n    secret: short\n"
	if err := Reload("", invalid); err == nil {
		t.Fatal("expected reload with invalid payload to fail")
	}

	backups, err := ListConfigBackups()
	if err != nil {
		t.Fatalf("ListConfigBackups failed: %v", err)
	}
	if len(backups) != 0 {
		t.Errorf("validation failure must not create backups, got %d", len(backups))
	}
	active, _ := os.ReadFile(cfgPath)
	if string(active) != original {
		t.Errorf("active config changed on failed reload: %q", string(active))
	}
}

// TestReloadAutoSnapshot_ApplyFailure proves the snapshot happens only after
// a successful apply: when ApplyConfig fails, no backup is written and the
// active file is left alone.
func TestReloadAutoSnapshot_ApplyFailure(t *testing.T) {
	dir := t.TempDir()
	original := "global:\n  log-level: info\n"
	cfgPath := backupTestConfig(t, dir, original)
	eng := &failingTransportEngine{}
	Init(eng, &core.Config{}, cfgPath)

	valid := "global:\n  log-level: debug\ntransports:\n  - name: t1\n    type: mqtt\n"
	err := Reload("", valid)
	if err == nil {
		t.Fatal("expected reload to fail when ApplyConfig fails")
	}
	if !eng.suspended || !eng.resumed {
		t.Error("engine should still be suspended/resumed around the failed apply")
	}

	backups, lerr := ListConfigBackups()
	if lerr != nil {
		t.Fatalf("ListConfigBackups failed: %v", lerr)
	}
	if len(backups) != 0 {
		t.Errorf("apply failure must not create backups, got %d", len(backups))
	}
	active, _ := os.ReadFile(cfgPath)
	if string(active) != original {
		t.Errorf("active config changed on failed apply: %q", string(active))
	}
}

// --- GetConfigBackup: content + strict name validation (anti-traversal) ---

// TestGetConfigBackup_Content proves the backup reader returns the exact
// stored content.
func TestGetConfigBackup_Content(t *testing.T) {
	dir := t.TempDir()
	original := "global:\n  log-level: info\n"
	cfgPath := backupTestConfig(t, dir, original)
	eng := &mockEngineForExecutor{}
	Init(eng, &core.Config{}, cfgPath)

	if err := Reload("", "global:\n  log-level: debug\n"); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	backups, err := ListConfigBackups()
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected exactly 1 backup, got %d (err=%v)", len(backups), err)
	}
	content, err := GetConfigBackup(backups[0].Name)
	if err != nil {
		t.Fatalf("GetConfigBackup failed: %v", err)
	}
	if content != original {
		t.Errorf("backup content = %q, want %q", content, original)
	}
}

// TestGetConfigBackup_NameValidation proves only strict generated names are
// accepted: traversal fragments, absolute paths, extensions, and empty names
// are rejected with ErrInvalidBackupName BEFORE any path is joined, so the
// reader can never be aimed outside the backup directory.
func TestGetConfigBackup_NameValidation(t *testing.T) {
	dir := t.TempDir()
	cfgPath := backupTestConfig(t, dir, "global:\n  log-level: info\n")
	Init(&mockEngineForExecutor{}, &core.Config{}, cfgPath)

	for _, name := range []string{
		"../../../etc/passwd",
		"../../outside.yaml",
		"/etc/passwd",
		"sub/dir/123.yaml",
		"123.yaml.bak",
		"abc.yaml",
		"12.YAML",
		"-123.yaml",
		"",
		// 19 nines overflow int64 — can never be a generated unix-nano stamp.
		strings.Repeat("9", 19) + ".yaml",
		strings.Repeat("9", 40) + ".yaml",
	} {
		if _, err := GetConfigBackup(name); !errors.Is(err, route.ErrInvalidBackupName) {
			t.Errorf("GetConfigBackup(%q) error = %v, want ErrInvalidBackupName", name, err)
		}
	}

	// A well-formed name that simply does not exist is a not-found error.
	if _, err := GetConfigBackup("1234567890123456789.yaml"); !errors.Is(err, route.ErrBackupNotFound) {
		t.Errorf("GetConfigBackup(missing) error = %v, want ErrBackupNotFound", err)
	}
}

// --- RestoreConfigBackup ---

// TestRestoreConfigBackup_FullChain proves restore runs the complete
// validate → ApplyConfig → persist chain: the current config returns to the
// backed-up state, the active file is rewritten with the backup content, and
// the pre-restore state is itself snapshotted (a restore can be undone).
func TestRestoreConfigBackup_FullChain(t *testing.T) {
	dir := t.TempDir()
	original := "global:\n  log-level: info\n"
	cfgPath := backupTestConfig(t, dir, original)
	eng := &mockEngineForExecutor{}
	Init(eng, &core.Config{}, cfgPath)

	newPayload := "global:\n  log-level: warn\n"
	if err := Reload("", newPayload); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	backups, err := ListConfigBackups()
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected 1 backup, got %d (err=%v)", len(backups), err)
	}

	if err := RestoreConfigBackup(backups[0].Name); err != nil {
		t.Fatalf("RestoreConfigBackup failed: %v", err)
	}

	// In-memory config rolled back.
	if cfg := CurrentConfig(); cfg == nil || cfg.Global.LogLevel != "info" {
		t.Errorf("log-level after restore = %+v, want info", cfg)
	}
	// On-disk config rolled back.
	active, _ := os.ReadFile(cfgPath)
	if string(active) != original {
		t.Errorf("active config after restore = %q, want %q", string(active), original)
	}
	// The restore itself snapshotted the replaced (pre-restore) state.
	after, err := ListConfigBackups()
	if err != nil {
		t.Fatalf("ListConfigBackups after restore failed: %v", err)
	}
	if len(after) != 2 {
		t.Fatalf("expected 2 backups after restore (original + pre-restore snapshot), got %d", len(after))
	}
	preRestore, err := GetConfigBackup(after[0].Name)
	if err != nil {
		t.Fatalf("GetConfigBackup(newest) failed: %v", err)
	}
	if preRestore != newPayload {
		t.Errorf("newest backup after restore = %q, want the pre-restore payload %q", preRestore, newPayload)
	}
}

// TestRestoreConfigBackup_InvalidContent proves a backup whose content no
// longer validates fails with ErrBackupInvalid BEFORE anything is applied:
// neither the in-memory config nor the on-disk file changes.
func TestRestoreConfigBackup_InvalidContent(t *testing.T) {
	dir := t.TempDir()
	current := "global:\n  log-level: info\n"
	cfgPath := backupTestConfig(t, dir, current)
	eng := &mockEngineForExecutor{}
	Init(eng, &core.Config{
		Global: core.GlobalConfig{LogLevel: "info"},
	}, cfgPath)

	// Hand-craft backup files inside the backup set with plausible names.
	backupsDir := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backupsDir, 0o700); err != nil {
		t.Fatalf("failed to create backups dir: %v", err)
	}
	invalidValidate := "1000000000000000000.yaml" // fails validation (secret < 8 chars)
	if err := os.WriteFile(filepath.Join(backupsDir, invalidValidate),
		[]byte("global:\n  api:\n    listen: 0.0.0.0:9090\n    secret: short\n"), 0o600); err != nil {
		t.Fatalf("failed to write invalid backup: %v", err)
	}
	invalidParse := "1000000000000000001.yaml" // fails parsing outright
	if err := os.WriteFile(filepath.Join(backupsDir, invalidParse),
		[]byte("global:\n  log-level: [unclosed"), 0o600); err != nil {
		t.Fatalf("failed to write unparsable backup: %v", err)
	}

	for _, name := range []string{invalidValidate, invalidParse} {
		err := RestoreConfigBackup(name)
		if !errors.Is(err, route.ErrBackupInvalid) {
			t.Errorf("RestoreConfigBackup(%s) error = %v, want ErrBackupInvalid", name, err)
		}
		if cfg := CurrentConfig(); cfg == nil || cfg.Global.LogLevel != "info" {
			t.Errorf("current config changed by failed restore (%s): %+v", name, cfg)
		}
		active, _ := os.ReadFile(cfgPath)
		if string(active) != current {
			t.Errorf("active config changed by failed restore (%s): %q", name, string(active))
		}
	}
}

// TestRestoreConfigBackup_NotFoundAndInvalidName proves the restore error
// classification for unknown and malformed names.
func TestRestoreConfigBackup_NotFoundAndInvalidName(t *testing.T) {
	dir := t.TempDir()
	cfgPath := backupTestConfig(t, dir, "global:\n  log-level: info\n")
	Init(&mockEngineForExecutor{}, &core.Config{}, cfgPath)

	if err := RestoreConfigBackup("1234567890123456789.yaml"); !errors.Is(err, route.ErrBackupNotFound) {
		t.Errorf("restore(missing) error = %v, want ErrBackupNotFound", err)
	}
	if err := RestoreConfigBackup("../escape.yaml"); !errors.Is(err, route.ErrInvalidBackupName) {
		t.Errorf("restore(traversal) error = %v, want ErrInvalidBackupName", err)
	}
}

// --- CreateConfigBackup (manual snapshot) ---

// TestCreateConfigBackup_Manual proves the manual snapshot endpoint backing:
// it copies the current on-disk file into the backup set and reports the
// name, or classifies failures precisely.
func TestCreateConfigBackup_Manual(t *testing.T) {
	dir := t.TempDir()
	current := "global:\n  log-level: info\n"
	cfgPath := backupTestConfig(t, dir, current)
	Init(&mockEngineForExecutor{}, &core.Config{}, cfgPath)

	name, err := CreateConfigBackup()
	if err != nil {
		t.Fatalf("CreateConfigBackup failed: %v", err)
	}
	content, err := GetConfigBackup(name)
	if err != nil {
		t.Fatalf("GetConfigBackup(%s) failed: %v", name, err)
	}
	if content != current {
		t.Errorf("manual backup content = %q, want %q", content, current)
	}

	// With a config path set but no file on disk, there is nothing to
	// snapshot — a precise error, not a guess.
	emptyDir := t.TempDir()
	Init(&mockEngineForExecutor{}, &core.Config{}, filepath.Join(emptyDir, "config.yaml"))
	if _, err := CreateConfigBackup(); !errors.Is(err, route.ErrConfigFileMissing) {
		t.Errorf("CreateConfigBackup(no disk file) error = %v, want ErrConfigFileMissing", err)
	}
}

// TestListConfigBackups_Empty proves the list endpoint returns an empty (not
// nil, not error) set when the backup directory does not exist yet.
func TestListConfigBackups_Empty(t *testing.T) {
	dir := t.TempDir()
	cfgPath := backupTestConfig(t, dir, "global:\n  log-level: info\n")
	Init(&mockEngineForExecutor{}, &core.Config{}, cfgPath)

	backups, err := ListConfigBackups()
	if err != nil {
		t.Fatalf("ListConfigBackups failed: %v", err)
	}
	if backups == nil {
		t.Fatal("expected empty slice, got nil")
	}
	if len(backups) != 0 {
		t.Errorf("expected 0 backups, got %d", len(backups))
	}
}
