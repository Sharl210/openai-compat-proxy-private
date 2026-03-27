package cacheinfo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const cacheInfoDirName = "Cache_Info"

func cacheInfoDir(providersDir string) string {
	return filepath.Join(providersDir, cacheInfoDirName)
}

func EnsureCacheInfoDir(providersDir string) error {
	return os.MkdirAll(cacheInfoDir(providersDir), 0755)
}

func LoadProviderStats(providersDir, providerID string) (*ProviderStats, error) {
	path := filepath.Join(cacheInfoDir(providersDir), providerID+".json")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 %s 失败: %w", path, err)
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

	jsonPath := filepath.Join(dir, providerID+".json")
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
