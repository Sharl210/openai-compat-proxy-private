package tokenestimator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func LoadBucketState(providersDir string, key BucketKey) (*BucketState, error) {
	jsonPath, _ := BucketPaths(providersDir, key)
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state BucketState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse %s: %w", jsonPath, err)
	}
	return &state, nil
}

func SaveBucketState(providersDir string, key BucketKey, state *BucketState) error {
	jsonPath, txtPath := BucketPaths(providersDir, key)
	if err := os.MkdirAll(filepath.Dir(jsonPath), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(txtPath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWrite(jsonPath, data); err != nil {
		return err
	}
	return atomicWrite(txtPath, []byte(RenderBucketState(*state)))
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
