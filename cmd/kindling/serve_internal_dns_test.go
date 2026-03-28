package main

import (
	"os"
	"reflect"
	"testing"
)

func TestInternalDNSEnabledForRuntime(t *testing.T) {
	t.Setenv("KINDLING_INTERNAL_DNS_ADDR", "")
	if !internalDNSEnabledForRuntime("cloud-hypervisor") {
		t.Fatal("expected cloud-hypervisor runtime to enable internal dns by default")
	}
	if internalDNSEnabledForRuntime("crun") {
		t.Fatal("expected non-cloud-hypervisor runtime to disable internal dns")
	}
}

func TestInternalDNSEnabledForRuntimeHonorsDisableFlag(t *testing.T) {
	t.Setenv("KINDLING_INTERNAL_DNS_ADDR", "off")
	if internalDNSEnabledForRuntime("cloud-hypervisor") {
		t.Fatal("expected internal dns to be disabled when KINDLING_INTERNAL_DNS_ADDR=off")
	}
}

func TestInternalDNSRuntimeMetadataIncludesBindAddress(t *testing.T) {
	t.Setenv("KINDLING_INTERNAL_DNS_ADDR", "127.0.0.1:1053")
	got := internalDNSRuntimeMetadata("cloud-hypervisor")
	want := map[string]any{
		"internal_dns_enabled": true,
		"internal_dns_addr":    "127.0.0.1:1053",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected metadata:\nwant: %#v\ngot:  %#v", want, got)
	}
}

func TestSplitCSV(t *testing.T) {
	got := splitCSV(" 1.1.1.1 , ,8.8.8.8:53 ")
	want := []string{"1.1.1.1", "8.8.8.8:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected split result:\nwant: %#v\ngot:  %#v", want, got)
	}
}

func TestEffectiveInternalDNSAddrDefaultsToPort53(t *testing.T) {
	if got := effectiveInternalDNSAddr(""); got != ":53" {
		t.Fatalf("effective address = %q, want :53", got)
	}
}

func TestMain(m *testing.M) {
	code := m.Run()
	os.Exit(code)
}
