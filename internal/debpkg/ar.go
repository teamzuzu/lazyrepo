package debpkg

import (
	"fmt"
	"strconv"
	"strings"
)

const arMagic = "!<arch>\n"

type arEntry struct {
	name string
	data []byte
}

// readAr parses the contents of a Unix ar archive (the outer container
// format .deb files use) into its member entries.
func readAr(data []byte) ([]arEntry, error) {
	if len(data) < len(arMagic) || string(data[:len(arMagic)]) != arMagic {
		return nil, fmt.Errorf("not an ar archive: missing %q magic", arMagic)
	}

	var entries []arEntry
	pos := len(arMagic)
	for pos+60 <= len(data) {
		header := data[pos : pos+60]
		name := strings.TrimSuffix(strings.TrimRight(string(header[0:16]), " "), "/")
		sizeStr := strings.TrimSpace(string(header[48:58]))
		size, err := strconv.Atoi(sizeStr)
		if err != nil {
			return nil, fmt.Errorf("ar entry %q has invalid size field %q: %w", name, sizeStr, err)
		}
		pos += 60
		if pos+size > len(data) {
			return nil, fmt.Errorf("ar entry %q is truncated", name)
		}
		entries = append(entries, arEntry{name: name, data: data[pos : pos+size]})
		pos += size
		if size%2 == 1 {
			pos++
		}
	}
	return entries, nil
}
