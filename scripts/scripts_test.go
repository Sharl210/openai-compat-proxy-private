package scripts_test

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDeployFailsPreflightWithoutTouchingRunningProcess(t *testing.T) {
	repoDir := newScriptTestRepo(t)
	port := mustReservePort(t)
	goodProviders := filepath.Join(repoDir, "providers-ok")
	mustWriteFile(t, filepath.Join(goodProviders, "openai.env"), "PROVIDER_ID=openai\n")
	mustWriteEnv(t, repoDir, fmt.Sprintf("LISTEN_ADDR=127.0.0.1:%d\nPROVIDERS_DIR=%s\n", port, filepath.Join(repoDir, "providers-missing")))
	mustBuildFakeProxy(t, repoDir)

	oldCmd := startFakeProxy(t, repoDir, map[string]string{
		"LISTEN_ADDR":      fmt.Sprintf("127.0.0.1:%d", port),
		"PROVIDERS_DIR":    goodProviders,
		"IGNORE_TERM":      "1",
		"PROXY_API_KEY":    "root-secret",
		"DEFAULT_PROVIDER": "openai",
	})
	oldPID := oldCmd.Process.Pid
	mustWriteFile(t, filepath.Join(repoDir, ".proxy.pid"), strconv.Itoa(oldPID)+"\n")
	mustWaitHealth(t, port)

	result := runScript(t, repoDir, "deploy-linux.sh")
	if result.err == nil {
		t.Fatalf("expected deploy preflight to fail when providers dir missing, stdout=%s stderr=%s", result.stdout, result.stderr)
	}
	if !processAlive(oldPID) {
		t.Fatalf("expected old process %d to remain alive after deploy preflight failure", oldPID)
	}
	pidText := strings.TrimSpace(mustReadFile(t, filepath.Join(repoDir, ".proxy.pid")))
	if pidText != strconv.Itoa(oldPID) {
		t.Fatalf("expected pid file to remain on old pid %d, got %q", oldPID, pidText)
	}
	stopProcess(t, oldCmd)
	_ = os.Remove(filepath.Join(repoDir, ".proxy.pid"))
}

func TestDeployUsesListenHostForHealthCheck(t *testing.T) {
	repoDir := newScriptTestRepo(t)
	port := mustReservePortOnHost(t, "127.0.0.2")
	providersDir := filepath.Join(repoDir, "providers")
	mustWriteFile(t, filepath.Join(providersDir, "openai.env"), "PROVIDER_ID=openai\n")
	mustWriteEnv(t, repoDir, fmt.Sprintf("LISTEN_ADDR=127.0.0.2:%d\nPROVIDERS_DIR=%s\n", port, providersDir))
	mustBuildFakeProxy(t, repoDir)

	result := runScript(t, repoDir, "deploy-linux.sh")
	if result.err != nil {
		t.Fatalf("expected deploy to succeed for loopback host alias, err=%v stdout=%s stderr=%s", result.err, result.stdout, result.stderr)
	}
	newPIDText := strings.TrimSpace(mustReadFile(t, filepath.Join(repoDir, ".proxy.pid")))
	newPID, err := strconv.Atoi(newPIDText)
	if err != nil {
		t.Fatalf("parse new pid: %v text=%q", err, newPIDText)
	}
	if !processAlive(newPID) {
		t.Fatalf("expected deployed pid %d to stay alive", newPID)
	}
	mustWaitHealthOnHost(t, "127.0.0.2", port)
	stopPID(t, newPID)
	_ = os.Remove(filepath.Join(repoDir, ".proxy.pid"))
}

func TestDeployPassesCurrentLogEnvNamesToProcess(t *testing.T) {
	repoDir := newScriptTestRepo(t)
	port := mustReservePort(t)
	providersDir := filepath.Join(repoDir, "providers")
	mustWriteFile(t, filepath.Join(providersDir, "openai.env"), "PROVIDER_ID=openai\n")
	mustWriteEnv(t, repoDir, fmt.Sprintf("LISTEN_ADDR=127.0.0.1:%d\nPROVIDERS_DIR=%s\nLOG_FILE_PATH=.proxy.requests.jsonl\nLOG_MAX_BODY_SIZE_MB=7\nLOG_MAX_REQUESTS=99\n", port, providersDir))
	mustBuildFakeProxy(t, repoDir)

	result := runScript(t, repoDir, "deploy-linux.sh")
	if result.err != nil {
		t.Fatalf("expected deploy to succeed, err=%v stdout=%s stderr=%s", result.err, result.stdout, result.stderr)
	}

	seenEnv := mustReadFile(t, filepath.Join(repoDir, ".seen.env"))
	for _, want := range []string{
		"LOG_FILE_PATH=.proxy.requests.jsonl",
		"LOG_MAX_BODY_SIZE_MB=7",
		"LOG_MAX_REQUESTS=99",
	} {
		if !strings.Contains(seenEnv, want) {
			t.Fatalf("expected deployed process env to contain %q, got %s", want, seenEnv)
		}
	}
	for _, legacy := range []string{"LOG_INCLUDE_BODIES=", "LOG_MAX_SIZE_MB=", "LOG_MAX_BACKUPS="} {
		if strings.Contains(seenEnv, legacy) {
			t.Fatalf("expected deployed process env to omit legacy log variable %q, got %s", legacy, seenEnv)
		}
	}

	newPIDText := strings.TrimSpace(mustReadFile(t, filepath.Join(repoDir, ".proxy.pid")))
	newPID, err := strconv.Atoi(newPIDText)
	if err != nil {
		t.Fatalf("parse new pid: %v text=%q", err, newPIDText)
	}
	stopPID(t, newPID)
	_ = os.Remove(filepath.Join(repoDir, ".proxy.pid"))
}

func TestRuntimeScriptDoesNotReferenceLegacyLogVariables(t *testing.T) {
	repoDir := newScriptTestRepo(t)
	runtimeScript := mustReadFile(t, filepath.Join(repoDir, "scripts", "lib", "runtime.sh"))
	for _, legacy := range []string{"LOG_INCLUDE_BODIES", "LOG_MAX_SIZE_MB", "LOG_MAX_BACKUPS"} {
		if strings.Contains(runtimeScript, legacy) {
			t.Fatalf("expected runtime.sh to stop referencing legacy log variable %q", legacy)
		}
	}
}

func TestDeployFailsWhenThirdPartyOccupiesPort(t *testing.T) {
	repoDir := newScriptTestRepo(t)
	port := mustReservePort(t)
	providersDir := filepath.Join(repoDir, "providers")
	mustWriteFile(t, filepath.Join(providersDir, "openai.env"), "PROVIDER_ID=openai\n")
	mustWriteEnv(t, repoDir, fmt.Sprintf("LISTEN_ADDR=127.0.0.1:%d\nPROVIDERS_DIR=%s\n", port, providersDir))
	mustBuildFakeProxy(t, repoDir)

	blocker := mustListen(t, fmt.Sprintf("127.0.0.1:%d", port))
	defer blocker.Close()

	result := runScript(t, repoDir, "deploy-linux.sh")
	if result.err == nil {
		t.Fatalf("expected deploy to fail when third-party listener occupies port, stdout=%s stderr=%s", result.stdout, result.stderr)
	}
	if !strings.Contains(result.stderr, fmt.Sprintf("service port %d is occupied by another process", port)) {
		t.Fatalf("expected occupied-port error, got stdout=%s stderr=%s", result.stdout, result.stderr)
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".proxy.pid")); !os.IsNotExist(err) {
		t.Fatalf("expected no pid file after occupied-port failure, got err=%v", err)
	}
}

func TestRestartReplacesStubbornOldProcess(t *testing.T) {
	repoDir := newScriptTestRepo(t)
	port := mustReservePort(t)
	providersDir := filepath.Join(repoDir, "providers")
	mustWriteFile(t, filepath.Join(providersDir, "openai.env"), "PROVIDER_ID=openai\n")
	mustWriteEnv(t, repoDir, fmt.Sprintf("LISTEN_ADDR=127.0.0.1:%d\nPROVIDERS_DIR=%s\n", port, providersDir))
	mustBuildFakeProxy(t, repoDir)

	oldCmd := startFakeProxy(t, repoDir, map[string]string{
		"LISTEN_ADDR":   fmt.Sprintf("127.0.0.1:%d", port),
		"PROVIDERS_DIR": providersDir,
		"IGNORE_TERM":   "1",
	})
	oldPID := oldCmd.Process.Pid
	mustWriteFile(t, filepath.Join(repoDir, ".proxy.pid"), strconv.Itoa(oldPID)+"\n")
	mustWaitHealth(t, port)

	result := runScript(t, repoDir, "restart-linux.sh")
	if result.err != nil {
		t.Fatalf("expected restart to succeed, err=%v stdout=%s stderr=%s", result.err, result.stdout, result.stderr)
	}
	newPIDText := strings.TrimSpace(mustReadFile(t, filepath.Join(repoDir, ".proxy.pid")))
	newPID, err := strconv.Atoi(newPIDText)
	if err != nil {
		t.Fatalf("parse new pid: %v text=%q", err, newPIDText)
	}
	if newPID == oldPID {
		t.Fatalf("expected restart to replace old pid %d, got same pid", oldPID)
	}
	if processAlive(oldPID) {
		t.Fatalf("expected old process %d to be gone after restart", oldPID)
	}
	if !processAlive(newPID) {
		t.Fatalf("expected new process %d to be alive after restart", newPID)
	}
	mustWaitHealth(t, port)
	stopPID(t, newPID)
	_ = os.Remove(filepath.Join(repoDir, ".proxy.pid"))
}

func TestRestartStopsManagedServiceBeforePreflightFailure(t *testing.T) {
	repoDir := newScriptTestRepo(t)
	port := mustReservePort(t)
	goodProviders := filepath.Join(repoDir, "providers-ok")
	mustWriteFile(t, filepath.Join(goodProviders, "openai.env"), "PROVIDER_ID=openai\n")
	mustWriteEnv(t, repoDir, fmt.Sprintf("LISTEN_ADDR=127.0.0.1:%d\nPROVIDERS_DIR=%s\n", port, goodProviders))
	mustBuildFakeProxy(t, repoDir)

	oldCmd := startFakeProxy(t, repoDir, map[string]string{
		"LISTEN_ADDR":   fmt.Sprintf("127.0.0.1:%d", port),
		"PROVIDERS_DIR": goodProviders,
		"IGNORE_TERM":   "1",
	})
	oldPID := oldCmd.Process.Pid
	mustWriteFile(t, filepath.Join(repoDir, ".proxy.pid"), strconv.Itoa(oldPID)+"\n")
	mustWaitHealth(t, port)

	mustWriteEnv(t, repoDir, fmt.Sprintf("LISTEN_ADDR=127.0.0.1:%d\nPROVIDERS_DIR=%s\n", port, filepath.Join(repoDir, "providers-missing")))

	result := runScript(t, repoDir, "restart-linux.sh")
	if result.err == nil {
		t.Fatalf("expected restart to fail on missing providers after stop, stdout=%s stderr=%s", result.stdout, result.stderr)
	}
	if processAlive(oldPID) {
		t.Fatalf("expected old process %d to be stopped before restart preflight failure", oldPID)
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".proxy.pid")); !os.IsNotExist(err) {
		t.Fatalf("expected pid file removed after restart stop phase, got err=%v", err)
	}
	_, _ = oldCmd.Process.Wait()
}

func TestStopLinuxStopsStubbornProcessAndKeepsArtifacts(t *testing.T) {
	repoDir := newScriptTestRepo(t)
	port := mustReservePort(t)
	providersDir := filepath.Join(repoDir, "providers")
	mustWriteFile(t, filepath.Join(providersDir, "openai.env"), "PROVIDER_ID=openai\n")
	mustWriteEnv(t, repoDir, fmt.Sprintf("LISTEN_ADDR=127.0.0.1:%d\nPROVIDERS_DIR=%s\n", port, providersDir))
	mustBuildFakeProxy(t, repoDir)
	mustWriteFile(t, filepath.Join(repoDir, ".proxy.log"), "hello\n")

	oldCmd := startFakeProxy(t, repoDir, map[string]string{
		"LISTEN_ADDR":   fmt.Sprintf("127.0.0.1:%d", port),
		"PROVIDERS_DIR": providersDir,
		"IGNORE_TERM":   "1",
	})
	oldPID := oldCmd.Process.Pid
	mustWriteFile(t, filepath.Join(repoDir, ".proxy.pid"), strconv.Itoa(oldPID)+"\n")
	mustWaitHealth(t, port)

	result := runScript(t, repoDir, "stop-linux.sh")
	if result.err != nil {
		t.Fatalf("expected stop to succeed, err=%v stdout=%s stderr=%s", result.err, result.stdout, result.stderr)
	}
	if processAlive(oldPID) {
		t.Fatalf("expected old process %d to be fully stopped", oldPID)
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".proxy.pid")); !os.IsNotExist(err) {
		t.Fatalf("expected pid file removed, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".proxy.log")); err != nil {
		t.Fatalf("expected log file retained by stop script, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "bin", "openai-compat-proxy")); err != nil {
		t.Fatalf("expected binary retained by stop script, err=%v", err)
	}
}

func TestStopLinuxStopsWithoutEnvFileUsingPidFile(t *testing.T) {
	repoDir := newScriptTestRepo(t)
	port := mustReservePort(t)
	providersDir := filepath.Join(repoDir, "providers")
	mustWriteFile(t, filepath.Join(providersDir, "openai.env"), "PROVIDER_ID=openai\n")
	mustWriteEnv(t, repoDir, fmt.Sprintf("LISTEN_ADDR=127.0.0.1:%d\nPROVIDERS_DIR=%s\n", port, providersDir))
	mustBuildFakeProxy(t, repoDir)
	mustWriteFile(t, filepath.Join(repoDir, ".proxy.log"), "hello\n")

	oldCmd := startFakeProxy(t, repoDir, map[string]string{
		"LISTEN_ADDR":   fmt.Sprintf("127.0.0.1:%d", port),
		"PROVIDERS_DIR": providersDir,
		"IGNORE_TERM":   "1",
	})
	oldPID := oldCmd.Process.Pid
	mustWriteFile(t, filepath.Join(repoDir, ".proxy.pid"), strconv.Itoa(oldPID)+"\n")
	mustWaitHealth(t, port)
	if err := os.Remove(filepath.Join(repoDir, ".env")); err != nil {
		t.Fatalf("remove env: %v", err)
	}

	result := runScript(t, repoDir, "stop-linux.sh")
	if result.err != nil {
		t.Fatalf("expected stop without env to succeed, err=%v stdout=%s stderr=%s", result.err, result.stdout, result.stderr)
	}
	if processAlive(oldPID) {
		t.Fatalf("expected old process %d to be fully stopped without env", oldPID)
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".proxy.pid")); !os.IsNotExist(err) {
		t.Fatalf("expected pid file removed, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".proxy.log")); err != nil {
		t.Fatalf("expected log file retained by stop script, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "bin", "openai-compat-proxy")); err != nil {
		t.Fatalf("expected binary retained by stop script, err=%v", err)
	}
	_, _ = oldCmd.Process.Wait()
}

func TestStopLinuxRecoversFromStaleLock(t *testing.T) {
	repoDir := newScriptTestRepo(t)
	port := mustReservePort(t)
	providersDir := filepath.Join(repoDir, "providers")
	mustWriteFile(t, filepath.Join(providersDir, "openai.env"), "PROVIDER_ID=openai\n")
	mustWriteEnv(t, repoDir, fmt.Sprintf("LISTEN_ADDR=127.0.0.1:%d\nPROVIDERS_DIR=%s\n", port, providersDir))
	mustBuildFakeProxy(t, repoDir)

	oldCmd := startFakeProxy(t, repoDir, map[string]string{
		"LISTEN_ADDR":   fmt.Sprintf("127.0.0.1:%d", port),
		"PROVIDERS_DIR": providersDir,
		"IGNORE_TERM":   "1",
	})
	oldPID := oldCmd.Process.Pid
	mustWriteFile(t, filepath.Join(repoDir, ".proxy.pid"), strconv.Itoa(oldPID)+"\n")
	mustWaitHealth(t, port)
	mustWriteFile(t, filepath.Join(repoDir, ".proxy.lock", "pid"), "999999\n")

	result := runScript(t, repoDir, "stop-linux.sh")
	if result.err != nil {
		t.Fatalf("expected stop to recover from stale lock, err=%v stdout=%s stderr=%s", result.err, result.stdout, result.stderr)
	}
	if processAlive(oldPID) {
		t.Fatalf("expected old process %d to be stopped after stale lock recovery", oldPID)
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".proxy.lock")); !os.IsNotExist(err) {
		t.Fatalf("expected lock dir removed after stale lock recovery, got err=%v", err)
	}
	_, _ = oldCmd.Process.Wait()
}

func TestStopLinuxStopsRunningServiceAfterListenAddrPortChange(t *testing.T) {
	repoDir := newScriptTestRepo(t)
	oldPort := mustReservePort(t)
	newPort := mustReservePort(t)
	providersDir := filepath.Join(repoDir, "providers")
	mustWriteFile(t, filepath.Join(providersDir, "openai.env"), "PROVIDER_ID=openai\n")
	mustWriteEnv(t, repoDir, fmt.Sprintf("LISTEN_ADDR=127.0.0.1:%d\nPROVIDERS_DIR=%s\n", oldPort, providersDir))
	mustBuildFakeProxy(t, repoDir)

	oldCmd := startFakeProxy(t, repoDir, map[string]string{
		"LISTEN_ADDR":   fmt.Sprintf("127.0.0.1:%d", oldPort),
		"PROVIDERS_DIR": providersDir,
		"IGNORE_TERM":   "1",
	})
	oldPID := oldCmd.Process.Pid
	mustWaitHealth(t, oldPort)
	mustWriteFile(t, filepath.Join(repoDir, ".proxy.pid"), "999999\n")
	mustWriteEnv(t, repoDir, fmt.Sprintf("LISTEN_ADDR=127.0.0.1:%d\nPROVIDERS_DIR=%s\n", newPort, providersDir))

	result := runScript(t, repoDir, "stop-linux.sh")
	if result.err != nil {
		t.Fatalf("expected stop to find running service after listen port change, err=%v stdout=%s stderr=%s", result.err, result.stdout, result.stderr)
	}
	if processAlive(oldPID) {
		t.Fatalf("expected old process %d to be stopped after listen port change", oldPID)
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".proxy.pid")); !os.IsNotExist(err) {
		t.Fatalf("expected pid file removed, got err=%v", err)
	}
	_, _ = oldCmd.Process.Wait()
}

func TestUninstallStopsStubbornProcessBeforeCleanup(t *testing.T) {
	repoDir := newScriptTestRepo(t)
	port := mustReservePort(t)
	providersDir := filepath.Join(repoDir, "providers")
	mustWriteFile(t, filepath.Join(providersDir, "openai.env"), "PROVIDER_ID=openai\n")
	mustWriteEnv(t, repoDir, fmt.Sprintf("LISTEN_ADDR=127.0.0.1:%d\nPROVIDERS_DIR=%s\n", port, providersDir))
	mustBuildFakeProxy(t, repoDir)
	mustWriteFile(t, filepath.Join(repoDir, ".proxy.log"), "hello\n")

	oldCmd := startFakeProxy(t, repoDir, map[string]string{
		"LISTEN_ADDR":   fmt.Sprintf("127.0.0.1:%d", port),
		"PROVIDERS_DIR": providersDir,
		"IGNORE_TERM":   "1",
	})
	oldPID := oldCmd.Process.Pid
	mustWriteFile(t, filepath.Join(repoDir, ".proxy.pid"), strconv.Itoa(oldPID)+"\n")
	mustWaitHealth(t, port)

	result := runScript(t, repoDir, "uninstall-linux.sh")
	if result.err != nil {
		t.Fatalf("expected uninstall to succeed, err=%v stdout=%s stderr=%s", result.err, result.stdout, result.stderr)
	}
	if processAlive(oldPID) {
		t.Fatalf("expected uninstall to fully stop old process %d", oldPID)
	}
	for _, rel := range []string{".proxy.pid", ".proxy.log", filepath.Join("bin", "openai-compat-proxy")} {
		if _, err := os.Stat(filepath.Join(repoDir, rel)); !os.IsNotExist(err) {
			t.Fatalf("expected %s removed, err=%v", rel, err)
		}
	}
}

func TestUninstallStopsWithoutEnvFileUsingPidFile(t *testing.T) {
	repoDir := newScriptTestRepo(t)
	port := mustReservePort(t)
	providersDir := filepath.Join(repoDir, "providers")
	mustWriteFile(t, filepath.Join(providersDir, "openai.env"), "PROVIDER_ID=openai\n")
	mustWriteEnv(t, repoDir, fmt.Sprintf("LISTEN_ADDR=127.0.0.1:%d\nPROVIDERS_DIR=%s\n", port, providersDir))
	mustBuildFakeProxy(t, repoDir)
	mustWriteFile(t, filepath.Join(repoDir, ".proxy.log"), "hello\n")

	oldCmd := startFakeProxy(t, repoDir, map[string]string{
		"LISTEN_ADDR":   fmt.Sprintf("127.0.0.1:%d", port),
		"PROVIDERS_DIR": providersDir,
		"IGNORE_TERM":   "1",
	})
	oldPID := oldCmd.Process.Pid
	mustWriteFile(t, filepath.Join(repoDir, ".proxy.pid"), strconv.Itoa(oldPID)+"\n")
	mustWaitHealth(t, port)
	if err := os.Remove(filepath.Join(repoDir, ".env")); err != nil {
		t.Fatalf("remove env: %v", err)
	}

	result := runScript(t, repoDir, "uninstall-linux.sh")
	if result.err != nil {
		t.Fatalf("expected uninstall without env to succeed, err=%v stdout=%s stderr=%s", result.err, result.stdout, result.stderr)
	}
	if processAlive(oldPID) {
		t.Fatalf("expected uninstall to fully stop old process %d without env", oldPID)
	}
	for _, rel := range []string{".proxy.pid", ".proxy.log", filepath.Join("bin", "openai-compat-proxy")} {
		if _, err := os.Stat(filepath.Join(repoDir, rel)); !os.IsNotExist(err) {
			t.Fatalf("expected %s removed, err=%v", rel, err)
		}
	}
	_, _ = oldCmd.Process.Wait()
}

func TestStopLinuxStopsWithBrokenEnvUsingPidFileFallback(t *testing.T) {
	repoDir := newScriptTestRepo(t)
	port := mustReservePort(t)
	providersDir := filepath.Join(repoDir, "providers")
	mustWriteFile(t, filepath.Join(providersDir, "openai.env"), "PROVIDER_ID=openai\n")
	mustWriteEnv(t, repoDir, fmt.Sprintf("LISTEN_ADDR=127.0.0.1:%d\nPROVIDERS_DIR=%s\n", port, providersDir))
	mustBuildFakeProxy(t, repoDir)

	oldCmd := startFakeProxy(t, repoDir, map[string]string{
		"LISTEN_ADDR":   fmt.Sprintf("127.0.0.1:%d", port),
		"PROVIDERS_DIR": providersDir,
		"IGNORE_TERM":   "1",
	})
	oldPID := oldCmd.Process.Pid
	mustWriteFile(t, filepath.Join(repoDir, ".proxy.pid"), strconv.Itoa(oldPID)+"\n")
	mustWaitHealth(t, port)
	mustWriteFile(t, filepath.Join(repoDir, ".env"), "PROVIDERS_DIR=\n")

	result := runScript(t, repoDir, "stop-linux.sh")
	if result.err != nil {
		t.Fatalf("expected stop with broken env to succeed, err=%v stdout=%s stderr=%s", result.err, result.stdout, result.stderr)
	}
	if processAlive(oldPID) {
		t.Fatalf("expected old process %d to stop with broken env fallback", oldPID)
	}
	_, _ = oldCmd.Process.Wait()
}

func TestUninstallStopsWithBrokenEnvUsingPidFileFallback(t *testing.T) {
	repoDir := newScriptTestRepo(t)
	port := mustReservePort(t)
	providersDir := filepath.Join(repoDir, "providers")
	mustWriteFile(t, filepath.Join(providersDir, "openai.env"), "PROVIDER_ID=openai\n")
	mustWriteEnv(t, repoDir, fmt.Sprintf("LISTEN_ADDR=127.0.0.1:%d\nPROVIDERS_DIR=%s\n", port, providersDir))
	mustBuildFakeProxy(t, repoDir)
	mustWriteFile(t, filepath.Join(repoDir, ".proxy.log"), "hello\n")

	oldCmd := startFakeProxy(t, repoDir, map[string]string{
		"LISTEN_ADDR":   fmt.Sprintf("127.0.0.1:%d", port),
		"PROVIDERS_DIR": providersDir,
		"IGNORE_TERM":   "1",
	})
	oldPID := oldCmd.Process.Pid
	mustWriteFile(t, filepath.Join(repoDir, ".proxy.pid"), strconv.Itoa(oldPID)+"\n")
	mustWaitHealth(t, port)
	mustWriteFile(t, filepath.Join(repoDir, ".env"), "LISTEN_ADDR=\n")

	result := runScript(t, repoDir, "uninstall-linux.sh")
	if result.err != nil {
		t.Fatalf("expected uninstall with broken env to succeed, err=%v stdout=%s stderr=%s", result.err, result.stdout, result.stderr)
	}
	if processAlive(oldPID) {
		t.Fatalf("expected uninstall to stop old process %d with broken env fallback", oldPID)
	}
	for _, rel := range []string{".proxy.pid", ".proxy.log", filepath.Join("bin", "openai-compat-proxy")} {
		if _, err := os.Stat(filepath.Join(repoDir, rel)); !os.IsNotExist(err) {
			t.Fatalf("expected %s removed, err=%v", rel, err)
		}
	}
	_, _ = oldCmd.Process.Wait()
}

type scriptResult struct {
	stdout string
	stderr string
	err    error
}

func runScript(t *testing.T, repoDir string, scriptName string) scriptResult {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join(repoDir, "scripts", scriptName))
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "PATH="+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return scriptResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func newScriptTestRepo(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	_, currentFile, _, _ := runtime.Caller(0)
	sourceScriptsDir := filepath.Dir(currentFile)
	mustCopyDir(t, sourceScriptsDir, filepath.Join(repoDir, "scripts"))
	mustWriteFile(t, filepath.Join(repoDir, "go.mod"), "module scriptstest\n\ngo 1.22\n")
	mustWriteFile(t, filepath.Join(repoDir, "cmd", "proxy", "main.go"), fakeProxyMain)
	return repoDir
}

func mustBuildFakeProxy(t *testing.T, repoDir string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", filepath.Join(repoDir, "bin", "openai-compat-proxy"), "./cmd/proxy")
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fake proxy: %v\n%s", err, output)
	}
}

func startFakeProxy(t *testing.T, repoDir string, env map[string]string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(filepath.Join(repoDir, "bin", "openai-compat-proxy"))
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), envList(env)...)
	logFile, err := os.OpenFile(filepath.Join(repoDir, ".proxy.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open log file: %v", err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake proxy: %v", err)
	}
	t.Cleanup(func() {
		_ = logFile.Close()
	})
	return cmd
}

func mustWaitHealth(t *testing.T, port int) {
	t.Helper()
	mustWaitHealthOnHost(t, "127.0.0.1", port)
}

func mustWaitHealthOnHost(t *testing.T, host string, port int) {
	t.Helper()
	url := fmt.Sprintf("http://%s:%d/healthz", host, port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("health endpoint %s did not become ready", url)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err != nil {
		return false
	}
	cmd := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid))
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	state := strings.TrimSpace(string(output))
	return state != "" && !strings.HasPrefix(state, "Z")
}

func stopProcess(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if cmd == nil || cmd.Process == nil {
		return
	}
	stopPID(t, cmd.Process.Pid)
	_, _ = cmd.Process.Wait()
}

func stopPID(t *testing.T, pid int) {
	t.Helper()
	if pid <= 0 || !processAlive(pid) {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("failed to kill pid %d", pid)
}

func mustReservePort(t *testing.T) int {
	t.Helper()
	return mustReservePortOnHost(t, "127.0.0.1")
}

func mustReservePortOnHost(t *testing.T, host string) int {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func mustListen(t *testing.T, addr string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen on %s: %v", addr, err)
	}
	return ln
}

func mustWriteEnv(t *testing.T, repoDir string, content string) {
	t.Helper()
	mustWriteFile(t, filepath.Join(repoDir, ".env"), content)
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file %s: %v", path, err)
	}
	return string(data)
}

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}

func mustCopyDir(t *testing.T, src string, dst string) {
	t.Helper()
	if err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	}); err != nil {
		t.Fatalf("copy dir %s -> %s: %v", src, dst, err)
	}
}

func envList(overrides map[string]string) []string {
	var out []string
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}

const fakeProxyMain = `package main

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

func main() {
	if os.Getenv("IGNORE_TERM") == "1" {
		signal.Ignore(syscall.SIGTERM)
	}
	listenAddr := os.Getenv("LISTEN_ADDR")
	providersDir := os.Getenv("PROVIDERS_DIR")
	if listenAddr == "" {
		fmt.Fprintln(os.Stderr, "missing LISTEN_ADDR")
		os.Exit(11)
	}
	if providersDir == "" {
		fmt.Fprintln(os.Stderr, "missing PROVIDERS_DIR")
		os.Exit(12)
	}
	providerFiles, err := filepath.Glob(filepath.Join(providersDir, "*.env"))
	if err != nil || len(providerFiles) == 0 {
		fmt.Fprintln(os.Stderr, "missing provider env files")
		os.Exit(13)
	}
	tracked := []string{
		"LOG_ENABLE",
		"LOG_FILE_PATH",
		"LOG_INCLUDE_BODIES",
		"LOG_MAX_BODY_SIZE_MB",
		"LOG_MAX_REQUESTS",
		"LOG_MAX_SIZE_MB",
		"LOG_MAX_BACKUPS",
	}
	lines := make([]string, 0, len(tracked))
	for _, key := range tracked {
		if value := os.Getenv(key); value != "" {
			lines = append(lines, key+"="+value)
		}
	}
	sort.Strings(lines)
	_ = os.WriteFile(filepath.Join(".seen.env"), []byte(strings.Join(lines, "\n")), 0o644)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"status\":\"ok\"}"))
	})
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(14)
	}
}
`
