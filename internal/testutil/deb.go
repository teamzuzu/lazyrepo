package testutil

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"testing"
)

// BuildAr constructs a minimal Unix ar archive ("!<arch>\n" plus one
// 60-byte header per entry) from a name->contents map, for tests that need
// a synthetic .deb-shaped byte blob without depending on a real dpkg-deb
// binary or a vendored fixture file.
func BuildAr(t testing.TB, entries map[string][]byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	buf.WriteString("!<arch>\n")
	for name, data := range entries {
		header := make([]byte, 60)
		for i := range header {
			header[i] = ' '
		}
		copy(header[0:16], []byte(name))
		copy(header[48:58], []byte(fmt.Sprintf("%d", len(data))))
		header[58] = '`'
		header[59] = '\n'
		buf.Write(header)
		buf.Write(data)
		if len(data)%2 == 1 {
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes()
}

// BuildDeb constructs a minimal .deb-shaped ar archive containing just a
// control.tar.gz member with the given control file contents. Real .debs
// also contain a debian-binary and data.tar member, but ParseControl only
// ever looks at control.tar*, so those are omitted.
func BuildDeb(t testing.TB, control string) []byte {
	t.Helper()

	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	hdr := &tar.Header{Name: "./control", Mode: 0o644, Size: int64(len(control))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("writing tar header: %v", err)
	}
	if _, err := tw.Write([]byte(control)); err != nil {
		t.Fatalf("writing tar content: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}

	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	if _, err := gw.Write(tarBuf.Bytes()); err != nil {
		t.Fatalf("writing gzip content: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}

	return BuildAr(t, map[string][]byte{"control.tar.gz": gzBuf.Bytes()})
}
