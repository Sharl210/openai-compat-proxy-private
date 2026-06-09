package tokenestimator

import "testing"

func TestBucketKeyIncludesProviderEndpointAndModel(t *testing.T) {
	key := BucketKey{
		ProviderID:   "codex-2",
		EndpointType: "responses",
		Model:        "gpt-5.4",
	}
	if key.ProviderID != "codex-2" || key.EndpointType != "responses" || key.Model != "gpt-5.4" {
		t.Fatalf("unexpected bucket key: %#v", key)
	}
}

func TestSafeModelNameIsStableAndNonEmpty(t *testing.T) {
	safe := SafeModelName("gpt-5.4:reasoning/high")
	if safe == "" {
		t.Fatal("expected non-empty safe model name")
	}
	if safe == "gpt-5.4:reasoning/high" {
		t.Fatal("expected filesystem-safe transformation")
	}
}

func TestEstimatorPathsFollowProviderEndpointModelTree(t *testing.T) {
	jsonPath, txtPath := BucketPaths("/tmp/providers", BucketKey{
		ProviderID:   "codex-2",
		EndpointType: "responses",
		Model:        "gpt-5.4",
	})
	if want := "/tmp/providers/Token_Estimator/SYSTEM_JSON_FILES/codex-2/responses/"; len(jsonPath) <= len(want) || jsonPath[:len(want)] != want {
		t.Fatalf("unexpected json path: %s", jsonPath)
	}
	if want := "/tmp/providers/Token_Estimator/codex-2/responses/"; len(txtPath) <= len(want) || txtPath[:len(want)] != want {
		t.Fatalf("unexpected txt path: %s", txtPath)
	}
}
