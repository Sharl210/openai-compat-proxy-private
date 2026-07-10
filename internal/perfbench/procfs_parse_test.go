package perfbench

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"testing"
)

var errProcessMemoryUnsupported = errors.New("process memory metrics unsupported")

type processMemory struct {
	RssAnon uint64
	VmRSS   uint64
}

func parseProcessStatus(reader io.Reader) (processMemory, error) {
	var memory processMemory
	var seenRssAnon bool
	var seenVmRSS bool
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || (fields[0] != "RssAnon:" && fields[0] != "VmRSS:") {
			continue
		}
		if len(fields) != 3 || fields[2] != "kB" {
			return processMemory{}, fmt.Errorf("invalid %s memory field %q", fields[0], scanner.Text())
		}
		kilobytes, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return processMemory{}, fmt.Errorf("parse %s: %w", fields[0], err)
		}
		if kilobytes > math.MaxUint64/1024 {
			return processMemory{}, fmt.Errorf("%s overflows bytes", fields[0])
		}
		switch fields[0] {
		case "RssAnon:":
			if seenRssAnon {
				return processMemory{}, errors.New("duplicate RssAnon field")
			}
			seenRssAnon = true
			memory.RssAnon = kilobytes * 1024
		case "VmRSS:":
			if seenVmRSS {
				return processMemory{}, errors.New("duplicate VmRSS field")
			}
			seenVmRSS = true
			memory.VmRSS = kilobytes * 1024
		}
	}
	if err := scanner.Err(); err != nil {
		return processMemory{}, fmt.Errorf("scan process status: %w", err)
	}
	if !seenRssAnon || !seenVmRSS {
		return processMemory{}, fmt.Errorf("missing process memory fields: RssAnon=%t VmRSS=%t", seenRssAnon, seenVmRSS)
	}
	return memory, nil
}

func TestProcessStatusParser_reads_required_kilobyte_fields(t *testing.T) {
	// Given
	status := "Name:\tworker\nVmRSS:\t123 kB\nRssAnon:\t45 kB\n"

	// When
	memory, err := parseProcessStatus(strings.NewReader(status))

	// Then
	if err != nil {
		t.Fatalf("parse process status: %v", err)
	}
	if memory.VmRSS != 123*1024 || memory.RssAnon != 45*1024 {
		t.Fatalf("process memory = %+v", memory)
	}
}

func TestProcessStatusParser_rejects_invalid_contract(t *testing.T) {
	for name, status := range map[string]string{
		"unit":     "VmRSS: 1 MB\nRssAnon: 1 kB\n",
		"missing":  "VmRSS: 1 kB\n",
		"overflow": "VmRSS: 18014398509481984 kB\nRssAnon: 1 kB\n",
	} {
		t.Run(name, func(t *testing.T) {
			// When
			_, err := parseProcessStatus(strings.NewReader(status))

			// Then
			if err == nil {
				t.Fatal("invalid process status was accepted")
			}
		})
	}
}

func TestReadProcessMemory_reports_support_explicitly(t *testing.T) {
	// When
	memory, supported, err := readProcessMemory()

	// Then
	if errors.Is(err, errProcessMemoryUnsupported) {
		if supported {
			t.Fatal("unsupported platform reported process memory support")
		}
		return
	}
	if err != nil {
		t.Fatalf("read process memory: %v", err)
	}
	if !supported || memory.VmRSS == 0 || memory.RssAnon == 0 {
		t.Fatalf("supported process memory = %+v, supported=%t", memory, supported)
	}
}
