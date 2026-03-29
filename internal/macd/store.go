package macd

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

// Store manages local VM state via SQLite.
type Store struct {
	db *sql.DB
}

// VM represents a local microVM.
type VM struct {
	ID        string
	Name      string
	HostGroup string // "box" or "temp"
	Status    string // "running", "stopped", "suspended"
	Arch      string
	VCPUs     int
	MemoryMB  int
	DiskMB    int
	CreatedAt time.Time
	Template  string // parent template id, empty if not a clone
	HostPort  int    // localhost TCP port when running (0 if stopped)
}

// NewStore opens (and creates if missing) the SQLite database at path.
func NewStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path+"?_journal=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS vms (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		host_group  TEXT NOT NULL CHECK (host_group IN ('box', 'temp')),
		status      TEXT NOT NULL DEFAULT 'stopped' CHECK (status IN ('running', 'stopped', 'suspended')),
		arch        TEXT NOT NULL DEFAULT 'arm64',
		cpus        INTEGER NOT NULL DEFAULT 4,
		memory_mb   INTEGER NOT NULL DEFAULT 8192,
		disk_mb     INTEGER NOT NULL DEFAULT 10240,
		created_at  INTEGER NOT NULL,
		template    TEXT,
		host_port   INTEGER NOT NULL DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS templates (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		host_group  TEXT NOT NULL CHECK (host_group IN ('box', 'temp')),
		created_at  INTEGER NOT NULL,
		size_mb     INTEGER NOT NULL DEFAULT 0
	);
	`
	_, err := s.db.Exec(schema)
	return err
}

// ListVMs returns all VMs, optionally filtered by hostGroup.
func (s *Store) ListVMs(hostGroup string) ([]VM, error) {
	var rows *sql.Rows
	var err error
	if hostGroup != "" {
		rows, err = s.db.Query(
			"SELECT id, name, host_group, status, arch, cpus, memory_mb, disk_mb, created_at, template, host_port FROM vms WHERE host_group = ? ORDER BY created_at",
			hostGroup,
		)
	} else {
		rows, err = s.db.Query(
			"SELECT id, name, host_group, status, arch, cpus, memory_mb, disk_mb, created_at, template, host_port FROM vms ORDER BY created_at",
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanVMs(rows)
}

func scanVMs(rows *sql.Rows) ([]VM, error) {
	var vms []VM
	for rows.Next() {
		var v VM
		var createdAt int64
		if err := rows.Scan(&v.ID, &v.Name, &v.HostGroup, &v.Status, &v.Arch, &v.VCPUs, &v.MemoryMB, &v.DiskMB, &createdAt, &v.Template, &v.HostPort); err != nil {
			return nil, err
		}
		v.CreatedAt = time.Unix(createdAt, 0)
		vms = append(vms, v)
	}
	return vms, rows.Err()
}

// GetVM returns a single VM by id.
func (s *Store) GetVM(id string) (*VM, error) {
	var v VM
	var createdAt int64
	err := s.db.QueryRow(
		"SELECT id, name, host_group, status, arch, cpus, memory_mb, disk_mb, created_at, template, host_port FROM vms WHERE id = ?",
		id,
	).Scan(&v.ID, &v.Name, &v.HostGroup, &v.Status, &v.Arch, &v.VCPUs, &v.MemoryMB, &v.DiskMB, &createdAt, &v.Template, &v.HostPort)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	v.CreatedAt = time.Unix(createdAt, 0)
	return &v, nil
}

// CreateVM inserts a new VM record.
func (s *Store) CreateVM(vm VM) error {
	_, err := s.db.Exec(
		`INSERT INTO vms (id, name, host_group, status, arch, cpus, memory_mb, disk_mb, created_at, template) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		vm.ID, vm.Name, vm.HostGroup, vm.Status, vm.Arch, vm.VCPUs, vm.MemoryMB, vm.DiskMB, vm.CreatedAt.Unix(), vm.Template,
	)
	return err
}

// UpdateVMHostPort records the host port assigned to a running VM.
func (s *Store) UpdateVMHostPort(id string, hostPort int) error {
	_, err := s.db.Exec("UPDATE vms SET host_port = ? WHERE id = ?", hostPort, id)
	return err
}

// UpdateVMStatus updates the status of a VM.
func (s *Store) UpdateVMStatus(id, status string) error {
	_, err := s.db.Exec("UPDATE vms SET status = ? WHERE id = ?", status, id)
	return err
}

// DeleteVM removes a VM record.
func (s *Store) DeleteVM(id string) error {
	_, err := s.db.Exec("DELETE FROM vms WHERE id = ?", id)
	return err
}

// Box returns the box VM if one exists.
func (s *Store) Box() (*VM, error) {
	vms, err := s.ListVMs("box")
	if err != nil {
		return nil, err
	}
	if len(vms) == 0 {
		return nil, nil
	}
	return &vms[0], nil
}

// EnsureBox creates the box VM record if it doesn't exist.
func (s *Store) EnsureBox(name string, cpus, memoryMB, diskMB int) (*VM, error) {
	existing, err := s.Box()
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}
	vm := VM{
		ID:        uuid.New().String(),
		Name:      name,
		HostGroup: "box",
		Status:    "stopped",
		Arch:      "arm64",
		VCPUs:     cpus,
		MemoryMB:  memoryMB,
		DiskMB:    diskMB,
		CreatedAt: time.Now(),
	}
	if err := s.CreateVM(vm); err != nil {
		return nil, err
	}
	return &vm, nil
}

// ListTemplates returns all templates, optionally filtered by hostGroup.
func (s *Store) ListTemplates(hostGroup string) ([]Template, error) {
	var rows *sql.Rows
	var err error
	if hostGroup != "" {
		rows, err = s.db.Query(
			"SELECT id, name, host_group, created_at, size_mb FROM templates WHERE host_group = ? ORDER BY created_at",
			hostGroup,
		)
	} else {
		rows, err = s.db.Query(
			"SELECT id, name, host_group, created_at, size_mb FROM templates ORDER BY created_at",
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var templates []Template
	for rows.Next() {
		var t Template
		var createdAt int64
		if err := rows.Scan(&t.ID, &t.Name, &t.HostGroup, &createdAt, &t.SizeMB); err != nil {
			return nil, err
		}
		t.CreatedAt = time.Unix(createdAt, 0)
		templates = append(templates, t)
	}
	return templates, rows.Err()
}

// Template represents a VM template.
type Template struct {
	ID        string
	Name      string
	HostGroup string
	CreatedAt time.Time
	SizeMB    int
}

// CreateTemplate inserts a new template record.
func (s *Store) CreateTemplate(t Template) error {
	_, err := s.db.Exec(
		`INSERT INTO templates (id, name, host_group, created_at, size_mb) VALUES (?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.HostGroup, t.CreatedAt.Unix(), t.SizeMB,
	)
	return err
}

// DeleteTemplate removes a template record.
func (s *Store) DeleteTemplate(id string) error {
	_, err := s.db.Exec("DELETE FROM templates WHERE id = ?", id)
	return err
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}
