package runtime

import "testing"

// TestRemoteVMSupportedCapabilityMatrix must match supportedMaskForBackend in remote_vm_product.go.
func TestRemoteVMSupportedCapabilityMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		backend                  string
		wantLiveMigrationSupport bool
		wantAllOtherSupported    bool
	}{
		{BackendCloudHypervisor, true, true},
		{BackendAppleVZ, false, true},
		{BackendCrun, false, true},
		{"unknown-backend", false, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.backend, func(t *testing.T) {
			t.Parallel()
			m := RemoteVMCapabilitiesForVM(tc.backend, "running")
			if got := m[string(RemoteVMCapLiveMigration)].Supported; got != tc.wantLiveMigrationSupport {
				t.Fatalf("live_migration supported=%v want %v", got, tc.wantLiveMigrationSupport)
			}
			keys := []RemoteVMProductCapability{
				RemoteVMCapBrowserApp,
				RemoteVMCapTerminalShell,
				RemoteVMCapSSHTCP,
				RemoteVMCapExecCopy,
				RemoteVMCapSuspendResume,
				RemoteVMCapTemplateClone,
			}
			for _, k := range keys {
				if got := m[string(k)].Supported; got != tc.wantAllOtherSupported {
					t.Fatalf("%s supported=%v want %v", k, got, tc.wantAllOtherSupported)
				}
			}
		})
	}
}

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

func TestRemoteVMCapabilitiesForVMCrunAccessParity(t *testing.T) {
	t.Parallel()
	m := RemoteVMCapabilitiesForVM(BackendCrun, "running")
	if !m["ssh_tcp"].Supported || !m["ssh_tcp"].Available {
		t.Fatalf("crun should advertise ssh_tcp when running: %#v", m["ssh_tcp"])
	}
	if !m["terminal_shell"].Supported || !m["terminal_shell"].Available {
		t.Fatalf("crun should advertise terminal_shell when running: %#v", m["terminal_shell"])
	}
	if !m["exec_copy"].Supported || !m["exec_copy"].Available {
		t.Fatalf("crun should advertise exec_copy when running: %#v", m["exec_copy"])
	}
	if !m["browser_app"].Supported || !m["browser_app"].Available {
		t.Fatalf("browser_app = %#v", m["browser_app"])
	}
}

func TestRemoteVMCapabilitiesForVMCrunStoppedNoLiveAccess(t *testing.T) {
	t.Parallel()
	m := RemoteVMCapabilitiesForVM(BackendCrun, "stopped")
	if !m["ssh_tcp"].Supported || m["ssh_tcp"].Available {
		t.Fatalf("ssh_tcp = %#v", m["ssh_tcp"])
	}
}
