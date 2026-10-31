package index

import (
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// PackageEntry is one Debian package known to lazyrepo, derived from a
// single GitHub release asset.
type PackageEntry struct {
	Key           string // dedup identity, e.g. "sha256:<digest>"
	Package       string
	Version       string
	Architecture  string
	Description   string
	Depends       string
	Filename      string // bare asset name, e.g. "pve-microvm_0.3.21-1_all.deb"
	Size          int64
	SHA256        string
	DownloadURL   string
	InstalledSize int64 // KiB, from the .deb's own Installed-Size control field
}

// Index is the in-memory set of known packages, safe for concurrent use.
type Index struct {
	mu      sync.RWMutex
	entries map[string]PackageEntry
}

// New returns an empty Index.
func New() *Index {
	return &Index{entries: make(map[string]PackageEntry)}
}

// Has reports whether an entry with the given Key is already indexed.
func (idx *Index) Has(key string) bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	_, ok := idx.entries[key]
	return ok
}

// Get returns the entry with the given Key, if any.
func (idx *Index) Get(key string) (PackageEntry, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	e, ok := idx.entries[key]
	return e, ok
}

// Add inserts or replaces the entry for e.Key.
func (idx *Index) Add(e PackageEntry) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.entries[e.Key] = e
}

// All returns entries in a total order, including architecture and unique key
// tie-breakers, so map iteration cannot change repository checksums.
func (idx *Index) All() []PackageEntry {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make([]PackageEntry, 0, len(idx.entries))
	for _, e := range idx.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Package != out[j].Package {
			return out[i].Package < out[j].Package
		}
		if out[i].Version != out[j].Version {
			return out[i].Version < out[j].Version
		}
		if out[i].Architecture != out[j].Architecture {
			return out[i].Architecture < out[j].Architecture
		}
		if out[i].Filename != out[j].Filename {
			return out[i].Filename < out[j].Filename
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// Lookup finds the entry whose Filename matches the given bare filename
// (as extracted from an incoming HTTP request path).
func (idx *Index) Lookup(filename string) (PackageEntry, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	for _, e := range idx.entries {
		if e.Filename == filename {
			return e, true
		}
	}
	return PackageEntry{}, false
}

// RenderPackages renders every entry as a Debian "Packages" file.
func (idx *Index) RenderPackages() []byte {
	var buf bytes.Buffer
	for _, e := range idx.All() {
		fmt.Fprintf(&buf, "Package: %s\n", e.Package)
		fmt.Fprintf(&buf, "Version: %s\n", e.Version)
		fmt.Fprintf(&buf, "Architecture: %s\n", e.Architecture)
		if e.InstalledSize != 0 {
			fmt.Fprintf(&buf, "Installed-Size: %d\n", e.InstalledSize)
		}
		fmt.Fprintf(&buf, "Filename: ./%s\n", e.Filename)
		fmt.Fprintf(&buf, "Size: %d\n", e.Size)
		if e.SHA256 != "" {
			fmt.Fprintf(&buf, "SHA256: %s\n", e.SHA256)
		}
		if e.Depends != "" {
			fmt.Fprintf(&buf, "Depends: %s\n", e.Depends)
		}
		if e.Description != "" {
			fmt.Fprintf(&buf, "Description: %s\n", e.Description)
		}
		buf.WriteString("\n")
	}
	return buf.Bytes()
}

// RenderPackagesGz renders the same content as RenderPackages, gzip-compressed.
func (idx *Index) RenderPackagesGz() ([]byte, error) {
	return compressPackages(idx.RenderPackages())
}

func compressPackages(packages []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(packages); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RenderRelease renders an (unsigned) apt "Release" file listing the
// MD5Sum/SHA256 hashes and sizes of Packages and Packages.gz. Without this,
// apt has no declared list of which index files exist and blindly probes
// every compression variant (Packages.xz, .bz2, .lzma, .gz, ...) before
// falling back to one that succeeds - each miss logs as a 404. Listing the
// real files here means apt fetches only what's actually there.
func (idx *Index) RenderRelease() ([]byte, error) {
	_, _, release, err := idx.RenderSnapshot()
	return release, err
}

// RenderSnapshot renders all repository files from one immutable package list.
func (idx *Index) RenderSnapshot() (packages, packagesGz, release []byte, err error) {
	packages = idx.RenderPackages()
	packagesGz, err = compressPackages(packages)
	if err != nil {
		return nil, nil, nil, err
	}

	md5Sum := md5.Sum(packages)
	sha256Sum := sha256.Sum256(packages)
	md5SumGz := md5.Sum(packagesGz)
	sha256SumGz := sha256.Sum256(packagesGz)

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Acquire-By-Hash: yes\n")
	fmt.Fprintf(&buf, "Origin: lazyrepo\n")
	fmt.Fprintf(&buf, "Label: lazyrepo\n")
	fmt.Fprintf(&buf, "Suite: ./\n")
	fmt.Fprintf(&buf, "Date: %s\n", time.Now().UTC().Format(time.RFC1123))
	fmt.Fprintf(&buf, "Description: lazyrepo apt repository\n")
	fmt.Fprintf(&buf, "MD5Sum:\n")
	fmt.Fprintf(&buf, " %x %d Packages\n", md5Sum, len(packages))
	fmt.Fprintf(&buf, " %x %d Packages.gz\n", md5SumGz, len(packagesGz))
	fmt.Fprintf(&buf, "SHA256:\n")
	fmt.Fprintf(&buf, " %x %d Packages\n", sha256Sum, len(packages))
	fmt.Fprintf(&buf, " %x %d Packages.gz\n", sha256SumGz, len(packagesGz))
	return packages, packagesGz, buf.Bytes(), nil
}

// LoadCache reads a previously-saved Index from path. A missing file is
// not an error - it simply returns an empty Index, as on first run.
func LoadCache(path string) (*Index, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return New(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading cache file: %w", err)
	}

	var entries []PackageEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parsing cache file: %w", err)
	}

	idx := New()
	for _, e := range entries {
		idx.entries[e.Key] = e
	}
	return idx, nil
}

// SaveCache writes the Index to path atomically (write to a temp file in
// the same directory, then rename over the destination).
func (idx *Index) SaveCache(path string) error {
	idx.mu.RLock()
	entries := make([]PackageEntry, 0, len(idx.entries))
	for _, e := range idx.entries {
		entries = append(entries, e)
	}
	idx.mu.RUnlock()

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling cache: %w", err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".index-*.json.tmp")
	if err != nil {
		return fmt.Errorf("creating temp cache file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp cache file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp cache file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming temp cache file into place: %w", err)
	}
	return nil
}
