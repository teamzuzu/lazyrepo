package index

import (
	"bytes"
	"compress/gzip"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func sampleEntry() PackageEntry {
	return PackageEntry{
		Key:          "sha256:abc123",
		Package:      "pve-microvm",
		Version:      "0.3.21-1",
		Architecture: "all",
		Description:  "MicroVM support for Proxmox VE",
		Depends:      "qemu-utils",
		Filename:     "pve-microvm_0.3.21-1_all.deb",
		Size:         13035136,
		SHA256:       "abc123",
		DownloadURL:  "https://github.com/rcarmo/pve-microvm/releases/download/v0.3.21/pve-microvm_0.3.21-1_all.deb",
	}
}

func TestIndex_HasAdd(t *testing.T) {
	idx := New()
	if idx.Has("sha256:abc123") {
		t.Fatal("empty index should not have any key")
	}
	idx.Add(sampleEntry())
	if !idx.Has("sha256:abc123") {
		t.Fatal("expected key to be present after Add")
	}
}

func TestIndex_All_SortedByPackageThenVersion(t *testing.T) {
	idx := New()
	idx.Add(PackageEntry{Key: "b", Package: "zeta", Version: "1.0"})
	idx.Add(PackageEntry{Key: "a", Package: "alpha", Version: "2.0"})
	idx.Add(PackageEntry{Key: "c", Package: "alpha", Version: "1.0"})

	all := idx.All()
	if len(all) != 3 {
		t.Fatalf("len(All()) = %d, want 3", len(all))
	}
	want := []string{"alpha 1.0", "alpha 2.0", "zeta 1.0"}
	for i, e := range all {
		got := e.Package + " " + e.Version
		if got != want[i] {
			t.Errorf("All()[%d] = %q, want %q", i, got, want[i])
		}
	}
}

func TestIndex_Lookup(t *testing.T) {
	idx := New()
	idx.Add(sampleEntry())

	e, ok := idx.Lookup("pve-microvm_0.3.21-1_all.deb")
	if !ok {
		t.Fatal("expected Lookup to find entry by Filename")
	}
	if e.DownloadURL != sampleEntry().DownloadURL {
		t.Errorf("DownloadURL = %q, want %q", e.DownloadURL, sampleEntry().DownloadURL)
	}

	if _, ok := idx.Lookup("does-not-exist.deb"); ok {
		t.Fatal("expected Lookup to return false for unknown filename")
	}
}

func TestIndex_RenderPackages(t *testing.T) {
	idx := New()
	idx.Add(sampleEntry())

	out := string(idx.RenderPackages())
	for _, want := range []string{
		"Package: pve-microvm\n",
		"Version: 0.3.21-1\n",
		"Architecture: all\n",
		"Filename: ./pve-microvm_0.3.21-1_all.deb\n",
		"Size: 13035136\n",
		"SHA256: abc123\n",
		"Depends: qemu-utils\n",
		"Description: MicroVM support for Proxmox VE\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderPackages() missing %q in:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "\n\n") {
		t.Error("expected stanza to end with a blank line separator")
	}
}

func TestIndex_RenderPackagesGz_RoundTrips(t *testing.T) {
	idx := New()
	idx.Add(sampleEntry())

	gz, err := idx.RenderPackagesGz()
	if err != nil {
		t.Fatalf("RenderPackagesGz returned error: %v", err)
	}
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("gzip.NewReader failed: %v", err)
	}
	decompressed, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading decompressed data: %v", err)
	}
	if string(decompressed) != string(idx.RenderPackages()) {
		t.Error("RenderPackagesGz content does not match RenderPackages after decompression")
	}
}

func TestSaveCache_LoadCache_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")

	idx := New()
	idx.Add(sampleEntry())
	if err := idx.SaveCache(path); err != nil {
		t.Fatalf("SaveCache returned error: %v", err)
	}

	loaded, err := LoadCache(path)
	if err != nil {
		t.Fatalf("LoadCache returned error: %v", err)
	}
	if !loaded.Has("sha256:abc123") {
		t.Fatal("expected loaded index to have the saved entry")
	}
	e, _ := loaded.Lookup("pve-microvm_0.3.21-1_all.deb")
	if e.Version != "0.3.21-1" {
		t.Errorf("Version = %q, want %q", e.Version, "0.3.21-1")
	}
}

func TestLoadCache_MissingFile_ReturnsEmptyIndex(t *testing.T) {
	dir := t.TempDir()
	idx, err := LoadCache(filepath.Join(dir, "does-not-exist.json"))
	if err != nil {
		t.Fatalf("LoadCache returned error for missing file: %v", err)
	}
	if len(idx.All()) != 0 {
		t.Errorf("len(All()) = %d, want 0 for missing cache file", len(idx.All()))
	}
}
