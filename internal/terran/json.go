package terran

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func decodeStrict(data []byte, value any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return fmt.Errorf("trailing JSON: %w", err)
	}
	return nil
}

func readStrict(path string, value any, max int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > max {
		return fmt.Errorf("%s exceeds %d bytes", path, max)
	}
	return decodeStrict(data, value)
}

func readTrustedStateStrict(path, description string, value any, max int64) error {
	data, _, err := readTrustedFile(path, description, max, 0o600)
	if err != nil {
		return err
	}
	return decodeStrict(data, value)
}

func marshalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

type atomicWriteResult struct {
	renamed bool
}

func atomicJSON(path string, value any) error {
	data, err := marshalJSON(value)
	if err != nil {
		return err
	}
	_, err = atomicPrivateJSONBytes(path, data, nil)
	return err
}

func atomicPrivateJSONBytes(path string, data []byte, afterRename func() error) (atomicWriteResult, error) {
	var result atomicWriteResult
	dir := filepath.Dir(path)
	if err := ensurePrivateDir(dir); err != nil {
		return result, err
	}
	f, err := os.CreateTemp(dir, ".terran-*")
	if err != nil {
		return result, err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return result, err
	}
	if _, err := f.Write(data); err != nil {
		return result, err
	}
	if err := f.Sync(); err != nil {
		return result, err
	}
	if err := f.Close(); err != nil {
		return result, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return result, err
	}
	result.renamed = true
	if afterRename != nil {
		if err := afterRename(); err != nil {
			return result, err
		}
	}
	d, err := os.Open(dir)
	if err != nil {
		return result, err
	}
	err = d.Sync()
	d.Close()
	if err != nil {
		return result, err
	}
	ok = true
	return result, nil
}
