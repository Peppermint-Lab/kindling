package runtime

import "testing"

func TestRemoteVMCapabilitiesForVMCloudHypervisorRunning(t *testing.T) {
	t.Parallel()
	m := RemoteVMCapabilitiesForVM(BackendCloudHypervisor, "running")
	if !m["ssh_tcp"].Supported || !m["ssh_tcp"].Available {
		t.Fatalf("ssh_tcp = %#v", m["ssh_tcp"])
	}
	if !m["live_migration"].Supported {
		t.Fatal("live_migration should be supported on cloud-hypervisor")
	}
	if !m["live_migration"].Available {
		t.Fatal("live_migration should be available when running")
	}
}

func TestRemoteVMCapabilitiesForVMCloudHypervisorStopped(t *testing.T) {
	t.Parallel()
	m := RemoteVMCapabilitiesForVM(BackendCloudHypervisor, "stopped")
	if !m["ssh_tcp"].Supported || m["ssh_tcp"].Available {
		t.Fatalf("ssh_tcp = %#v", m["ssh_tcp"])
	}
}

func TestRemoteVMCapabilitiesForVMAppleVZNoLiveMigration(t *testing.T) {
	t.Parallel()
	m := RemoteVMCapabilitiesForVM(BackendAppleVZ, "running")
	if m["live_migration"].Supported {
		t.Fatal("live_migration not supported on apple-vz")
	}
}

func TestRemoteVMCapabilitiesForVMCrunGuestAccessUnsupported(t *testing.T) {
	t.Parallel()
	m := RemoteVMCapabilitiesForVM(BackendCrun, "running")
	if m["ssh_tcp"].Supported || m["terminal_shell"].Supported {
		t.Fatalf("crun should not advertise guest ssh/shell yet: %#v", m["ssh_tcp"])
	}
	if !m["browser_app"].Supported || !m["browser_app"].Available {
		t.Fatalf("browser_app = %#v", m["browser_app"])
	}
}
