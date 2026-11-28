package app

import (
	"fmt"
	"io"
	"os"
	"strings"

	"cyberagent-workbench/internal/apperror"
)

func readExecInputFile(path string, maxBytes int64) ([]byte, error) {
	if path != strings.TrimSpace(path) {
		return nil, apperror.New(apperror.CodeInvalidArgument, "file path cannot contain surrounding whitespace")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, apperror.New(apperror.CodeInvalidArgument, "not a regular file")
	}
	if info.Size() > maxBytes {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			fmt.Sprintf("file exceeds %d MiB", maxBytes/(1024*1024)))
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
		return nil, apperror.New(apperror.CodeInvalidArgument,
			fmt.Sprintf("file exceeds %d MiB", maxBytes/(1024*1024)))
	}
	return data, nil
}
