package debpkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// maxControlTarSize bounds the decompressed size of a control.tar member.
// Real control.tar members are realistically kilobytes, so this ceiling is
// generous but prevents a decompression bomb from exhausting memory.
const maxControlTarSize = 64 * 1024 * 1024 // 64 MB

// maxControlFileSize bounds the size of the control file extracted from
// inside control.tar. A real Debian control file is at most a few KB.
const maxControlFileSize = 1 * 1024 * 1024 // 1 MB

// ControlFields is the subset of a .deb's control file that lazyrepo needs
// to render an apt Packages stanza.
type ControlFields struct {
	Package      string
	Version      string
	Architecture string
	Depends      string
	Description  string
}

// ParseControl extracts control file fields from the raw bytes of a .deb
// package (a Unix ar archive containing a control.tar.{gz,xz,zst} member).
func ParseControl(debBytes []byte) (*ControlFields, error) {
	entries, err := readAr(debBytes)
	if err != nil {
		return nil, err
	}

	var controlEntry *arEntry
	for i := range entries {
		if strings.HasPrefix(entries[i].name, "control.tar") {
			controlEntry = &entries[i]
			break
		}
	}
	if controlEntry == nil {
		return nil, fmt.Errorf("no control.tar member found in package")
	}

	tarBytes, err := decompressControlTar(controlEntry.name, controlEntry.data)
	if err != nil {
		return nil, fmt.Errorf("decompressing %s: %w", controlEntry.name, err)
	}

	text, err := extractControlFile(tarBytes)
	if err != nil {
		return nil, err
	}

	return parseControlText(text)
}

func decompressControlTar(name string, data []byte) ([]byte, error) {
	switch {
	case name == "control.tar":
		if int64(len(data)) > maxControlTarSize {
			return nil, fmt.Errorf("control.tar exceeds size limit of %d bytes", maxControlTarSize)
		}
		return data, nil
	case strings.HasSuffix(name, ".gz"):
		r, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return readAllLimited(r, maxControlTarSize)
	case strings.HasSuffix(name, ".xz"):
		r, err := xz.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		return readAllLimited(r, maxControlTarSize)
	case strings.HasSuffix(name, ".zst"):
		r, err := zstd.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return readAllLimited(r, maxControlTarSize)
	default:
		return nil, fmt.Errorf("unsupported control.tar compression for member %q", name)
	}
}

// readAllLimited reads all of r, but returns an error instead of silently
// truncating if r produces more than limit bytes.
func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("decompressed data exceeds size limit of %d bytes", limit)
	}
	return data, nil
}

func extractControlFile(tarBytes []byte) (string, error) {
	tr := tar.NewReader(bytes.NewReader(tarBytes))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("reading control.tar: %w", err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if name == "control" {
			b, err := readAllLimited(tr, maxControlFileSize)
			if err != nil {
				return "", fmt.Errorf("reading control file: %w", err)
			}
			return string(b), nil
		}
	}
	return "", fmt.Errorf("control file not found inside control.tar")
}

func parseControlText(text string) (*ControlFields, error) {
	fields := map[string]string{}
	var currentKey string
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			currentKey = ""
			continue
		}
		if (line[0] == ' ' || line[0] == '\t') && currentKey != "" {
			fields[currentKey] += "\n" + line
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		fields[key] = val
		currentKey = key
	}

	cf := &ControlFields{
		Package:      fields["Package"],
		Version:      fields["Version"],
		Architecture: fields["Architecture"],
		Depends:      fields["Depends"],
		Description:  fields["Description"],
	}
	if cf.Package == "" || cf.Version == "" || cf.Architecture == "" {
		return nil, fmt.Errorf("control file missing one of required fields Package/Version/Architecture")
	}
	return cf, nil
}
