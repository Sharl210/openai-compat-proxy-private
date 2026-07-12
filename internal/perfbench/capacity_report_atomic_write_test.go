package perfbench

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type capacityReportFileOps struct {
	writeFile  func(string, []byte, os.FileMode) error
	renameFile func(string, string) error
}

func (operations capacityReportFileOps) rename(oldPath string, newPath string) error {
	if operations.renameFile != nil {
		return operations.renameFile(oldPath, newPath)
	}
	return os.Rename(oldPath, newPath)
}

type capacityReportArtifact struct {
	path           string
	payload        []byte
	mode           os.FileMode
	temporaryPath  string
	backupPath     string
	hadOriginal    bool
	replacementSet bool
}

func writeCapacityReportSet(result capacityReportResult, samples []capacityReportSample) error {
	return writeCapacityReportSetWithFileOps(result, samples, capacityReportFileOps{writeFile: os.WriteFile})
}

func writeCapacityReportSetWithFileOps(result capacityReportResult, samples []capacityReportSample, operations capacityReportFileOps) error {
	if operations.writeFile == nil {
		return errors.New("capacity report write operation is required")
	}
	artifacts, err := capacityReportArtifacts(result, samples)
	if err != nil {
		return err
	}
	if err := stageCapacityReportArtifacts(artifacts, operations); err != nil {
		return err
	}
	if err := replaceCapacityReportArtifacts(artifacts, operations); err != nil {
		return err
	}
	return removeCapacityReportBackups(artifacts)
}

func capacityReportArtifacts(result capacityReportResult, samples []capacityReportSample) ([]capacityReportArtifact, error) {
	var raw strings.Builder
	encoder := json.NewEncoder(&raw)
	for _, sample := range samples {
		if err := encoder.Encode(sample); err != nil {
			return nil, fmt.Errorf("encode capacity raw sample: %w", err)
		}
	}
	summary, err := json.Marshal(result.Summary)
	if err != nil {
		return nil, fmt.Errorf("marshal capacity summary: %w", err)
	}
	return []capacityReportArtifact{
		{path: result.RawPath, payload: []byte(raw.String())},
		{path: result.SummaryPath, payload: append(summary, '\n')},
		{path: result.TextPath, payload: []byte(result.HumanSummary)},
	}, nil
}

func stageCapacityReportArtifacts(artifacts []capacityReportArtifact, operations capacityReportFileOps) error {
	for index := range artifacts {
		artifact := &artifacts[index]
		if err := stageCapacityReportArtifact(artifact, operations); err != nil {
			return errors.Join(err, removeCapacityReportTemps(artifacts))
		}
	}
	return nil
}

func stageCapacityReportArtifact(artifact *capacityReportArtifact, operations capacityReportFileOps) error {
	if !filepath.IsAbs(artifact.path) {
		return fmt.Errorf("capacity report path must be absolute: %q", artifact.path)
	}
	info, err := os.Stat(artifact.path)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			return fmt.Errorf("capacity report target is not a regular file: %q", artifact.path)
		}
		artifact.hadOriginal = true
		artifact.mode = info.Mode().Perm()
	case errors.Is(err, fs.ErrNotExist):
		artifact.mode = 0o600
	default:
		return fmt.Errorf("stat capacity report target %q: %w", artifact.path, err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(artifact.path), "."+filepath.Base(artifact.path)+".")
	if err != nil {
		return fmt.Errorf("create capacity report temporary file for %q: %w", artifact.path, err)
	}
	artifact.temporaryPath = temporary.Name()
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close capacity report temporary file %q: %w", artifact.temporaryPath, err)
	}
	if err := os.Chmod(artifact.temporaryPath, artifact.mode); err != nil {
		return fmt.Errorf("set capacity report temporary file permissions %q: %w", artifact.temporaryPath, err)
	}
	if err := operations.writeFile(artifact.temporaryPath, artifact.payload, artifact.mode); err != nil {
		return fmt.Errorf("write capacity report temporary file %q: %w", artifact.temporaryPath, err)
	}
	return nil
}

func replaceCapacityReportArtifacts(artifacts []capacityReportArtifact, operations capacityReportFileOps) error {
	for index := range artifacts {
		artifact := &artifacts[index]
		if artifact.hadOriginal {
			artifact.backupPath = artifact.temporaryPath + ".previous"
			if err := operations.rename(artifact.path, artifact.backupPath); err != nil {
				return errors.Join(fmt.Errorf("back up capacity report %q: %w", artifact.path, err), restoreCapacityReportArtifacts(artifacts, operations))
			}
		}
		if err := operations.rename(artifact.temporaryPath, artifact.path); err != nil {
			return errors.Join(fmt.Errorf("replace capacity report %q: %w", artifact.path, err), restoreCapacityReportArtifacts(artifacts, operations))
		}
		artifact.temporaryPath = ""
		artifact.replacementSet = true
	}
	return nil
}

func restoreCapacityReportArtifacts(artifacts []capacityReportArtifact, operations capacityReportFileOps) error {
	var restoreErr error
	for index := len(artifacts) - 1; index >= 0; index-- {
		artifact := &artifacts[index]
		if artifact.replacementSet {
			if err := os.Remove(artifact.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				restoreErr = errors.Join(restoreErr, fmt.Errorf("remove replacement capacity report %q: %w", artifact.path, err))
			}
		}
		if artifact.backupPath != "" {
			if err := operations.rename(artifact.backupPath, artifact.path); err != nil {
				restoreErr = errors.Join(restoreErr, fmt.Errorf("restore capacity report %q: %w", artifact.path, err))
			}
		}
	}
	return errors.Join(restoreErr, removeCapacityReportTemps(artifacts))
}

func removeCapacityReportTemps(artifacts []capacityReportArtifact) error {
	var removeErr error
	for _, artifact := range artifacts {
		if artifact.temporaryPath == "" {
			continue
		}
		if err := os.Remove(artifact.temporaryPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			removeErr = errors.Join(removeErr, fmt.Errorf("remove capacity report temporary file %q: %w", artifact.temporaryPath, err))
		}
	}
	return removeErr
}

func removeCapacityReportBackups(artifacts []capacityReportArtifact) error {
	var removeErr error
	for _, artifact := range artifacts {
		if artifact.backupPath == "" {
			continue
		}
		if err := os.Remove(artifact.backupPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			removeErr = errors.Join(removeErr, fmt.Errorf("remove capacity report backup %q: %w", artifact.backupPath, err))
		}
	}
	return removeErr
}
