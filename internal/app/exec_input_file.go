package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

func readExecInputFile(path string, maxBytes int64) ([]byte, error) {
	if path != strings.TrimSpace(path) {
		return nil, errors.New("file path cannot contain surrounding whitespace")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("file exceeds %d MiB", maxBytes/(1024*1024))
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	// Bound the actual read as well: the file may grow after the size check.
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file exceeds %d MiB", maxBytes/(1024*1024))
	}
	return data, nil
}
