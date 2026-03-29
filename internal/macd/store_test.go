package macd

import (
	"path/filepath"
	"testing"
	"time"
)

func TestListVMsIncludesHostPort(t *testing.T) {
	t.Parallel()

	store, err := NewStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	vm := VM{
		ID:        "vm-1",
		Name:      "box-1",
		HostGroup: "box",
		Status:    "running",
		Arch:      "arm64",
		VCPUs:     4,
		MemoryMB:  8192,
		DiskMB:    51200,
		CreatedAt: time.Unix(1, 0),
	}
	if err := store.CreateVM(vm); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	if err := store.UpdateVMHostPort(vm.ID, 32123); err != nil {
		t.Fatalf("UpdateVMHostPort: %v", err)
	}

	vms, err := store.ListVMs("box")
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	if len(vms) != 1 {
		t.Fatalf("ListVMs length = %d, want 1", len(vms))
	}
	if got := vms[0].HostPort; got != 32123 {
		t.Fatalf("HostPort = %d, want 32123", got)
	}
}
