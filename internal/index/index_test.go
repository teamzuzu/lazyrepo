package index

import (
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"crypto/sha256"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func sampleEntry() PackageEntry {
	return PackageEntry{
		Key:           "sha256:abc123",
		Package:       "pve-microvm",
		Version:       "0.3.21-1",
		Architecture:  "all",
		Description:   "MicroVM support for Proxmox VE",
		Depends:       "qemu-utils",
		Filename:      "pve-microvm_0.3.21-1_all.deb",
		Size:          13035136,
		SHA256:        "abc123",
		DownloadURL:   "https://github.com/rcarmo/pve-microvm/releases/download/v0.3.21/pve-microvm_0.3.21-1_all.deb",
		InstalledSize: 13206,
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

func TestIndex_Get(t *testing.T) {
	idx := New()
	if _, ok := idx.Get("sha256:abc123"); ok {
		t.Fatal("empty index should not have any key")
	}
	idx.Add(sampleEntry())
	e, ok := idx.Get("sha256:abc123")
	if !ok {
		t.Fatal("expected Get to find entry after Add")
	}
	if e.InstalledSize != 13206 {
		t.Errorf("InstalledSize = %d, want %d", e.InstalledSize, 13206)
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
		"Installed-Size: 13206\n",
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

func TestIndex_RenderPackages_OmitsInstalledSizeWhenZero(t *testing.T) {
	idx := New()
	e := sampleEntry()
	e.InstalledSize = 0
	idx.Add(e)

	out := string(idx.RenderPackages())
	if strings.Contains(out, "Installed-Size:") {
		t.Errorf("RenderPackages() should omit Installed-Size when zero, got:\n%s", out)
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

func TestIndex_RenderRelease_HashesMatchPackagesFiles(t *testing.T) {
	idx := New()
	idx.Add(sampleEntry())

	packages := idx.RenderPackages()
	packagesGz, err := idx.RenderPackagesGz()
	if err != nil {
		t.Fatalf("RenderPackagesGz returned error: %v", err)
	}

	release, err := idx.RenderRelease()
	if err != nil {
		t.Fatalf("RenderRelease returned error: %v", err)
	}
	out := string(release)

	wantMD5 := fmt.Sprintf("%x %d Packages\n", md5.Sum(packages), len(packages))
	wantMD5Gz := fmt.Sprintf("%x %d Packages.gz\n", md5.Sum(packagesGz), len(packagesGz))
	wantSHA256 := fmt.Sprintf("%x %d Packages\n", sha256.Sum256(packages), len(packages))
	wantSHA256Gz := fmt.Sprintf("%x %d Packages.gz\n", sha256.Sum256(packagesGz), len(packagesGz))

	for _, want := range []string{
		"Suite: ./\n",
		" " + wantMD5,
		" " + wantMD5Gz,
		" " + wantSHA256,
		" " + wantSHA256Gz,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderRelease() missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "Date: ") {
		t.Error("RenderRelease() missing Date field")
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

func TestMultiArchitectureOutputIsDeterministic(t *testing.T) {
	idx := New()
	for _, arch := range []string{"amd64", "arm64", "armhf"} {
		for _, suffix := range []string{"a", "b"} {
			e := sampleEntry()
			e.Key = arch + suffix
			e.Architecture = arch
			e.Filename = "package_" + arch + suffix + ".deb"
			idx.Add(e)
		}
	}
	want := idx.RenderPackages()
	for i := 0; i < 100; i++ {
		packages, compressed, release, err := idx.RenderSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(packages, want) {
			t.Fatal("package order changed between renders")
		}
		for name, data := range map[string][]byte{"Packages": packages, "Packages.gz": compressed} {
			line := fmt.Sprintf(" %x %d %s\n", sha256.Sum256(data), len(data), name)
			if !strings.Contains(string(release), line) {
				t.Fatalf("Release does not match %s", name)
			}
		}
	}
}
