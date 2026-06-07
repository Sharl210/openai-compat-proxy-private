package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestResolveModelAndEffortPrefersRequestSuffixOverMappedSuffix(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{{Key: "gpt-5", Target: "claude-sonnet-4-5-low"}}}

	model, effort := p.ResolveModelAndEffort("gpt-5-high", true)
	if model != "claude-sonnet-4-5" {
		t.Fatalf("expected mapped model without suffix, got %q", model)
	}
	if effort != "high" {
		t.Fatalf("expected request suffix high to win, got %q", effort)
	}
}

func TestResolveModelAndEffortParsesMappedSuffixWhenClientSuffixDisabled(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{NewModelMapEntry("#re:.*", "claude-sonnet-4-5-low")}}

	model, effort := p.ResolveModelAndEffort("gpt-5-high", false)
	if model != "claude-sonnet-4-5" {
		t.Fatalf("expected mapped target suffix to be stripped even when client suffix is disabled, got %q", model)
	}
	if effort != "low" {
		t.Fatalf("expected mapped target suffix effort low, got %q", effort)
	}
}

func TestResolveModelAndEffortUsesMappedSuffixWhenNoRequestSuffix(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{{Key: "gpt-5", Target: "claude-sonnet-4-5-low"}}}

	model, effort := p.ResolveModelAndEffort("gpt-5", true)
	if model != "claude-sonnet-4-5" {
		t.Fatalf("expected mapped model without suffix, got %q", model)
	}
	if effort != "low" {
		t.Fatalf("expected mapped suffix low, got %q", effort)
	}
}

func TestResolveModelAndEffortSupportsNoneSuffix(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{{Key: "gpt-5", Target: "claude-sonnet-4-5-high"}}}

	model, effort := p.ResolveModelAndEffort("gpt-5-none", true)
	if model != "claude-sonnet-4-5" {
		t.Fatalf("expected mapped model without suffix, got %q", model)
	}
	if effort != "none" {
		t.Fatalf("expected request suffix none to win, got %q", effort)
	}
}

func TestResolveModelAndEffortSupportsMinimalSuffix(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{{Key: "gpt-5", Target: "claude-sonnet-4-5"}}}

	model, effort := p.ResolveModelAndEffort("gpt-5-minimal", true)
	if model != "claude-sonnet-4-5" {
		t.Fatalf("expected mapped model without suffix, got %q", model)
	}
	if effort != "minimal" {
		t.Fatalf("expected request suffix minimal to win, got %q", effort)
	}
}

func TestResolveModelAndEffortSupportsMaxSuffix(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{{Key: "gpt-5", Target: "claude-sonnet-4-5"}}}

	model, effort := p.ResolveModelAndEffort("gpt-5-max", true)
	if model != "claude-sonnet-4-5" {
		t.Fatalf("expected mapped model without suffix, got %q", model)
	}
	if effort != "max" {
		t.Fatalf("expected request suffix max to win, got %q", effort)
	}
}

func TestResolveModelAndEffortTreatsModelMapSourceAndTargetSuffixIndependently(t *testing.T) {
	tests := []struct {
		name       string
		entry      ModelMapEntry
		request    string
		wantModel  string
		wantEffort string
	}{
		{
			name:       "no suffix on either side",
			entry:      NewModelMapEntry("client-gpt", "upstream-gpt"),
			request:    "client-gpt",
			wantModel:  "upstream-gpt",
			wantEffort: "",
		},
		{
			name:       "source suffix selects rule while target has no suffix",
			entry:      NewModelMapEntry("client-gpt-high", "upstream-gpt"),
			request:    "client-gpt-high",
			wantModel:  "upstream-gpt",
			wantEffort: "high",
		},
		{
			name:       "target suffix sets effort when request has none",
			entry:      NewModelMapEntry("client-gpt", "upstream-gpt-low"),
			request:    "client-gpt",
			wantModel:  "upstream-gpt",
			wantEffort: "low",
		},
		{
			name:       "request suffix wins over target suffix",
			entry:      NewModelMapEntry("client-gpt-high", "upstream-gpt-low"),
			request:    "client-gpt-high",
			wantModel:  "upstream-gpt",
			wantEffort: "high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := ProviderConfig{ModelMap: []ModelMapEntry{tt.entry}}
			model, effort := p.ResolveModelAndEffort(tt.request, true)
			if model != tt.wantModel || effort != tt.wantEffort {
				t.Fatalf("expected %q/%q, got %q/%q", tt.wantModel, tt.wantEffort, model, effort)
			}
		})
	}
}

func TestResolveModelDoesNotDirectlyMatchBaseSourceForSuffixedRequest(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{NewModelMapEntry("client-gpt", "upstream-gpt")}}

	mapped := p.resolveModel("client-gpt-high")
	if mapped != "" {
		t.Fatalf("expected direct MODEL_MAP source not to match before suffix fallback, got %q", mapped)
	}
}

func TestResolveModelAndEffortFallsBackToBaseSourceAfterRequestSuffixParsing(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{NewModelMapEntry("client-gpt", "upstream-gpt")}}

	model, effort := p.ResolveModelAndEffort("client-gpt-high", true)
	if model != "upstream-gpt" || effort != "high" {
		t.Fatalf("expected base source mapping to preserve request suffix effort, got %q/%q", model, effort)
	}
}

func TestResolveModelAndEffortWithRequestEffortMatchesSuffixedSource(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{NewModelMapEntry("client-gpt-high", "upstream-gpt")}}

	model, effort := p.ResolveModelAndEffortWithRequestEffort("client-gpt", "high", false)
	if model != "upstream-gpt" || effort != "high" {
		t.Fatalf("expected explicit effort to match suffixed source, got %q/%q", model, effort)
	}
}

func TestResolveModelAndEffortWithRequestEffortPrefersSuffixedSourceOverBaseSource(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{
		NewModelMapEntry("client-gpt-high", "upstream-priority"),
		NewModelMapEntry("client-gpt", "upstream-base"),
	}}

	model, effort := p.ResolveModelAndEffortWithRequestEffort("client-gpt", "high", false)
	if model != "upstream-priority" || effort != "high" {
		t.Fatalf("expected explicit effort to prefer suffixed source, got %q/%q", model, effort)
	}
}

func TestResolveModelAndEffortWithRequestEffortMatchesRegexSuffixedSource(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{NewModelMapEntry("#re:client-(.*)-high", "upstream-$1")}}

	model, effort := p.ResolveModelAndEffortWithRequestEffort("client-gpt", "high", false)
	if model != "upstream-gpt" || effort != "high" {
		t.Fatalf("expected explicit effort to match regex suffixed source, got %q/%q", model, effort)
	}
}

func TestResolveModelAndEffortWithRequestEffortIgnoresUnknownEffort(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{NewModelMapEntry("client-gpt-fast", "upstream-gpt")}}

	model, effort := p.ResolveModelAndEffortWithRequestEffort("client-gpt", "fast", false)
	if model != "client-gpt" || effort != "" {
		t.Fatalf("expected unknown effort not to synthesize MODEL_MAP source, got %q/%q", model, effort)
	}
}

func TestResolveModelAndEffortWithRequestEffortDoesNotAffectManualOrHiddenModelLists(t *testing.T) {
	p := ProviderConfig{
		ManualModels: []string{"client-gpt"},
		HiddenModels: []string{"client-gpt-high"},
	}

	model, effort := p.ResolveModelAndEffortWithRequestEffort("client-gpt", "high", false)
	if model != "client-gpt" || effort != "" {
		t.Fatalf("expected request effort not to change model list controls or create override without MODEL_MAP, got %q/%q", model, effort)
	}
	if p.HidesModel("client-gpt") {
		t.Fatalf("expected hidden suffixed model not to hide base model")
	}
}

func TestResolveModelAndEffortKeepsUnmappedSuffixedClientModelWhenSuffixDisabled(t *testing.T) {
	p := ProviderConfig{}

	model, effort := p.ResolveModelAndEffortWithRequestEffort("client-gpt-high", "", false)
	if model != "client-gpt-high" || effort != "" {
		t.Fatalf("expected disabled client suffix to remain a literal model without inferred effort, got %q/%q", model, effort)
	}
}

func TestResolveModelAndEffortManualReasonSuffixWorksWhenSuffixFlagDisabled(t *testing.T) {
	p := ProviderConfig{ManualModels: []string{"#reason_suffix:gpt-5.5"}}

	model, effort := p.ResolveModelAndEffort("gpt-5.5-minimal", false)
	if model != "gpt-5.5" {
		t.Fatalf("expected manual reason suffix family to strip to base model, got %q", model)
	}
	if effort != "minimal" {
		t.Fatalf("expected manual reason suffix family to resolve minimal effort, got %q", effort)
	}
}

func TestResolveModelAndEffortLiteralManualSuffixOverridesFamilyWhenSuffixFlagDisabled(t *testing.T) {
	p := ProviderConfig{ManualModels: []string{"#reason_suffix:gpt-5.5", "gpt-5.5-low"}}

	model, effort := p.ResolveModelAndEffort("gpt-5.5-low", false)
	if model != "gpt-5.5-low" {
		t.Fatalf("expected literal manual suffix model to stay intact when suffix flag is disabled, got %q", model)
	}
	if effort != "" {
		t.Fatalf("expected literal manual suffix model to avoid effort parsing, got %q", effort)
	}
}

func TestResolveModelAndEffortParsesLiteralManualSuffixWhenSuffixFlagEnabled(t *testing.T) {
	p := ProviderConfig{ManualModels: []string{"#reason_suffix:gpt-5.5", "gpt-5.5-low"}}

	model, effort := p.ResolveModelAndEffort("gpt-5.5-low", true)
	if model != "gpt-5.5" {
		t.Fatalf("expected suffix-enabled literal manual model to strip to base model, got %q", model)
	}
	if effort != "low" {
		t.Fatalf("expected suffix-enabled literal manual model to resolve low effort, got %q", effort)
	}
}

func TestVisibleModelIDsExpandsManualReasonSuffixFamilyIndependentOfSuffixFlags(t *testing.T) {
	p := ProviderConfig{ManualModels: []string{"#reason_suffix:gpt-5.5"}}

	got := p.VisibleModelIDs()
	want := []string{"gpt-5.5", "gpt-5.5-minimal", "gpt-5.5-xhigh", "gpt-5.5-medium", "gpt-5.5-high", "gpt-5.5-low", "gpt-5.5-none", "gpt-5.5-max"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("expected manual reason suffix family expansion, want %v got %v", want, got)
	}
}

func TestVisibleModelIDsDoesNotExposeModelMapKeysOrTargets(t *testing.T) {
	p := ProviderConfig{
		ModelMap:     []ModelMapEntry{{Key: "client-alias", Target: "upstream-real"}},
		ManualModels: []string{"manual-model"},
	}

	ids := p.VisibleModelIDs()
	if containsString(ids, "client-alias") {
		t.Fatalf("expected MODEL_MAP key to stay out of visible models, got %v", ids)
	}
	if containsString(ids, "upstream-real") {
		t.Fatalf("expected MODEL_MAP target to stay out of visible models, got %v", ids)
	}
	if !containsString(ids, "manual-model") {
		t.Fatalf("expected manual model to stay visible, got %v", ids)
	}
}

func TestManualModelOverridesHiddenModel(t *testing.T) {
	p := ProviderConfig{ManualModels: []string{"gpt-5.5-noprompt"}, HiddenModels: []string{"gpt-5.5-noprompt", "#re:gpt-5\\.5.*"}}

	if p.HidesModel("gpt-5.5-noprompt") {
		t.Fatalf("expected explicit manual model to override hidden model patterns")
	}
	ids := p.VisibleModelIDs()
	if strings.Join(ids, ",") != "gpt-5.5-noprompt" {
		t.Fatalf("expected explicit manual model to stay visible despite hidden patterns, got %v", ids)
	}
}

func TestManualReasonSuffixFamilyAllowsHiddenModelVariant(t *testing.T) {
	p := ProviderConfig{ManualModels: []string{"#reason_suffix:gpt-5.5"}, HiddenModels: []string{"gpt-5.5-minimal"}}

	for _, model := range []string{"gpt-5.5", "gpt-5.5-none", "gpt-5.5-low", "gpt-5.5-medium", "gpt-5.5-high", "gpt-5.5-xhigh"} {
		if p.HidesModel(model) {
			t.Fatalf("expected manual reason suffix family model %q to stay visible", model)
		}
	}
	if !p.HidesModel("gpt-5.5-minimal") {
		t.Fatalf("expected hidden model to remove one generated reason suffix family variant")
	}
	ids := p.VisibleModelIDs()
	for _, want := range []string{"gpt-5.5", "gpt-5.5-none", "gpt-5.5-low", "gpt-5.5-medium", "gpt-5.5-high", "gpt-5.5-xhigh"} {
		if !containsString(ids, want) {
			t.Fatalf("expected manual reason suffix family model %q to stay visible, got %v", want, ids)
		}
	}
	if containsString(ids, "gpt-5.5-minimal") {
		t.Fatalf("expected hidden generated reason suffix family variant to be removed, got %v", ids)
	}
}

func TestManualReasonSuffixFamilyAllowsHiddenEffortSelector(t *testing.T) {
	p := ProviderConfig{ManualModels: []string{"#reason_suffix:gpt-5.5"}, HiddenModels: []string{"#reason_suffix:-minimal"}}

	if !p.HidesModel("gpt-5.5-minimal") {
		t.Fatalf("expected hidden effort selector to remove one generated family effort")
	}
	for _, model := range []string{"gpt-5.5", "gpt-5.5-none", "gpt-5.5-low", "gpt-5.5-medium", "gpt-5.5-high", "gpt-5.5-xhigh"} {
		if p.HidesModel(model) {
			t.Fatalf("expected hidden effort selector not to remove %q", model)
		}
	}
}

func TestManualNarrowReasonSuffixVariantOverridesHiddenFamily(t *testing.T) {
	p := ProviderConfig{ManualModels: []string{"gpt-5.5-minimal"}, HiddenModels: []string{"#reason_suffix:gpt-5.5"}}

	if p.HidesModel("gpt-5.5-minimal") {
		t.Fatalf("expected narrow static manual variant to override broad hidden family")
	}
	if !p.HidesModel("gpt-5.5-low") || !p.HidesModel("gpt-5.5") {
		t.Fatalf("expected broad hidden family to still hide non-manual variants")
	}
	ids := p.VisibleModelIDs()
	if strings.Join(ids, ",") != "gpt-5.5-minimal" {
		t.Fatalf("expected only narrow manual variant to remain visible, got %v", ids)
	}
}

func TestHiddenReasonSuffixFamilyHidesGeneratedVariants(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{NewModelMapEntry("gpt-5.5", "upstream-gpt-5.5")}, EnableReasoningEffortSuffix: true, ExposeReasoningSuffixModels: true, HiddenModels: []string{"#reason_suffix:gpt-5.5"}}

	for _, model := range []string{"gpt-5.5", "gpt-5.5-none", "gpt-5.5-minimal", "gpt-5.5-low", "gpt-5.5-medium", "gpt-5.5-high", "gpt-5.5-xhigh"} {
		if !p.HidesModel(model) {
			t.Fatalf("expected hidden reason suffix family marker to hide %q", model)
		}
	}
	if ids := p.VisibleModelIDs(); len(ids) != 0 {
		t.Fatalf("expected hidden reason suffix family marker to remove visible models, got %v", ids)
	}
}

func TestManualReasonSuffixRegexMarkerResolvesSuffix(t *testing.T) {
	p := ProviderConfig{ManualModels: []string{"#reason_suffix:#re:gpt-5\\..*"}}

	model, effort := p.ResolveModelAndEffort("gpt-5.5-low", false)
	if model != "gpt-5.5" || effort != "low" {
		t.Fatalf("expected regex reason suffix marker to resolve gpt-5.5-low to gpt-5.5/low, got %q/%q", model, effort)
	}
	if !p.HasManualReasonSuffixForModel("gpt-5.5-minimal") {
		t.Fatalf("expected regex reason suffix marker to match generated minimal suffix")
	}
	if p.HasManualReasonSuffixForModel("gpt-4.1-low") {
		t.Fatalf("expected regex reason suffix marker not to match gpt-4.1-low")
	}
}

func TestReasonSuffixRegexMarkerRequiresSuffixBeforeRegex(t *testing.T) {
	p := ProviderConfig{ManualModels: []string{"#re:#reason_suffix:gpt-5\\..*"}, HiddenModels: []string{"#re:#reason_suffix:gpt-5\\..*"}}

	if p.HasManualReasonSuffixForModel("gpt-5.5-low") {
		t.Fatalf("expected #re:#reason_suffix order not to enable reason suffix parsing")
	}
	if p.HidesModel("gpt-5.5-low") {
		t.Fatalf("expected #re:#reason_suffix order not to hide reason suffix family")
	}
}

func TestHiddenReasonSuffixRegexMarkerHidesSuffixFamily(t *testing.T) {
	p := ProviderConfig{HiddenModels: []string{"#reason_suffix:#re:gpt-5\\..*"}}

	if !p.HidesModel("gpt-5.5") || !p.HidesModel("gpt-5.5-low") || !p.HidesModel("gpt-5.5-minimal") {
		t.Fatalf("expected regex reason suffix marker to hide base and suffix family")
	}
	if p.HidesModel("gpt-4.1-low") {
		t.Fatalf("expected regex reason suffix marker not to hide unmatched family")
	}
}

func TestManualReasonSuffixSelectorAddsOnlyRequestedEffort(t *testing.T) {
	p := ProviderConfig{ManualModels: []string{"#reason_suffix:-minimal"}}

	ids := p.ManualReasonSuffixModelIDsFrom([]string{"gpt-5.5", "gpt-4.1"})
	want := []string{"gpt-5.5-minimal", "gpt-4.1-minimal"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("expected suffix selector to add only minimal variants, want %v got %v", want, ids)
	}
}

func TestHiddenReasonSuffixSelectorHidesOnlyRequestedEffort(t *testing.T) {
	p := ProviderConfig{HiddenModels: []string{"#reason_suffix:-minimal"}}

	if !p.HidesModel("gpt-5.5-minimal") {
		t.Fatalf("expected suffix selector to hide minimal variant")
	}
	if p.HidesModel("gpt-5.5-low") || p.HidesModel("gpt-5.5") {
		t.Fatalf("expected suffix selector not to hide other efforts or base model")
	}
}

func TestHiddenReasonSuffixSelectorOverridesGlobalSuffixExposure(t *testing.T) {
	p := ProviderConfig{
		ManualModels:                []string{"gpt-5.5"},
		EnableReasoningEffortSuffix: true,
		ExposeReasoningSuffixModels: true,
		HiddenModels:                []string{"#reason_suffix:-minimal", "gpt-5.5-high"},
	}

	ids := p.VisibleModelIDs()
	for _, hidden := range []string{"gpt-5.5-minimal", "gpt-5.5-high"} {
		if containsString(ids, hidden) {
			t.Fatalf("expected hidden suffix %q to override global suffix exposure, got %v", hidden, ids)
		}
	}
	for _, visible := range []string{"gpt-5.5", "gpt-5.5-none", "gpt-5.5-low", "gpt-5.5-medium", "gpt-5.5-xhigh"} {
		if !containsString(ids, visible) {
			t.Fatalf("expected non-hidden suffix %q to remain visible, got %v", visible, ids)
		}
	}
}

func TestHiddenReasonSuffixFamilyOverridesGlobalSuffixExposure(t *testing.T) {
	p := ProviderConfig{
		ManualModels:                []string{"gpt-5.5"},
		EnableReasoningEffortSuffix: true,
		ExposeReasoningSuffixModels: true,
		HiddenModels:                []string{"#reason_suffix:gpt-5.5"},
	}

	if ids := p.VisibleModelIDs(); len(ids) != 1 || ids[0] != "gpt-5.5" {
		t.Fatalf("expected hidden suffix family to hide generated variants while keeping manual base model, got %v", ids)
	}
}

func TestManualReasonSuffixSelectorAllowsSpecificHiddenVariant(t *testing.T) {
	p := ProviderConfig{ManualModels: []string{"#reason_suffix:-minimal"}, HiddenModels: []string{"gpt-5.5-minimal"}}

	if !p.HidesModel("gpt-5.5-minimal") {
		t.Fatalf("expected specific hidden model to override broad manual effort selector")
	}
	if p.HidesModel("gpt-4.1-minimal") {
		t.Fatalf("expected broad manual effort selector to keep other minimal variants visible")
	}
}

func TestManualReasonSuffixFamilyOverridesSameHiddenFamilyMarker(t *testing.T) {
	p := ProviderConfig{ManualModels: []string{"#reason_suffix:gpt-5.5"}, HiddenModels: []string{"#reason_suffix:gpt-5.5"}}

	for _, model := range []string{"gpt-5.5", "gpt-5.5-none", "gpt-5.5-minimal", "gpt-5.5-low", "gpt-5.5-medium", "gpt-5.5-high", "gpt-5.5-xhigh"} {
		if p.HidesModel(model) {
			t.Fatalf("expected manual reason suffix family to override identical hidden family marker for %q", model)
		}
	}
	ids := p.VisibleModelIDs()
	for _, want := range []string{"gpt-5.5", "gpt-5.5-none", "gpt-5.5-minimal", "gpt-5.5-low", "gpt-5.5-medium", "gpt-5.5-high", "gpt-5.5-xhigh"} {
		if !containsString(ids, want) {
			t.Fatalf("expected manual reason suffix family model %q to remain visible, got %v", want, ids)
		}
	}
}

func TestResolveModelTreatsUnmarkedPatternAsLiteral(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{NewModelMapEntry("gpt-5.5", "real")}}

	if got := p.ResolveModel("gpt-5.5", false); got != "real" {
		t.Fatalf("expected literal model to resolve, got %q", got)
	}
	if got := p.ResolveModel("gpt-5x5", false); got != "gpt-5x5" {
		t.Fatalf("expected unmarked dot to stay literal, got %q", got)
	}
}

func TestResolveModelSupportsRegexCaptures(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{NewModelMapEntry("#re:mini(.*)o", "real-$0-$1")}}

	model, effort := p.ResolveModelAndEffort("mini2o", false)
	if model != "real-mini2o-2" {
		t.Fatalf("expected regex captures to resolve to %q, got %q", "real-mini2o-2", model)
	}
	if effort != "" {
		t.Fatalf("expected no effort override for regex mapping, got %q", effort)
	}
}

func TestResolveModelOnlyExpandsSingleDigitRegexCaptures(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{NewModelMapEntry("#re:mini(.*)o", "real-$0-$1-$9-$12")}}

	model := p.ResolveModel("mini2o", false)
	if model != "real-mini2o-2--$12" {
		t.Fatalf("expected only $0-$9 captures to expand without ambiguous $12 handling, got %q", model)
	}
}

func TestResolveModelKeepsEscapedCapturePlaceholdersLiteral(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{NewModelMapEntry("#re:mini(.*)o", `real-\$1-$1-\$12-$12`)}}

	model := p.ResolveModel("mini2o", false)
	if model != "real-$1-2-$12-$12" {
		t.Fatalf("expected escaped capture placeholders to stay literal, got %q", model)
	}
}

func TestResolveModelKeepsSourcePatternAsStandardRegexp(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{NewModelMapEntry(`#re:gpt-4\.1`, "exact")}}

	if got := p.ResolveModel("gpt-4.1", false); got != "exact" {
		t.Fatalf("expected escaped regexp dot to match literal dot, got %q", got)
	}
	if got := p.ResolveModel("gpt-4x1", false); got != "gpt-4x1" {
		t.Fatalf("expected escaped regexp dot not to match arbitrary char, got %q", got)
	}
}

func TestResolveModelTreatsRegexpEscapedStarAsLiteralStar(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{NewModelMapEntry(`#re:gpt-4\*mini`, "literal-star")}}

	if got := p.ResolveModel("gpt-4*mini", false); got != "literal-star" {
		t.Fatalf("expected escaped regexp star to match literal star, got %q", got)
	}
	if got := p.ResolveModel("gpt-4ooooomini", false); got != "gpt-4ooooomini" {
		t.Fatalf("expected escaped regexp star not to behave like wildcard, got %q", got)
	}
}

func TestResolveModelTreatsEscapedBackslashThenEscapedStarAsLiteralBackslashStar(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{NewModelMapEntry(`#re:gpt-4\\\*mini`, "literal-backslash-star")}}

	if got := p.ResolveModel(`gpt-4\*mini`, false); got != "literal-backslash-star" {
		t.Fatalf("expected escaped backslash plus escaped star to match literal backslash-star, got %q", got)
	}
	if got := p.ResolveModel("gpt-4*mini", false); got != "gpt-4*mini" {
		t.Fatalf("expected backslash-star regexp not to match bare literal star, got %q", got)
	}
}

func TestLoadProviderFileParsesRegexModelMapPrefixWithColonSeparator(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := strings.Join([]string{
		"PROVIDER_ID=openai",
		"MODEL_MAP=#re:mini(.*)o:real-$1,gpt-5.5:gpt-5.6",
		"",
	}, "\n")
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if got := provider.ResolveModel("mini2o", false); got != "real-2" {
		t.Fatalf("expected regex MODEL_MAP entry to resolve, got %q", got)
	}
	if got := provider.ResolveModel("gpt-5.5", false); got != "gpt-5.6" {
		t.Fatalf("expected literal MODEL_MAP entry to resolve, got %q", got)
	}
	if got := provider.ResolveModel("gpt-5x5", false); got != "gpt-5x5" {
		t.Fatalf("expected literal MODEL_MAP dot not to match arbitrary char, got %q", got)
	}
}

func TestResolveModelTreatsReColonAsLiteralWithoutHashPrefix(t *testing.T) {
	p := ProviderConfig{ModelMap: []ModelMapEntry{NewModelMapEntry("re:mini(.*)o", "literal")}}

	if got := p.ResolveModel("re:mini(.*)o", false); got != "literal" {
		t.Fatalf("expected unmarked re: prefix to stay literal, got %q", got)
	}
	if got := p.ResolveModel("mini2o", false); got != "mini2o" {
		t.Fatalf("expected unmarked re: prefix not to enable regex, got %q", got)
	}
}

func TestLoadProviderFileParsesHiddenModelsList(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := strings.Join([]string{
		"PROVIDER_ID=openai",
		"HIDDEN_MODELS=#re:gpt-4.*,manual-alpha,claude-sonnet",
		"",
	}, "\n")
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if want := []string{"#re:gpt-4.*", "manual-alpha", "claude-sonnet"}; len(provider.HiddenModels) != len(want) {
		t.Fatalf("expected hidden models %v, got %#v", want, provider.HiddenModels)
	}
	for i, want := range []string{"#re:gpt-4.*", "manual-alpha", "claude-sonnet"} {
		if provider.HiddenModels[i] != want {
			t.Fatalf("expected hidden model %q at index %d, got %#v", want, i, provider.HiddenModels)
		}
	}
	if !provider.HidesModel("gpt-4o") || !provider.HidesModel("manual-alpha") || provider.HidesModel("gpt-5") {
		t.Fatalf("expected hidden model matcher to honor regex and exact patterns")
	}
}

func TestManualModelsOverrideHiddenModels(t *testing.T) {
	provider := ProviderConfig{
		ManualModels: []string{"manual-alpha"},
		HiddenModels: []string{"#re:.*"},
	}
	if provider.HidesModel("manual-alpha") {
		t.Fatalf("expected manual model to stay visible even when hidden regex matches")
	}
	visible := provider.VisibleModelIDs()
	if len(visible) != 1 || visible[0] != "manual-alpha" {
		t.Fatalf("expected manual model to remain visible, got %#v", visible)
	}
}

func TestLoadProviderFileResolvesSystemPromptFilesRelativeToProviderEnv(t *testing.T) {
	rootDir := t.TempDir()
	promptDir := filepath.Join(rootDir, "prompts")
	if err := os.MkdirAll(promptDir, 0o755); err != nil {
		t.Fatalf("mkdir prompt dir: %v", err)
	}
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\n" +
		"SYSTEM_PROMPT_FILES=prompt.md, prompts/extra.md\n" +
		"SYSTEM_PROMPT_POSITION=append\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}

	expectedPaths := []string{
		filepath.Join(rootDir, "prompt.md"),
		filepath.Join(rootDir, "prompts", "extra.md"),
	}
	if len(provider.SystemPromptFiles) != len(expectedPaths) {
		t.Fatalf("expected %d resolved prompt paths, got %#v", len(expectedPaths), provider.SystemPromptFiles)
	}
	for i, expected := range expectedPaths {
		if provider.SystemPromptFiles[i] != expected {
			t.Fatalf("expected prompt path %q at index %d, got %q", expected, i, provider.SystemPromptFiles[i])
		}
	}
	if provider.SystemPromptPosition != SystemPromptPositionAppend {
		t.Fatalf("expected append position, got %q", provider.SystemPromptPosition)
	}
}

func TestLoadProviderFileTreatsBlankOrInvalidPromptPositionAsPrepend(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nSYSTEM_PROMPT_POSITION=sideways\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}

	if provider.SystemPromptPosition != SystemPromptPositionPrepend {
		t.Fatalf("expected invalid prompt position to fall back to prepend, got %q", provider.SystemPromptPosition)
	}

	providerBody = "PROVIDER_ID=openai\nSYSTEM_PROMPT_POSITION=\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("rewrite provider env: %v", err)
	}

	provider, err = loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error after blank position: %v", err)
	}
	if provider.SystemPromptPosition != SystemPromptPositionPrepend {
		t.Fatalf("expected blank prompt position to fall back to prepend, got %q", provider.SystemPromptPosition)
	}
}

func TestLoadProviderFileAllowsBlankSystemPromptFiles(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nSYSTEM_PROMPT_FILES=\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}

	if len(provider.SystemPromptFiles) != 0 {
		t.Fatalf("expected blank prompt files to resolve to empty slice, got %#v", provider.SystemPromptFiles)
	}
	if provider.SystemPromptText != "" {
		t.Fatalf("expected blank prompt text, got %q", provider.SystemPromptText)
	}
}

func TestLoadProviderFileRejectsInvalidSupportsBooleanValues(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := strings.Join([]string{
		"PROVIDER_ID=openai",
		"SUPPORTS_CHAT=yes",
		"SUPPORTS_RESPONSES=enabled",
		"SUPPORTS_MODELS=maybe",
		"SUPPORTS_ANTHROPIC_MESSAGES=on",
		"",
	}, "\n")
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	_, err := loadProviderFile(providerEnvPath)
	if err == nil {
		t.Fatalf("expected invalid SUPPORTS_* boolean to fail validation")
	}
	if _, ok := err.(invalidConfigError); !ok {
		t.Fatalf("expected invalidConfigError for invalid SUPPORTS_* boolean, got %T", err)
	}
	if err.Error() != "invalid SUPPORTS_CHAT in "+providerEnvPath+": \"yes\"" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLoadProviderFileRejectsInvalidProviderEnabledBooleanValue(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := strings.Join([]string{
		"PROVIDER_ID=openai",
		"PROVIDER_ENABLED=enabled",
		"",
	}, "\n")
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	_, err := loadProviderFile(providerEnvPath)
	if err == nil {
		t.Fatalf("expected invalid PROVIDER_ENABLED to fail validation")
	}
	if _, ok := err.(invalidConfigError); !ok {
		t.Fatalf("expected invalidConfigError for invalid PROVIDER_ENABLED, got %T", err)
	}
	if err.Error() != "invalid PROVIDER_ENABLED in "+providerEnvPath+": \"enabled\"" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLoadProviderFileRejectsInvalidReasoningSuffixBooleanValues(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := strings.Join([]string{
		"PROVIDER_ID=openai",
		"ENABLE_REASONING_EFFORT_SUFFIX=maybe",
		"",
	}, "\n")
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	_, err := loadProviderFile(providerEnvPath)
	if err == nil {
		t.Fatalf("expected invalid ENABLE_REASONING_EFFORT_SUFFIX to fail validation")
	}
	if err.Error() != "invalid ENABLE_REASONING_EFFORT_SUFFIX in "+providerEnvPath+": \"maybe\"" {
		t.Fatalf("unexpected error message: %v", err)
	}

	providerBody = strings.Join([]string{
		"PROVIDER_ID=openai",
		"EXPOSE_REASONING_SUFFIX_MODELS=on",
		"",
	}, "\n")
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("rewrite provider env: %v", err)
	}

	_, err = loadProviderFile(providerEnvPath)
	if err == nil {
		t.Fatalf("expected invalid EXPOSE_REASONING_SUFFIX_MODELS to fail validation")
	}
	if err.Error() != "invalid EXPOSE_REASONING_SUFFIX_MODELS in "+providerEnvPath+": \"on\"" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLoadProviderFileParsesThinkingTagStyleAsStrictBoolean(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := strings.Join([]string{
		"PROVIDER_ID=openai",
		"UPSTREAM_THINKING_TAG_STYLE=true",
		"",
	}, "\n")
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.UpstreamThinkingTagStyle != UpstreamThinkingTagStyleLegacy {
		t.Fatalf("expected true to enable think tag parsing, got %q", provider.UpstreamThinkingTagStyle)
	}

	providerBody = strings.Join([]string{
		"PROVIDER_ID=openai",
		"UPSTREAM_THINKING_TAG_STYLE=false",
		"",
	}, "\n")
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("rewrite provider env: %v", err)
	}
	provider, err = loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.UpstreamThinkingTagStyle != UpstreamThinkingTagStyleOff {
		t.Fatalf("expected false to disable think tag parsing, got %q", provider.UpstreamThinkingTagStyle)
	}

	providerBody = strings.Join([]string{
		"PROVIDER_ID=openai",
		"UPSTREAM_THINKING_TAG_STYLE=legacy",
		"",
	}, "\n")
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("rewrite provider env: %v", err)
	}
	if _, err := loadProviderFile(providerEnvPath); err == nil {
		t.Fatalf("expected non-boolean UPSTREAM_THINKING_TAG_STYLE to fail validation")
	}
}

func TestLoadProviderFileTreatsBlankClaudeInjectionFlagsAsInheritance(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := strings.Join([]string{
		"PROVIDER_ID=openai",
		"INJECT_CLAUDE_CODE_METADATA_USER_ID=",
		"INJECT_CLAUDE_CODE_SYSTEM_PROMPT=",
		"",
	}, "\n")
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.InjectClaudeCodeMetadataUserIDSet {
		t.Fatalf("expected blank metadata injection flag to inherit root config")
	}
	if provider.InjectClaudeCodeSystemPromptSet {
		t.Fatalf("expected blank system prompt injection flag to inherit root config")
	}
}

func TestLoadProviderFileParsesExplicitClaudeInjectionFlags(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := strings.Join([]string{
		"PROVIDER_ID=openai",
		"INJECT_CLAUDE_CODE_METADATA_USER_ID=true",
		"INJECT_CLAUDE_CODE_SYSTEM_PROMPT=false",
		"",
	}, "\n")
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if !provider.InjectClaudeCodeMetadataUserIDSet || !provider.InjectClaudeCodeMetadataUserID {
		t.Fatalf("expected explicit true metadata injection override, got %#v", provider)
	}
	if !provider.InjectClaudeCodeSystemPromptSet || provider.InjectClaudeCodeSystemPrompt {
		t.Fatalf("expected explicit false system prompt injection override, got %#v", provider)
	}
}

func TestLoadProviderFileRejectsInvalidClaudeMetadataInjectionFlag(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := strings.Join([]string{
		"PROVIDER_ID=openai",
		"INJECT_CLAUDE_CODE_METADATA_USER_ID=enabled",
		"",
	}, "\n")
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	_, err := loadProviderFile(providerEnvPath)
	if err == nil {
		t.Fatalf("expected invalid INJECT_CLAUDE_CODE_METADATA_USER_ID to fail validation")
	}
	if err.Error() != "invalid INJECT_CLAUDE_CODE_METADATA_USER_ID in "+providerEnvPath+": \"enabled\"" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLoadProviderFileRejectsInvalidClaudeSystemPromptInjectionFlag(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := strings.Join([]string{
		"PROVIDER_ID=openai",
		"INJECT_CLAUDE_CODE_SYSTEM_PROMPT=enabled",
		"",
	}, "\n")
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	_, err := loadProviderFile(providerEnvPath)
	if err == nil {
		t.Fatalf("expected invalid INJECT_CLAUDE_CODE_SYSTEM_PROMPT to fail validation")
	}
	if err.Error() != "invalid INJECT_CLAUDE_CODE_SYSTEM_PROMPT in "+providerEnvPath+": \"enabled\"" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLoadProviderFileRejectsInvalidThinkingMappingBooleanValue(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := strings.Join([]string{
		"PROVIDER_ID=openai",
		"MAP_REASONING_SUFFIX_TO_ANTHROPIC_THINKING=sometimes",
		"",
	}, "\n")
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	_, err := loadProviderFile(providerEnvPath)
	if err == nil {
		t.Fatalf("expected invalid MAP_REASONING_SUFFIX_TO_ANTHROPIC_THINKING to fail validation")
	}
	if err.Error() != "invalid MAP_REASONING_SUFFIX_TO_ANTHROPIC_THINKING in "+providerEnvPath+": \"sometimes\"" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLoadProviderFileSupportsFlagsKeepWeakSemantics(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := strings.Join([]string{
		"PROVIDER_ID=openai",
		"SUPPORTS_CHAT=false",
		"SUPPORTS_RESPONSES=true",
		"SUPPORTS_MODELS=1",
		"SUPPORTS_ANTHROPIC_MESSAGES=false",
		"",
	}, "\n")
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}

	if provider.SupportsChat {
		t.Fatalf("expected SUPPORTS_CHAT=false to disable only chat exposure")
	}
	if !provider.SupportsResponses {
		t.Fatalf("expected SUPPORTS_RESPONSES=true to keep responses exposure enabled")
	}
	if !provider.SupportsModels {
		t.Fatalf("expected SUPPORTS_MODELS=1 to keep models exposure enabled")
	}
	if provider.SupportsAnthropicMessages {
		t.Fatalf("expected SUPPORTS_ANTHROPIC_MESSAGES=false to disable only messages exposure")
	}
	if provider.SupportsResponses == provider.SupportsChat {
		t.Fatalf("expected responses exposure to remain independent from chat exposure")
	}
	if provider.SupportsModels == provider.SupportsAnthropicMessages {
		t.Fatalf("expected models exposure to remain independent from anthropic messages exposure")
	}
}

func TestLoadProviderFileUsesRetryDefaultsWhenUnset(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.UpstreamRetryCount != DefaultUpstreamRetryCount {
		t.Fatalf("expected default retry count %d, got %d", DefaultUpstreamRetryCount, provider.UpstreamRetryCount)
	}
	if provider.UpstreamRetryDelay != DefaultUpstreamRetryDelay {
		t.Fatalf("expected default retry delay %v, got %v", DefaultUpstreamRetryDelay, provider.UpstreamRetryDelay)
	}
	if provider.UpstreamFirstByteTimeout != 0 {
		t.Fatalf("expected provider first byte timeout to inherit root config by default, got %v", provider.UpstreamFirstByteTimeout)
	}
}

func TestLoadProviderFileParsesFirstByteTimeoutOverride(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nUPSTREAM_FIRST_BYTE_TIMEOUT=20m\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.UpstreamFirstByteTimeout != 20*time.Minute {
		t.Fatalf("expected provider first byte timeout 20m, got %v", provider.UpstreamFirstByteTimeout)
	}

	providerBody = "PROVIDER_ID=openai\nUPSTREAM_FIRST_BYTE_TIMEOUT=bad\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("rewrite provider env: %v", err)
	}
	if _, err := loadProviderFile(providerEnvPath); err == nil {
		t.Fatalf("expected invalid provider first byte timeout to fail validation")
	}
}

func TestLoadProviderFileParsesRetryOverrides(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nUPSTREAM_RETRY_COUNT=2\nUPSTREAM_RETRY_DELAY=750ms\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.UpstreamRetryCount != 2 {
		t.Fatalf("expected retry count 2, got %d", provider.UpstreamRetryCount)
	}
	if provider.UpstreamRetryDelay != 750*time.Millisecond {
		t.Fatalf("expected retry delay 750ms, got %v", provider.UpstreamRetryDelay)
	}
}

func TestLoadProviderFileParsesUpstreamMaxOutputTokens(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nUPSTREAM_MAX_OUTPUT_TOKENS=64000\nFORCE_UPSTREAM_MAX_OUTPUT_TOKENS=true\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.UpstreamMaxOutputTokens != 64000 {
		t.Fatalf("expected upstream max output tokens 64000, got %d", provider.UpstreamMaxOutputTokens)
	}
	if !provider.UpstreamMaxOutputTokensSet {
		t.Fatalf("expected upstream max output tokens to be marked as explicitly set")
	}
	if !provider.ForceUpstreamMaxOutputTokens {
		t.Fatalf("expected force upstream max output tokens to be true")
	}
	if !provider.ForceUpstreamMaxOutputTokensSet {
		t.Fatalf("expected force upstream max output tokens to be marked as explicitly set")
	}
}

func TestLoadProviderFileParsesAnthropicMaxThinkingBudget(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nANTHROPIC_MAX_THINKING_BUDGET=40000\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.AnthropicMaxThinkingBudget != 40000 {
		t.Fatalf("expected anthropic max thinking budget 40000, got %d", provider.AnthropicMaxThinkingBudget)
	}
	if !provider.AnthropicMaxThinkingBudgetSet {
		t.Fatalf("expected anthropic max thinking budget to be marked as explicitly set")
	}
}

func TestLoadProviderFileRejectsInvalidAnthropicMaxThinkingBudget(t *testing.T) {
	for _, value := range []string{"0", "1023", "bad"} {
		t.Run(value, func(t *testing.T) {
			rootDir := t.TempDir()
			providerEnvPath := filepath.Join(rootDir, "openai.env")
			providerBody := "PROVIDER_ID=openai\nANTHROPIC_MAX_THINKING_BUDGET=" + value + "\n"
			if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
				t.Fatalf("write provider env: %v", err)
			}

			_, err := loadProviderFile(providerEnvPath)
			if err == nil {
				t.Fatalf("expected invalid anthropic max thinking budget to fail validation")
			}
			if err.Error() != "invalid ANTHROPIC_MAX_THINKING_BUDGET in "+providerEnvPath+": \""+value+"\"" {
				t.Fatalf("unexpected error message: %v", err)
			}
		})
	}
}

func TestLoadProviderFileTreatsBlankUpstreamMaxOutputTokensAsUnset(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nUPSTREAM_MAX_OUTPUT_TOKENS=\nFORCE_UPSTREAM_MAX_OUTPUT_TOKENS=\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.UpstreamMaxOutputTokens != 0 {
		t.Fatalf("expected blank upstream max output tokens to stay unset, got %d", provider.UpstreamMaxOutputTokens)
	}
	if provider.UpstreamMaxOutputTokensSet {
		t.Fatalf("expected blank upstream max output tokens not to be marked as explicitly set")
	}
	if provider.ForceUpstreamMaxOutputTokens {
		t.Fatalf("expected blank force upstream max output tokens to be false")
	}
	if provider.ForceUpstreamMaxOutputTokensSet {
		t.Fatalf("expected blank force upstream max output tokens not to be marked as explicitly set")
	}
}

func TestLoadProviderFileParsesMinusOneUpstreamMaxOutputTokensAsOmit(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nUPSTREAM_MAX_OUTPUT_TOKENS=-1\nFORCE_UPSTREAM_MAX_OUTPUT_TOKENS=true\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.UpstreamMaxOutputTokens != -1 {
		t.Fatalf("expected upstream max output tokens -1, got %d", provider.UpstreamMaxOutputTokens)
	}
	if !provider.UpstreamMaxOutputTokensSet {
		t.Fatalf("expected upstream max output tokens -1 to be marked as explicitly set")
	}
	if !provider.ForceUpstreamMaxOutputTokens {
		t.Fatalf("expected force upstream max output tokens to be true")
	}
	if !provider.ForceUpstreamMaxOutputTokensSet {
		t.Fatalf("expected force upstream max output tokens to be marked as explicitly set")
	}
}

func TestLoadProviderFileRejectsInvalidUpstreamMaxOutputTokens(t *testing.T) {
	for _, value := range []string{"0", "-2", "bad"} {
		t.Run(value, func(t *testing.T) {
			rootDir := t.TempDir()
			providerEnvPath := filepath.Join(rootDir, "openai.env")
			providerBody := "PROVIDER_ID=openai\nUPSTREAM_MAX_OUTPUT_TOKENS=" + value + "\n"
			if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
				t.Fatalf("write provider env: %v", err)
			}

			_, err := loadProviderFile(providerEnvPath)
			if err == nil {
				t.Fatalf("expected invalid upstream max output tokens to fail validation")
			}
			if _, ok := err.(invalidConfigError); !ok {
				t.Fatalf("expected invalidConfigError for invalid upstream max output tokens, got %T", err)
			}
			if err.Error() != "invalid UPSTREAM_MAX_OUTPUT_TOKENS in "+providerEnvPath+": \""+value+"\"" {
				t.Fatalf("unexpected error message: %v", err)
			}
		})
	}
}

func TestLoadProviderFileRejectsInvalidForceUpstreamMaxOutputTokens(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nFORCE_UPSTREAM_MAX_OUTPUT_TOKENS=maybe\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	_, err := loadProviderFile(providerEnvPath)
	if err == nil {
		t.Fatalf("expected invalid force upstream max output tokens to fail validation")
	}
	if _, ok := err.(invalidConfigError); !ok {
		t.Fatalf("expected invalidConfigError for invalid force upstream max output tokens, got %T", err)
	}
	if err.Error() != "invalid FORCE_UPSTREAM_MAX_OUTPUT_TOKENS in "+providerEnvPath+": \"maybe\"" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLoadProviderFileParsesScopedUpstreamMaxOutputTokens(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nUPSTREAM_MAX_OUTPUT_TOKENS=64000,gpt-5.5:128000,#re:.*gpt-.*:100000\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.UpstreamMaxOutputTokens != 64000 {
		t.Fatalf("expected default upstream max output tokens 64000, got %d", provider.UpstreamMaxOutputTokens)
	}
	if len(provider.UpstreamMaxOutputTokenRules) != 2 {
		t.Fatalf("expected 2 scoped max output rules, got %#v", provider.UpstreamMaxOutputTokenRules)
	}
	if provider.UpstreamMaxOutputTokenRules[0].Pattern != "gpt-5.5" || provider.UpstreamMaxOutputTokenRules[0].Tokens != 128000 {
		t.Fatalf("expected exact model rule first, got %#v", provider.UpstreamMaxOutputTokenRules)
	}
	if provider.UpstreamMaxOutputTokenRules[1].Pattern != "#re:.*gpt-.*" || provider.UpstreamMaxOutputTokenRules[1].Tokens != 100000 {
		t.Fatalf("expected regex rule second, got %#v", provider.UpstreamMaxOutputTokenRules)
	}
}

func TestResolveUpstreamMaxOutputTokensPrefersExactThenRegexThenDefault(t *testing.T) {
	provider := ProviderConfig{
		UpstreamMaxOutputTokens: 64000,
		UpstreamMaxOutputTokenRules: []ScopedIntRule{
			{Pattern: "gpt-5.5", Tokens: 128000, IsExact: true},
			{Pattern: "#re:.*gpt-.*", Tokens: 100000, PatternRE: regexp.MustCompile("^(?:.*gpt-.*)$")},
		},
	}
	if got := provider.ResolveUpstreamMaxOutputTokens("gpt-5.5"); got != 128000 {
		t.Fatalf("expected exact match 128000, got %d", got)
	}
	if got := provider.ResolveUpstreamMaxOutputTokens("gpt-5.4-mini"); got != 100000 {
		t.Fatalf("expected regex match 100000, got %d", got)
	}
	if got := provider.ResolveUpstreamMaxOutputTokens("gpt-5.5-high-noprompt"); got != 100000 {
		t.Fatalf("expected literal exact rule not to strip suffixes, got %d", got)
	}
	if got := provider.ResolveUpstreamMaxOutputTokens("claude-sonnet-4-5"); got != 64000 {
		t.Fatalf("expected default 64000, got %d", got)
	}
}

func TestLoadProviderFileParsesModelLimitContextTokens(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nMODEL_LIMIT_CONTEXT_TOKENS=-1,gpt-5.5:256000,#re:.*gpt-.*:64000\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.ModelLimitContextTokens != -1 {
		t.Fatalf("expected default model context limit -1, got %d", provider.ModelLimitContextTokens)
	}
	if !provider.ModelLimitContextTokensSet {
		t.Fatalf("expected model context limit to be marked as explicitly set")
	}
	if len(provider.ModelLimitContextTokenRules) != 2 {
		t.Fatalf("expected 2 scoped context limit rules, got %#v", provider.ModelLimitContextTokenRules)
	}
	if provider.ModelLimitContextTokenRules[0].Pattern != "gpt-5.5" || provider.ModelLimitContextTokenRules[0].Tokens != 256000 {
		t.Fatalf("expected exact context limit rule first, got %#v", provider.ModelLimitContextTokenRules)
	}
	if provider.ModelLimitContextTokenRules[1].Pattern != "#re:.*gpt-.*" || provider.ModelLimitContextTokenRules[1].Tokens != 64000 {
		t.Fatalf("expected regex context limit rule second, got %#v", provider.ModelLimitContextTokenRules)
	}
}

func TestLoadProviderFileTreatsBlankModelLimitContextTokensAsUnset(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nMODEL_LIMIT_CONTEXT_TOKENS=\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.ModelLimitContextTokens != 0 {
		t.Fatalf("expected blank model context limit to stay unset, got %d", provider.ModelLimitContextTokens)
	}
	if provider.ModelLimitContextTokensSet {
		t.Fatalf("expected blank model context limit not to be marked as explicitly set")
	}
}

func TestLoadProviderFileRejectsInvalidModelLimitContextTokens(t *testing.T) {
	for _, value := range []string{"0", "-2", "bad"} {
		t.Run(value, func(t *testing.T) {
			rootDir := t.TempDir()
			providerEnvPath := filepath.Join(rootDir, "openai.env")
			providerBody := "PROVIDER_ID=openai\nMODEL_LIMIT_CONTEXT_TOKENS=" + value + "\n"
			if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
				t.Fatalf("write provider env: %v", err)
			}

			_, err := loadProviderFile(providerEnvPath)
			if err == nil {
				t.Fatalf("expected invalid model context limit to fail validation")
			}
			if _, ok := err.(invalidConfigError); !ok {
				t.Fatalf("expected invalidConfigError for invalid model context limit, got %T", err)
			}
			if err.Error() != "invalid MODEL_LIMIT_CONTEXT_TOKENS in "+providerEnvPath+": \""+value+"\"" {
				t.Fatalf("unexpected error message: %v", err)
			}
		})
	}
}

func TestResolveModelLimitContextTokensPrefersExactThenRegexThenDefault(t *testing.T) {
	provider := ProviderConfig{
		ModelLimitContextTokens: -1,
		ModelLimitContextTokenRules: []ScopedIntRule{
			{Pattern: "gpt-5.5", Tokens: 256000, IsExact: true},
			{Pattern: "#re:.*gpt-.*", Tokens: 64000, PatternRE: regexp.MustCompile("^(?:.*gpt-.*)$")},
		},
	}
	if got := provider.ResolveModelLimitContextTokens("gpt-5.5"); got != 256000 {
		t.Fatalf("expected exact context limit 256000, got %d", got)
	}
	if got := provider.ResolveModelLimitContextTokens("gpt-5.4-mini"); got != 64000 {
		t.Fatalf("expected regex context limit 64000, got %d", got)
	}
	if got := provider.ResolveModelLimitContextTokens("claude-sonnet-4-5"); got != -1 {
		t.Fatalf("expected default context limit -1, got %d", got)
	}
}

func TestLoadProviderFileTreatsScopedOnlyModelLimitContextTokensDefaultAsUnlimited(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nMODEL_LIMIT_CONTEXT_TOKENS=gpt-5.5:256000\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.ModelLimitContextTokens != -1 {
		t.Fatalf("expected scoped-only provider context limit default -1, got %d", provider.ModelLimitContextTokens)
	}
	if got := provider.ResolveModelLimitContextTokens("claude-sonnet-4-5"); got != -1 {
		t.Fatalf("expected unmatched scoped-only provider context limit to stay unlimited, got %d", got)
	}
	if got := provider.ResolveModelLimitContextTokens("gpt-5.5"); got != 256000 {
		t.Fatalf("expected scoped provider context limit 256000, got %d", got)
	}
}

func TestLoadProviderFileAllowsZeroRetryOverride(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nUPSTREAM_RETRY_COUNT=0\nUPSTREAM_RETRY_DELAY=0s\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.UpstreamRetryCount != 0 {
		t.Fatalf("expected retry count 0, got %d", provider.UpstreamRetryCount)
	}
	if provider.UpstreamRetryDelay != 0 {
		t.Fatalf("expected retry delay 0, got %v", provider.UpstreamRetryDelay)
	}
	if provider.UpstreamFirstByteTimeout != 0 {
		t.Fatalf("expected provider first byte timeout to keep inheriting root config, got %v", provider.UpstreamFirstByteTimeout)
	}
}

func TestLoadProviderFileRejectsInvalidRetryCount(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nUPSTREAM_RETRY_COUNT=-3\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	_, err := loadProviderFile(providerEnvPath)
	if err == nil {
		t.Fatalf("expected invalid retry count to fail validation")
	}
	if _, ok := err.(invalidConfigError); !ok {
		t.Fatalf("expected invalidConfigError for invalid retry count, got %T", err)
	}
	if err.Error() != "invalid UPSTREAM_RETRY_COUNT in "+providerEnvPath+": \"-3\"" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLoadProviderFileRejectsInvalidRetryDelay(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nUPSTREAM_RETRY_DELAY=bad\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	if _, err := loadProviderFile(providerEnvPath); err == nil {
		t.Fatalf("expected invalid retry delay to fail validation")
	} else {
		if _, ok := err.(invalidConfigError); !ok {
			t.Fatalf("expected invalidConfigError for invalid retry delay, got %T", err)
		}
		if err.Error() != "invalid UPSTREAM_RETRY_DELAY in "+providerEnvPath+": \"bad\"" {
			t.Fatalf("unexpected error message: %v", err)
		}
	}
}

func TestLoadProviderFileParsesProxyAPIKeyOverride(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nPROXY_API_KEY_OVERRIDE=provider-secret\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if !provider.ProxyAPIKeyOverrideSet {
		t.Fatalf("expected proxy api key override to be marked as set")
	}
	if provider.ProxyAPIKeyOverride != "provider-secret" {
		t.Fatalf("expected proxy api key override provider-secret, got %q", provider.ProxyAPIKeyOverride)
	}
	if provider.EffectiveProxyAPIKey("root-secret") != "provider-secret" {
		t.Fatalf("expected provider override to win over root key")
	}
	if provider.StatusCheckProxyAPIKey("root-secret", false) != "provider-secret" {
		t.Fatalf("expected provider-scoped status key to use provider override")
	}
	if provider.StatusCheckProxyAPIKey("root-secret", true) != "root-secret" {
		t.Fatalf("expected legacy status key to prefer root key")
	}
	providerBody = "PROVIDER_ID=openai\nPROXY_API_KEY_OVERRIDE=empty\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("rewrite provider env: %v", err)
	}
	provider, err = loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error after empty override: %v", err)
	}
	if !provider.ProxyAPIKeyDisabled() {
		t.Fatalf("expected empty override to disable proxy auth")
	}
	if provider.EffectiveProxyAPIKey("root-secret") != "" {
		t.Fatalf("expected disabled override to return empty effective proxy key")
	}
}

func TestLoadProviderFileTreatsBlankProxyAPIKeyOverrideAsRootInheritance(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nPROXY_API_KEY_OVERRIDE=\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if !provider.ProxyAPIKeyOverrideSet {
		t.Fatalf("expected blank proxy api key override to be marked as set")
	}
	if provider.ProxyAPIKeyDisabled() {
		t.Fatalf("expected blank proxy api key override to inherit root key, not disable auth")
	}
	if got := provider.EffectiveProxyAPIKey("root-secret"); got != "root-secret" {
		t.Fatalf("expected blank override to inherit root key, got %q", got)
	}
	if got := provider.StatusCheckProxyAPIKey("root-secret", false); got != "root-secret" {
		t.Fatalf("expected provider-scoped status key to inherit root key, got %q", got)
	}
}

func TestLoadProviderFileParsesDownstreamNonStreamStrategyOverride(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nDOWNSTREAM_NON_STREAM_STRATEGY_OVERRIDE=upstream_non_stream\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if !provider.DownstreamNonStreamStrategyOverrideSet {
		t.Fatalf("expected downstream non-stream strategy override to be marked as set")
	}
	if got := provider.DownstreamNonStreamStrategyOverride; got != DownstreamNonStreamStrategyUpstreamNonStream {
		t.Fatalf("expected provider override %q, got %q", DownstreamNonStreamStrategyUpstreamNonStream, got)
	}
	if got := provider.EffectiveDownstreamNonStreamStrategy(DownstreamNonStreamStrategyProxyBuffer); got != DownstreamNonStreamStrategyUpstreamNonStream {
		t.Fatalf("expected provider override to win, got %q", got)
	}

	providerBody = "PROVIDER_ID=openai\nDOWNSTREAM_NON_STREAM_STRATEGY_OVERRIDE=\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("rewrite provider env: %v", err)
	}
	provider, err = loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error after blank override: %v", err)
	}
	if !provider.DownstreamNonStreamStrategyOverrideSet {
		t.Fatalf("expected blank downstream non-stream strategy override to be marked as set")
	}
	if got := provider.EffectiveDownstreamNonStreamStrategy(DownstreamNonStreamStrategyProxyBuffer); got != DownstreamNonStreamStrategyProxyBuffer {
		t.Fatalf("expected blank override to inherit root strategy, got %q", got)
	}

	providerBody = "PROVIDER_ID=openai\nDOWNSTREAM_NON_STREAM_STRATEGY_OVERRIDE=bad-mode\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("rewrite provider env with invalid strategy: %v", err)
	}
	if _, err := loadProviderFile(providerEnvPath); err == nil {
		t.Fatalf("expected invalid downstream non-stream strategy override to fail validation")
	}
}

func TestLoadProviderFileUsesResponsesUpstreamEndpointByDefault(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.UpstreamEndpointType != UpstreamEndpointTypeResponses {
		t.Fatalf("expected default upstream endpoint type %q, got %q", UpstreamEndpointTypeResponses, provider.UpstreamEndpointType)
	}
}

func TestLoadProviderFileParsesUpstreamEndpointType(t *testing.T) {
	for _, endpointType := range []string{UpstreamEndpointTypeResponses, UpstreamEndpointTypeChat, UpstreamEndpointTypeAnthropic} {
		t.Run(endpointType, func(t *testing.T) {
			rootDir := t.TempDir()
			providerEnvPath := filepath.Join(rootDir, "openai.env")
			providerBody := "PROVIDER_ID=openai\nUPSTREAM_ENDPOINT_TYPE=" + endpointType + "\n"
			if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
				t.Fatalf("write provider env: %v", err)
			}

			provider, err := loadProviderFile(providerEnvPath)
			if err != nil {
				t.Fatalf("loadProviderFile returned error: %v", err)
			}
			if provider.UpstreamEndpointType != endpointType {
				t.Fatalf("expected upstream endpoint type %q, got %q", endpointType, provider.UpstreamEndpointType)
			}
		})
	}
}

func TestLoadProviderFileRejectsInvalidUpstreamEndpointType(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nUPSTREAM_ENDPOINT_TYPE=invalid\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	_, err := loadProviderFile(providerEnvPath)
	if err == nil {
		t.Fatalf("expected invalid upstream endpoint type to fail validation")
	}
	if _, ok := err.(invalidConfigError); !ok {
		t.Fatalf("expected invalidConfigError for invalid upstream endpoint type, got %T", err)
	}
	if err.Error() != "invalid UPSTREAM_ENDPOINT_TYPE in "+providerEnvPath+": \"invalid\"" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLoadProviderFileParsesOpenAIServiceTier(t *testing.T) {
	for _, tier := range []string{OpenAIServiceTierAuto, OpenAIServiceTierDefault, OpenAIServiceTierFlex, OpenAIServiceTierPriority} {
		t.Run(tier, func(t *testing.T) {
			rootDir := t.TempDir()
			providerEnvPath := filepath.Join(rootDir, "openai.env")
			providerBody := "PROVIDER_ID=openai\nOPENAI_SERVICE_TIER=" + tier + "\n"
			if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
				t.Fatalf("write provider env: %v", err)
			}

			provider, err := loadProviderFile(providerEnvPath)
			if err != nil {
				t.Fatalf("loadProviderFile returned error: %v", err)
			}
			if provider.OpenAIServiceTier != tier {
				t.Fatalf("expected openai service tier %q, got %q", tier, provider.OpenAIServiceTier)
			}
		})
	}
}

func TestLoadProviderFileRejectsUnknownOpenAIServiceTier(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nOPENAI_SERVICE_TIER=fast\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	_, err := loadProviderFile(providerEnvPath)
	if err == nil {
		t.Fatalf("expected invalid openai service tier to fail validation")
	}
	if _, ok := err.(invalidConfigError); !ok {
		t.Fatalf("expected invalidConfigError for invalid openai service tier, got %T", err)
	}
	if err.Error() != "invalid OPENAI_SERVICE_TIER in "+providerEnvPath+": \"fast\" (allowed: auto, default, flex, priority)" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLoadProviderFileUsesPreserveResponsesToolCompatModeByDefault(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if got := responsesToolCompatModeFromField(t, provider); got != "preserve" {
		t.Fatalf("expected default responses tool compat mode %q, got %q", "preserve", got)
	}
}

func TestLoadProviderFileParsesResponsesToolCompatMode(t *testing.T) {
	for _, mode := range []string{"preserve", "function_only"} {
		t.Run(mode, func(t *testing.T) {
			rootDir := t.TempDir()
			providerEnvPath := filepath.Join(rootDir, "openai.env")
			providerBody := "PROVIDER_ID=openai\nRESPONSES_TOOL_COMPAT_MODE=" + mode + "\n"
			if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
				t.Fatalf("write provider env: %v", err)
			}

			provider, err := loadProviderFile(providerEnvPath)
			if err != nil {
				t.Fatalf("loadProviderFile returned error: %v", err)
			}
			if got := responsesToolCompatModeFromField(t, provider); got != mode {
				t.Fatalf("expected responses tool compat mode %q, got %q", mode, got)
			}
		})
	}
}

func TestLoadProviderFileRejectsUnknownResponsesToolCompatMode(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nRESPONSES_TOOL_COMPAT_MODE=invalid\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	_, err := loadProviderFile(providerEnvPath)
	if err == nil {
		t.Fatalf("expected invalid responses tool compat mode to fail validation")
	}
	if _, ok := err.(invalidConfigError); !ok {
		t.Fatalf("expected invalidConfigError for invalid responses tool compat mode, got %T", err)
	}
	if err.Error() != "invalid RESPONSES_TOOL_COMPAT_MODE in "+providerEnvPath+": \"invalid\" (allowed: preserve, function_only)" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLoadProviderFileParsesUpstreamXMLToolCallStyle(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  string
	}{
		{value: "true", want: UpstreamXMLToolCallStyleLegacy},
		{value: "false", want: UpstreamXMLToolCallStyleOff},
	} {
		t.Run(tc.value, func(t *testing.T) {
			rootDir := t.TempDir()
			providerEnvPath := filepath.Join(rootDir, "openai.env")
			providerBody := "PROVIDER_ID=openai\nUPSTREAM_XML_TOOL_CALL_STYLE=" + tc.value + "\n"
			if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
				t.Fatalf("write provider env: %v", err)
			}

			provider, err := loadProviderFile(providerEnvPath)
			if err != nil {
				t.Fatalf("loadProviderFile returned error: %v", err)
			}
			if provider.UpstreamXMLToolCallStyle != tc.want {
				t.Fatalf("expected XML tool call style %q, got %q", tc.want, provider.UpstreamXMLToolCallStyle)
			}
		})
	}
}

func TestLoadProviderFileDefaultsUpstreamXMLToolCallStyleToLegacy(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}
	if provider.UpstreamXMLToolCallStyle != UpstreamXMLToolCallStyleLegacy {
		t.Fatalf("expected default XML tool call style %q, got %q", UpstreamXMLToolCallStyleLegacy, provider.UpstreamXMLToolCallStyle)
	}
}

func TestLoadProviderFileRejectsInvalidUpstreamXMLToolCallStyle(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nUPSTREAM_XML_TOOL_CALL_STYLE=maybe\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	_, err := loadProviderFile(providerEnvPath)
	if err == nil {
		t.Fatalf("expected invalid UPSTREAM_XML_TOOL_CALL_STYLE to fail validation")
	}
	if err.Error() != "invalid UPSTREAM_XML_TOOL_CALL_STYLE in "+providerEnvPath+": \"maybe\"" {
		t.Fatalf("unexpected error message: %v", err)
	}
}
