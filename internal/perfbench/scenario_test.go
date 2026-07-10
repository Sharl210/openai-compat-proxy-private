package perfbench

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

const scenarioCatalogVersion = "v1"

type downstreamProtocol string

const (
	downstreamResponses downstreamProtocol = "responses"
	downstreamChat      downstreamProtocol = "chat"
	downstreamMessages  downstreamProtocol = "messages"
)

type upstreamProtocol string

const (
	upstreamResponses upstreamProtocol = "responses"
	upstreamChat      upstreamProtocol = "chat"
	upstreamAnthropic upstreamProtocol = "anthropic"
)

type deliveryMode string

const (
	deliveryStream            deliveryMode = "stream"
	deliveryProxyBuffer       deliveryMode = "proxy_buffer"
	deliveryUpstreamNonStream deliveryMode = "upstream_non_stream"
)

type scenarioProfile string

const (
	profilePlain          scenarioProfile = "plain"
	profileLog            scenarioProfile = "log"
	profileLogArchive     scenarioProfile = "log_archive"
	profileRetryOnce      scenarioProfile = "retry_once"
	profileHistoryRestore scenarioProfile = "history_restore"
)

type scenario struct {
	ID         string             `json:"id"`
	Downstream downstreamProtocol `json:"downstream"`
	Upstream   upstreamProtocol   `json:"upstream"`
	Delivery   deliveryMode       `json:"delivery"`
	ImageBytes int64              `json:"image_bytes"`
	Profile    scenarioProfile    `json:"profile"`
}

func scenarioCatalog() []scenario {
	downstreams := [...]downstreamProtocol{downstreamResponses, downstreamChat, downstreamMessages}
	upstreams := [...]upstreamProtocol{upstreamResponses, upstreamChat, upstreamAnthropic}
	deliveries := [...]deliveryMode{deliveryStream, deliveryProxyBuffer, deliveryUpstreamNonStream}
	images := [...]struct {
		label string
		bytes int64
	}{{"1mib", 1 << 20}, {"8mib", 8 << 20}, {"32mib", 32 << 20}}
	profiles := [...]scenarioProfile{
		profilePlain,
		profileLog,
		profileLogArchive,
		profileRetryOnce,
		profileHistoryRestore,
	}

	catalog := make([]scenario, 0, 342)
	for _, downstream := range downstreams {
		for _, upstream := range upstreams {
			for _, delivery := range deliveries {
				for _, image := range images {
					for _, profile := range profiles {
						if profile == profileHistoryRestore &&
							(downstream != downstreamResponses || upstream == upstreamResponses) {
							continue
						}
						catalog = append(catalog, scenario{
							ID: fmt.Sprintf("%s-%s-%s-%s-%s",
								downstream, upstream, delivery, image.label, profile),
							Downstream: downstream,
							Upstream:   upstream,
							Delivery:   delivery,
							ImageBytes: image.bytes,
							Profile:    profile,
						})
					}
				}
			}
		}
	}
	return catalog
}

func scenarioCatalogCanonicalDigest(catalog []scenario) (string, string) {
	var canonical strings.Builder
	canonical.WriteString(scenarioCatalogVersion)
	canonical.WriteByte('\n')
	for _, item := range catalog {
		canonical.WriteString(item.ID)
		canonical.WriteByte('|')
		canonical.WriteString(string(item.Downstream))
		canonical.WriteByte('|')
		canonical.WriteString(string(item.Upstream))
		canonical.WriteByte('|')
		canonical.WriteString(string(item.Delivery))
		canonical.WriteByte('|')
		canonical.WriteString(strconv.FormatInt(item.ImageBytes, 10))
		canonical.WriteByte('|')
		canonical.WriteString(string(item.Profile))
		canonical.WriteByte('\n')
	}
	digest := sha256.Sum256([]byte(canonical.String()))
	return scenarioCatalogVersion, hex.EncodeToString(digest[:])
}

func TestScenarioMatrix_is_complete_and_stable(t *testing.T) {
	// Given
	const expectedCount = 342
	const expectedVersion = "v1"
	const expectedDigest = "e39cf48dbe9bbaddf461dd57b3576ae5f85228e3ac4c4680b391cc7d1d07a229"

	// When
	first := scenarioCatalog()
	second := scenarioCatalog()

	// Then
	if len(first) != expectedCount {
		t.Fatalf("scenario count = %d, want %d", len(first), expectedCount)
	}
	if len(second) != len(first) {
		t.Fatalf("second scenario count = %d, want %d", len(second), len(first))
	}
	if first[0].ID != "responses-responses-stream-1mib-plain" {
		t.Fatalf("first scenario ID = %q", first[0].ID)
	}
	if first[len(first)-1].ID != "messages-anthropic-upstream_non_stream-32mib-retry_once" {
		t.Fatalf("last scenario ID = %q", first[len(first)-1].ID)
	}

	seen := make(map[string]struct{}, len(first))
	historyCount := 0
	for i, scenario := range first {
		if scenario != second[i] {
			t.Fatalf("scenario %d changed between catalog calls", i)
		}
		if _, duplicate := seen[scenario.ID]; duplicate {
			t.Fatalf("duplicate scenario ID %q", scenario.ID)
		}
		seen[scenario.ID] = struct{}{}
		if scenario.Profile == profileHistoryRestore {
			historyCount++
			if scenario.Downstream != downstreamResponses || scenario.Upstream == upstreamResponses {
				t.Fatalf("history_restore used by unsupported scenario %q", scenario.ID)
			}
		}
	}
	if historyCount != 18 {
		t.Fatalf("history_restore count = %d, want 18", historyCount)
	}
	version, digest := scenarioCatalogCanonicalDigest(first)
	if version != expectedVersion || digest != expectedDigest {
		t.Fatalf("catalog identity = %s/%s, want %s/%s",
			version, digest, expectedVersion, expectedDigest)
	}
}
