package debpkg

import (
	"testing"

	"github.com/teamzuzu/lazyrepo/internal/testutil"
)

func TestParseControl_Basic(t *testing.T) {
	deb := testutil.BuildDeb(t, "Package: pve-microvm\n"+
		"Version: 0.3.21-1\n"+
		"Architecture: all\n"+
		"Installed-Size: 13206\n"+
		"Depends: qemu-utils\n"+
		"Description: MicroVM support for Proxmox VE\n"+
		" Longer description continuation line.\n")

	fields, err := ParseControl(deb)
	if err != nil {
		t.Fatalf("ParseControl returned error: %v", err)
	}
	if fields.Package != "pve-microvm" {
		t.Errorf("Package = %q, want %q", fields.Package, "pve-microvm")
	}
	if fields.Version != "0.3.21-1" {
		t.Errorf("Version = %q, want %q", fields.Version, "0.3.21-1")
	}
	if fields.Architecture != "all" {
		t.Errorf("Architecture = %q, want %q", fields.Architecture, "all")
	}
	if fields.Depends != "qemu-utils" {
		t.Errorf("Depends = %q, want %q", fields.Depends, "qemu-utils")
	}
	if fields.Description == "" {
		t.Error("Description is empty, want non-empty")
	}
	if fields.InstalledSize != 13206 {
		t.Errorf("InstalledSize = %d, want %d", fields.InstalledSize, 13206)
	}
}

func TestParseControl_MissingInstalledSizeDefaultsToZero(t *testing.T) {
	deb := testutil.BuildDeb(t, "Package: pve-microvm\nVersion: 0.3.21-1\nArchitecture: all\n")

	fields, err := ParseControl(deb)
	if err != nil {
		t.Fatalf("ParseControl returned error: %v", err)
	}
	if fields.InstalledSize != 0 {
		t.Errorf("InstalledSize = %d, want 0 when control file has no Installed-Size field", fields.InstalledSize)
	}
}

func TestParseControl_MissingRequiredField(t *testing.T) {
	deb := testutil.BuildDeb(t, "Package: pve-microvm\nVersion: 0.3.21-1\n")
	if _, err := ParseControl(deb); err == nil {
		t.Fatal("expected error for control file missing Architecture, got nil")
	}
}

func TestParseControl_NotAnArArchive(t *testing.T) {
	if _, err := ParseControl([]byte("not an ar archive")); err == nil {
		t.Fatal("expected error for non-ar input, got nil")
	}
}

func TestParseControl_NoControlMember(t *testing.T) {
	deb := testutil.BuildAr(t, map[string][]byte{"debian-binary": []byte("2.0\n")})
	if _, err := ParseControl(deb); err == nil {
		t.Fatal("expected error when no control.tar member is present, got nil")
	}
}
