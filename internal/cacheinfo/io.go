package cacheinfo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	cacheInfoDirName  = "Cache_Info"
	systemJSONDirName = "SYSTEM_JSON_FILES"
)

func systemJSONDir(providersDir string) string {
	return filepath.Join(cacheInfoDir(providersDir), systemJSONDirName)
}

func cacheInfoDir(providersDir string) string {
	return filepath.Join(providersDir, cacheInfoDirName)
}

func EnsureCacheInfoDir(providersDir string) error {
	if err := os.MkdirAll(cacheInfoDir(providersDir), 0755); err != nil {
		return err
	}
	return os.MkdirAll(systemJSONDir(providersDir), 0755)
}

func LoadProviderStats(providersDir, providerID string) (*ProviderStats, error) {
	jsonPath := filepath.Join(systemJSONDir(providersDir), providerID+".json")
	stats, err := loadProviderStatsFromPath(jsonPath)
	if err == nil {
		return stats, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	legacyPath := filepath.Join(cacheInfoDir(providersDir), providerID+".json")
	legacyStats, legacyErr := loadProviderStatsFromPath(legacyPath)
	if legacyErr != nil {
		if os.IsNotExist(legacyErr) {
			return nil, nil
		}
		return nil, legacyErr
	}
	return legacyStats, nil
}

func loadProviderStatsFromPath(path string) (*ProviderStats, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var stats ProviderStats
	if err := json.Unmarshal(data, &stats); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	return &stats, nil
}

func SaveProviderStats(providersDir, providerID string, stats *ProviderStats) error {
	dir := cacheInfoDir(providersDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	jsonDir := systemJSONDir(providersDir)
	if err := os.MkdirAll(jsonDir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	jsonPath := filepath.Join(jsonDir, providerID+".json")
	if err := atomicWriteJSON(jsonPath, stats); err != nil {
		return err
	}

	txtPath := filepath.Join(dir, providerID+".txt")
	content := RenderProviderStats(*stats)
	if err := atomicWriteTXT(txtPath, content); err != nil {
		return err
	}

	return nil
}

func atomicWriteJSON(path string, stats *ProviderStats) error {
	data, err := json.MarshalIndent(stats, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 JSON 失败: %w", err)
	}
	return atomicWrite(path, data)
}

func atomicWriteTXT(path, content string) error {
	return atomicWrite(path, []byte(content))
}

func atomicWrite(path string, data []byte) error {
	tmpPath := path + ".tmp"

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("创建临时文件 %s 失败: %w", tmpPath, err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("写入临时文件失败: %w", err)
	}

	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("fsync 失败: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename 失败: %w", err)
	}

	return nil
}
