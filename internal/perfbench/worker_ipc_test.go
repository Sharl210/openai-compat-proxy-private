package perfbench

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
)

const (
	helperTokenEnvironment = "PERFBENCH_HELPER_TOKEN"
	resultFrameMarker      = "PERFBENCH_RESULT_V1 "
	maxResultFrameBytes    = 1 << 20
	maxResultHeaderBytes   = 64
)

func helperActivated(argvSentinel, environmentToken string) bool {
	return argvSentinel != "" && argvSentinel == environmentToken
}

func newHelperSentinel() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate helper sentinel: %w", err)
	}
	return hex.EncodeToString(random), nil
}

func workerEnvironment(inherited []string, token string) []string {
	controlled := make([]string, 0, 5)
	for _, entry := range inherited {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "SYSTEMROOT", "WINDIR":
			controlled = append(controlled, entry)
		}
	}
	return append(controlled,
		helperTokenEnvironment+"="+token,
		"GOGC=100",
		"GOMAXPROCS=4",
	)
}

func encodeWorkerResultFrame(result workerResult) ([]byte, error) {
	payload, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal worker result: %w", err)
	}
	if len(payload) > maxResultFrameBytes {
		return nil, fmt.Errorf("worker result is %d bytes, limit %d", len(payload), maxResultFrameBytes)
	}
	header := resultFrameMarker + strconv.Itoa(len(payload)) + "\n"
	return append([]byte(header), payload...), nil
}

func decodeWorkerResultFrame(reader io.Reader) (workerResult, error) {
	buffered := bufio.NewReaderSize(reader, maxResultHeaderBytes)
	header, err := buffered.ReadSlice('\n')
	if err != nil {
		return workerResult{}, fmt.Errorf("read worker result header: %w", err)
	}
	if len(header) > maxResultHeaderBytes || !bytes.HasPrefix(header, []byte(resultFrameMarker)) {
		return workerResult{}, errors.New("worker result frame marker missing")
	}
	lengthText := string(header[len(resultFrameMarker) : len(header)-1])
	payloadLength, err := strconv.Atoi(lengthText)
	if err != nil || payloadLength < 0 || payloadLength > maxResultFrameBytes {
		return workerResult{}, fmt.Errorf("invalid worker result length %q", lengthText)
	}
	if lengthText != strconv.Itoa(payloadLength) {
		return workerResult{}, fmt.Errorf("non-canonical worker result length %q", lengthText)
	}
	payload := make([]byte, payloadLength)
	if _, err := io.ReadFull(buffered, payload); err != nil {
		return workerResult{}, fmt.Errorf("read worker result payload: %w", err)
	}
	if _, err := buffered.ReadByte(); err != io.EOF {
		if err == nil {
			return workerResult{}, errors.New("worker result frame contains trailing data")
		}
		return workerResult{}, fmt.Errorf("check worker result frame end: %w", err)
	}
	var result workerResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return workerResult{}, fmt.Errorf("decode worker result payload: %w", err)
	}
	canonical, err := json.Marshal(result)
	if err != nil {
		return workerResult{}, fmt.Errorf("re-encode worker result payload: %w", err)
	}
	if !bytes.Equal(payload, canonical) {
		return workerResult{}, errors.New("worker result payload is not canonical")
	}
	return result, nil
}

func TestHelperActivation_requires_matching_argv_and_environment(t *testing.T) {
	// Given / When / Then
	if helperActivated("", "token") {
		t.Fatal("environment alone activated helper")
	}
	if helperActivated("token", "") {
		t.Fatal("argv alone activated helper")
	}
	if helperActivated("token-a", "token-b") {
		t.Fatal("mismatched activation tokens activated helper")
	}
	if !helperActivated("token", "token") {
		t.Fatal("matching argv and environment did not activate helper")
	}
}

func TestWorkerEnvironment_strips_inherited_helper_values(t *testing.T) {
	// Given
	inherited := []string{
		"PATH=/bin",
		"LD_PRELOAD=/tmp/inject.so",
		"GODEBUG=gctrace=1",
		"SYSTEMROOT=C:\\Windows",
		"PERFBENCH_HELPER_TOKEN=stale",
		"PERFBENCH_HELPER_PROCESS=1",
		"PERFBENCH_UNRELATED=stale",
	}

	// When
	environment := workerEnvironment(inherited, "fresh")

	// Then
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "PATH=/bin") || strings.Contains(joined, "LD_PRELOAD") || strings.Contains(joined, "GODEBUG") {
		t.Fatalf("non-allowlisted environment survived: %q", joined)
	}
	if !strings.Contains(joined, "SYSTEMROOT=C:\\Windows") {
		t.Fatal("required Windows environment was removed")
	}
	if strings.Contains(joined, "stale") || strings.Contains(joined, "PERFBENCH_HELPER_PROCESS") {
		t.Fatalf("inherited helper environment survived: %q", joined)
	}
	if strings.Count(joined, "PERFBENCH_HELPER_TOKEN=fresh") != 1 {
		t.Fatalf("controlled helper token missing or duplicated: %q", joined)
	}
}

func TestWorkerResultFrame_requires_exactly_one_clean_frame(t *testing.T) {
	// Given
	want := workerResult{ScenarioID: "scenario-v1"}
	frame, err := encodeWorkerResultFrame(want)
	if err != nil {
		t.Fatalf("encode result frame: %v", err)
	}

	// When / Then
	got, err := decodeWorkerResultFrame(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("decode result frame: %v", err)
	}
	if got.ScenarioID != want.ScenarioID {
		t.Fatalf("scenario ID = %q, want %q", got.ScenarioID, want.ScenarioID)
	}
	for name, polluted := range map[string][]byte{
		"leading":  append([]byte("noise"), frame...),
		"trailing": append(append([]byte(nil), frame...), []byte("noise")...),
		"multiple": append(append([]byte(nil), frame...), frame...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeWorkerResultFrame(bytes.NewReader(polluted)); err == nil {
				t.Fatal("polluted result frame was accepted")
			}
		})
	}
}

func TestWorkerResultFrame_rejects_header_padding_that_hides_trailing_data(t *testing.T) {
	// Given
	probe, err := json.Marshal(workerResult{Error: "x"})
	if err != nil {
		t.Fatalf("marshal probe result: %v", err)
	}
	errorLength := maxResultFrameBytes - len(probe) + 1
	payload, err := json.Marshal(workerResult{Error: strings.Repeat("x", errorLength)})
	if err != nil {
		t.Fatalf("marshal maximum result: %v", err)
	}
	if len(payload) != maxResultFrameBytes {
		t.Fatalf("payload bytes = %d, want %d", len(payload), maxResultFrameBytes)
	}
	length := strconv.Itoa(len(payload))
	zeroCount := 255 - len(resultFrameMarker) - len(length)
	framed := []byte(resultFrameMarker + strings.Repeat("0", zeroCount) + length + "\n")
	framed = append(framed, payload...)
	framed = append(framed, []byte("trailing pollution")...)

	// When
	_, err = decodeWorkerResultFrame(bytes.NewReader(framed))

	// Then
	if err == nil {
		t.Fatal("padded length header hid trailing pollution")
	}
}
