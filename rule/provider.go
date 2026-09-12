package rule

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/CoreC-Dev/CoreC/core"
)

// FileProvider loads match-only rules from a YAML file and supports
// hot-reload at a configurable interval.
type FileProvider struct {
	name     string
	path     string
	interval time.Duration

	mu       sync.RWMutex
	matchers []exprNode // compiled match expressions
	count    int
	updated  time.Time
	stopCh   chan struct{}
}

// providerFile is the YAML schema for external rule files.
type providerFile struct {
	Rules []core.RuleConfig `yaml:"rules"`
}

// NewFileProvider creates a file-based rule provider.
// interval is the hot-reload interval; 0 means no auto-reload.
func NewFileProvider(name, path, intervalStr string) (*FileProvider, error) {
	interval, err := time.ParseDuration(intervalStr)
	if err != nil && intervalStr != "" {
		return nil, fmt.Errorf("invalid interval %q: %w", intervalStr, err)
	}

	p := &FileProvider{
		name:     name,
		path:     path,
		interval: interval,
		stopCh:   make(chan struct{}),
	}

	if err := p.load(); err != nil {
		return nil, fmt.Errorf("provider %s: %w", name, err)
	}

	if interval > 0 {
		go p.reloadLoop()
	}

	return p, nil
}

// load reads and parses the rule file, compiling all match expressions.
func (p *FileProvider) load() error {
	data, err := os.ReadFile(p.path)
	if err != nil {
		return fmt.Errorf("read file %s: %w", p.path, err)
	}

	var pf providerFile
	if err := yaml.Unmarshal(data, &pf); err != nil {
		return fmt.Errorf("parse yaml %s: %w", p.path, err)
	}

	matchers := make([]exprNode, 0, len(pf.Rules))
	for _, rc := range pf.Rules {
		match := strings.TrimSpace(rc.Match)
		if strings.EqualFold(match, "ALL") {
			// ALL matches everything — use a sentinel node
			matchers = append(matchers, &allNode{})
			continue
		}
		node, err := compileExpr(match)
		if err != nil {
			slog.Warn("provider rule skipped", "provider", p.name, "rule", rc.Name, "error", err)
			continue
		}
		matchers = append(matchers, node)
	}

	p.mu.Lock()
	p.matchers = matchers
	p.count = len(matchers)
	p.updated = time.Now()
	p.mu.Unlock()

	slog.Info("rule provider loaded", "name", p.name, "path", p.path, "count", len(matchers))
	return nil
}

// reloadLoop periodically re-reads the file.
func (p *FileProvider) reloadLoop() {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := p.load(); err != nil {
				slog.Error("rule provider reload failed", "name", p.name, "error", err)
			}
		case <-p.stopCh:
			return
		}
	}
}

// Match returns true if any rule in the provider matches the DataPoint.
func (p *FileProvider) Match(point core.DataPoint) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, m := range p.matchers {
		if m.eval(point) {
			return true
		}
	}
	return false
}

func (p *FileProvider) Name() string { return p.name }
func (p *FileProvider) Count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.count
}

// Initial loads the provider data (already done in constructor, kept for interface compat).
func (p *FileProvider) Initial() error { return nil }

// Update forces a reload of the provider file.
func (p *FileProvider) Update() error { return p.load() }

// Close stops the reload goroutine.
func (p *FileProvider) Close() {
	close(p.stopCh)
}

// allNode matches everything.
type allNode struct{}

func (n *allNode) eval(_ core.DataPoint) bool { return true }
