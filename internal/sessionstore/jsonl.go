package sessionstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func readJSONLines[T any](path string) ([]T, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read JSONL file: %w", err)
	}
	if len(content) == 0 {
		return nil, nil
	}

	endsWithNewline := content[len(content)-1] == '\n'
	lines := bytes.Split(content, []byte{'\n'})
	values := make([]T, 0, len(lines))
	for index, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			if index == len(lines)-1 && endsWithNewline {
				continue
			}
			return nil, fmt.Errorf("decode JSONL line %d: empty record", index+1)
		}

		var value T
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&value); err != nil {
			if index == len(lines)-1 && !endsWithNewline {
				return values, nil
			}
			return nil, fmt.Errorf("decode JSONL line %d: %w", index+1, err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if index == len(lines)-1 && !endsWithNewline {
				return values, nil
			}
			if err == nil {
				return nil, fmt.Errorf("decode JSONL line %d: multiple values", index+1)
			}
			return nil, fmt.Errorf("decode JSONL line %d: %w", index+1, err)
		}
		values = append(values, value)
	}

	return values, nil
}

// hasTruncatedJSONLine reports only an incomplete final record. Complete final
// JSON without a newline is valid and will be normalized before the next append.
func hasTruncatedJSONLine(path string) (bool, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || len(content) == 0 {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect JSONL recovery state: %w", err)
	}
	if content[len(content)-1] == '\n' {
		return false, nil
	}
	lineStart := bytes.LastIndexByte(content, '\n') + 1
	decoder := json.NewDecoder(bytes.NewReader(content[lineStart:]))
	var value any
	if err := decoder.Decode(&value); err != nil {
		return true, nil
	}
	var extra any
	return !errors.Is(decoder.Decode(&extra), io.EOF), nil
}

func appendJSONLine(path string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode JSONL record: %w", err)
	}
	encoded = append(encoded, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create JSONL parent directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open JSONL file: %w", err)
	}
	written, writeErr := file.Write(encoded)
	if writeErr == nil && written != len(encoded) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("append JSONL record: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close JSONL file: %w", closeErr)
	}

	return nil
}

func prepareJSONLinesForAppend(path string) error {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || len(content) == 0 {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read JSONL append boundary: %w", err)
	}
	if content[len(content)-1] == '\n' {
		return nil
	}

	lastNewline := bytes.LastIndexByte(content, '\n')
	lineStart := lastNewline + 1
	decoder := json.NewDecoder(bytes.NewReader(content[lineStart:]))
	var value any
	decodeErr := decoder.Decode(&value)
	if decodeErr == nil {
		var extra any
		decodeErr = decoder.Decode(&extra)
		if errors.Is(decodeErr, io.EOF) {
			return appendRawJSONL(path, []byte{'\n'})
		}
	}

	file, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open truncated JSONL file: %w", err)
	}
	truncateErr := file.Truncate(int64(lineStart))
	if truncateErr == nil {
		truncateErr = file.Sync()
	}
	closeErr := file.Close()
	if truncateErr != nil {
		return fmt.Errorf("repair truncated JSONL file: %w", truncateErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close repaired JSONL file: %w", closeErr)
	}

	return nil
}

func appendRawJSONL(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open JSONL append boundary: %w", err)
	}
	written, writeErr := file.Write(content)
	if writeErr == nil && written != len(content) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("write JSONL append boundary: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close JSONL append boundary: %w", closeErr)
	}

	return nil
}
