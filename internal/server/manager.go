// Package server supervises Minecraft server processes and manages their
// configuration.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"mcsman/internal/crypto"
	"mcsman/internal/db"
	"mcsman/internal/events"
	"mcsman/internal/fsutil"
	"mcsman/internal/java"
)

var (
	ErrNotFound = errors.New("server not found")
	ErrConflict = errors.New("conflict")
	ErrInvalid  = errors.New("invalid request")
)

var (
	nameRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	mcVersionRe = regexp.MustCompile(`^\d+\.\d+(\.\d+)?$`)
)

// Options configures a Manager.
type Options struct {
	DB      *db.DB
	Bus     *events.Bus
	Sealer  *crypto.Sealer // LabelRCON
	DataDir string
	Logger  *slog.Logger
	// Java resolves a Java major version to the path of its java binary.
	Java func(version string) (string, error)
	// Backoff overrides the crash restart delays (tests).
	Backoff []time.Duration
	// Provisioner validates versions and installs server JARs on create.
	// Nil skips provisioning (tests place server.jar themselves).
	Provisioner Provisioner
}

// Provisioner resolves Minecraft versions to their requirements and installs
// server software.
type Provisioner interface {
	// JavaFor returns the Java major version a Minecraft version needs;
	// known is false if the version does not exist.
	JavaFor(ctx context.Context, mcVersion string) (major int, known bool, err error)
	// InstallJar installs the server JAR for mcVersion as dir/server.jar.
	InstallJar(ctx context.Context, mcVersion, dir string) error
}

// Manager is the registry of server instances.
type Manager struct {
	opts Options
	deps *deps

	mu        sync.RWMutex
	instances map[string]*Instance
}

func NewManager(o Options) *Manager {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Backoff == nil {
		o.Backoff = defaultBackoff
	}
	return &Manager{
		opts:      o,
		deps:      &deps{db: o.DB, bus: o.Bus, log: o.Logger, java: o.Java, backoff: o.Backoff},
		instances: map[string]*Instance{},
	}
}

// ServersDir is where server directories live.
func (m *Manager) ServersDir() string { return filepath.Join(m.opts.DataDir, "servers") }

// Load registers every server in the DB. Any status left over from a previous
// daemon run is reset to stopped, since those processes are gone.
func (m *Manager) Load(ctx context.Context) error {
	servers, err := m.opts.DB.ListServers(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range servers {
		if s.Status != db.StatusStopped {
			if err := m.opts.DB.SetServerStatus(ctx, s.ID, db.StatusStopped); err != nil {
				return err
			}
			s.Status = db.StatusStopped
		}
		pw, err := m.opts.Sealer.Open(s.RCONPasswordEnc)
		if err != nil {
			return fmt.Errorf("server %s: decrypt rcon password (was jwt_secret changed?): %w", s.Name, err)
		}
		m.instances[s.ID] = newInstance(m.deps, s, pw)
	}
	return nil
}

// Get returns the instance with the given ID.
func (m *Manager) Get(id string) (*Instance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	inst, ok := m.instances[id]
	if !ok {
		return nil, ErrNotFound
	}
	return inst, nil
}

// Lookup finds an instance by ID or name.
func (m *Manager) Lookup(idOrName string) (*Instance, error) {
	if inst, err := m.Get(idOrName); err == nil {
		return inst, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, inst := range m.instances {
		if inst.srv.Name == idOrName {
			return inst, nil
		}
	}
	return nil, ErrNotFound
}

// List returns all servers with live status, sorted by name.
func (m *Manager) List() []db.Server {
	m.mu.RLock()
	out := make([]db.Server, 0, len(m.instances))
	for _, inst := range m.instances {
		out = append(out, inst.Server())
	}
	m.mu.RUnlock()
	slices.SortFunc(out, func(a, b db.Server) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// CreateParams are the inputs to Create.
type CreateParams struct {
	Name              string  `json:"name"`
	MCVersion         string  `json:"mc_version"`
	JavaVersion       string  `json:"java_version"`
	Port              int     `json:"port"`
	RCONPort          int     `json:"rcon_port"`
	MaxMemoryMB       int     `json:"max_memory_mb"`
	AutoRestart       *bool   `json:"auto_restart"`
	ConnectionAddress *string `json:"connection_address"`
	AcceptEULA        bool    `json:"accept_eula"`
}

// Create validates p, creates the server directory and registers the server.
func (m *Manager) Create(ctx context.Context, p CreateParams) (*db.Server, error) {
	if !p.AcceptEULA {
		return nil, fmt.Errorf("%w: you must accept the Minecraft EULA (https://aka.ms/MinecraftEULA)", ErrInvalid)
	}
	if !nameRe.MatchString(p.Name) {
		return nil, fmt.Errorf("%w: name must be 1-32 chars of a-z, 0-9, '-', '_'", ErrInvalid)
	}
	if !mcVersionRe.MatchString(p.MCVersion) {
		return nil, fmt.Errorf("%w: mc_version %q is not a version number", ErrInvalid, p.MCVersion)
	}
	if prov := m.opts.Provisioner; prov != nil {
		major, known, err := prov.JavaFor(ctx, p.MCVersion)
		if err != nil {
			return nil, fmt.Errorf("look up version %s: %w", p.MCVersion, err)
		}
		if !known {
			return nil, fmt.Errorf("%w: Paper has no release %q", ErrInvalid, p.MCVersion)
		}
		if p.JavaVersion == "" && major > 0 {
			p.JavaVersion = strconv.Itoa(major)
		}
	}
	if p.JavaVersion == "" {
		p.JavaVersion = strconv.Itoa(java.MajorFor(p.MCVersion))
	}
	if _, err := strconv.Atoi(p.JavaVersion); err != nil {
		return nil, fmt.Errorf("%w: java_version must be a major version number", ErrInvalid)
	}
	if p.Port == 0 {
		p.Port = 25565
	}
	if p.RCONPort == 0 {
		p.RCONPort = p.Port + 10
	}
	if p.MaxMemoryMB == 0 {
		p.MaxMemoryMB = 2048
	}
	if err := validateCommon(p.Port, p.RCONPort, p.MaxMemoryMB); err != nil {
		return nil, err
	}
	dir, err := fsutil.SafePath(m.ServersDir(), p.Name)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	pw, err := crypto.RandomToken(24)
	if err != nil {
		return nil, err
	}
	enc, err := m.opts.Sealer.Seal(pw)
	if err != nil {
		return nil, err
	}
	s := &db.Server{
		Name:              p.Name,
		Directory:         dir,
		MCVersion:         p.MCVersion,
		JavaVersion:       p.JavaVersion,
		Port:              p.Port,
		RCONPort:          p.RCONPort,
		RCONPasswordEnc:   enc,
		MaxMemoryMB:       p.MaxMemoryMB,
		AutoRestart:       p.AutoRestart == nil || *p.AutoRestart,
		ConnectionAddress: emptyToNil(p.ConnectionAddress),
	}

	if err := m.checkNameAndPorts(s); err != nil {
		return nil, err
	}

	_, statErr := os.Stat(dir)
	created := os.IsNotExist(statErr)
	cleanup := func() {
		if created {
			os.RemoveAll(dir)
		}
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	if err := fsutil.WriteFileAtomic(filepath.Join(dir, "eula.txt"),
		[]byte("# Accepted via mcsman\neula=true\n"), 0o640); err != nil {
		cleanup()
		return nil, err
	}
	// Download outside the registry lock; it can take a while.
	if prov := m.opts.Provisioner; prov != nil {
		if _, err := os.Stat(filepath.Join(dir, "server.jar")); os.IsNotExist(err) {
			if err := prov.InstallJar(ctx, s.MCVersion, dir); err != nil {
				cleanup()
				return nil, fmt.Errorf("install Paper %s: %w", s.MCVersion, err)
			}
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkPortsUnusedLocked("", s.Port, s.RCONPort); err != nil {
		cleanup()
		return nil, err
	}
	if err := m.opts.DB.CreateServer(ctx, s); err != nil {
		cleanup()
		if isUniqueErr(err) {
			return nil, fmt.Errorf("%w: a server with that name, directory or port already exists", ErrConflict)
		}
		return nil, err
	}
	m.instances[s.ID] = newInstance(m.deps, s, pw)
	return s, nil
}

// checkNameAndPorts fails fast on obvious conflicts before any download.
func (m *Manager) checkNameAndPorts(s *db.Server) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, inst := range m.instances {
		if inst.srv.Name == s.Name || inst.srv.Directory == s.Directory {
			return fmt.Errorf("%w: a server named %q already exists", ErrConflict, s.Name)
		}
	}
	return m.checkPortsUnusedLocked("", s.Port, s.RCONPort)
}

// Running returns instances whose status is running.
func (m *Manager) Running() []*Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*Instance
	for _, inst := range m.instances {
		if inst.Status() == db.StatusRunning {
			out = append(out, inst)
		}
	}
	return out
}

// All returns every instance.
func (m *Manager) All() []*Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Instance, 0, len(m.instances))
	for _, inst := range m.instances {
		out = append(out, inst)
	}
	return out
}

// DataDir is the daemon data directory.
func (m *Manager) DataDir() string { return m.opts.DataDir }

// UpdateParams holds optional fields for Update; nil means unchanged.
type UpdateParams struct {
	Name              *string `json:"name"`
	MCVersion         *string `json:"mc_version"`
	JavaVersion       *string `json:"java_version"`
	Port              *int    `json:"port"`
	RCONPort          *int    `json:"rcon_port"`
	MaxMemoryMB       *int    `json:"max_memory_mb"`
	AutoRestart       *bool   `json:"auto_restart"`
	ConnectionAddress *string `json:"connection_address"`
}

// Update applies p. Changes to ports, memory and versions take effect on the
// next start.
func (m *Manager) Update(ctx context.Context, id string, p UpdateParams) (*db.Server, error) {
	inst, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	inst.mu.Lock()
	defer inst.mu.Unlock()

	s := inst.srv
	if p.Name != nil {
		if !nameRe.MatchString(*p.Name) {
			return nil, fmt.Errorf("%w: invalid name", ErrInvalid)
		}
		s.Name = *p.Name
	}
	if p.MCVersion != nil {
		if !mcVersionRe.MatchString(*p.MCVersion) {
			return nil, fmt.Errorf("%w: invalid mc_version", ErrInvalid)
		}
		s.MCVersion = *p.MCVersion
	}
	if p.JavaVersion != nil {
		if _, err := strconv.Atoi(*p.JavaVersion); err != nil {
			return nil, fmt.Errorf("%w: invalid java_version", ErrInvalid)
		}
		s.JavaVersion = *p.JavaVersion
	}
	if p.Port != nil {
		s.Port = *p.Port
	}
	if p.RCONPort != nil {
		s.RCONPort = *p.RCONPort
	}
	if p.MaxMemoryMB != nil {
		s.MaxMemoryMB = *p.MaxMemoryMB
	}
	if p.AutoRestart != nil {
		s.AutoRestart = *p.AutoRestart
	}
	if p.ConnectionAddress != nil {
		s.ConnectionAddress = emptyToNil(p.ConnectionAddress)
	}
	if err := validateCommon(s.Port, s.RCONPort, s.MaxMemoryMB); err != nil {
		return nil, err
	}
	if err := m.checkPortsUnusedLocked(id, s.Port, s.RCONPort); err != nil {
		return nil, err
	}
	if err := m.opts.DB.UpdateServer(ctx, &s); err != nil {
		if isUniqueErr(err) {
			return nil, fmt.Errorf("%w: name or port already in use", ErrConflict)
		}
		return nil, err
	}
	inst.srv = s
	s.Status = inst.status
	return &s, nil
}

// Delete removes a stopped server, its directory and its backups.
func (m *Manager) Delete(ctx context.Context, id string) error {
	inst, err := m.Get(id)
	if err != nil {
		return err
	}
	switch inst.Status() {
	case db.StatusStopped, db.StatusCrashed:
	default:
		return fmt.Errorf("%w: stop the server before deleting it", ErrConflict)
	}
	inst.mu.Lock()
	inst.cancelRestartLocked()
	srv := inst.srv
	inst.mu.Unlock()

	// Re-derive paths through SafePath rather than trusting the stored value.
	dir, err := fsutil.SafePath(m.ServersDir(), filepath.Base(srv.Directory))
	if err != nil || dir != srv.Directory {
		return fmt.Errorf("refusing to delete unexpected directory %q", srv.Directory)
	}
	backups, err := fsutil.SafePath(filepath.Join(m.opts.DataDir, "backups"), filepath.Base(srv.Directory))
	if err != nil {
		return err
	}
	if err := m.opts.DB.DeleteServer(ctx, id); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.instances, id)
	m.mu.Unlock()
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.RemoveAll(backups)
}

// Shutdown stops all running servers in parallel without a countdown.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.RLock()
	insts := make([]*Instance, 0, len(m.instances))
	for _, inst := range m.instances {
		insts = append(insts, inst)
	}
	m.mu.RUnlock()
	var wg sync.WaitGroup
	for _, inst := range insts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := inst.Stop(ctx, StopOptions{}); err != nil {
				m.opts.Logger.Error("shutdown stop", "server", inst.Server().Name, "err", err)
			}
		}()
	}
	wg.Wait()
}

func (m *Manager) checkPortsUnusedLocked(selfID string, ports ...int) error {
	if ports[0] == ports[1] {
		return fmt.Errorf("%w: port and rcon_port must differ", ErrInvalid)
	}
	for id, inst := range m.instances {
		if id == selfID {
			continue
		}
		for _, p := range ports {
			if p == inst.srv.Port || p == inst.srv.RCONPort {
				return fmt.Errorf("%w: port %d is used by server %s", ErrConflict, p, inst.srv.Name)
			}
		}
	}
	return nil
}

func validateCommon(port, rconPort, memMB int) error {
	for _, p := range []int{port, rconPort} {
		if p < 1024 || p > 65535 {
			return fmt.Errorf("%w: port %d out of range 1024-65535", ErrInvalid, p)
		}
	}
	if memMB < 512 || memMB > 65536 {
		return fmt.Errorf("%w: max_memory_mb must be 512-65536", ErrInvalid)
	}
	return nil
}

func emptyToNil(s *string) *string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	v := strings.TrimSpace(*s)
	return &v
}

func isUniqueErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
