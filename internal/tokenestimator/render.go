package tokenestimator

import (
	"fmt"
	"strings"
)

func RenderBucketState(state BucketState) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("提供商: %s\n", state.ProviderID))
	b.WriteString(fmt.Sprintf("上游端点类型: %s\n", state.EndpointType))
	b.WriteString(fmt.Sprintf("最终上游模型: %s\n", state.FinalUpstreamRawModel))
	b.WriteString(fmt.Sprintf("样本总数: %d\n", state.SampleCount))
	b.WriteString(fmt.Sprintf("有效样本数: %d\n", state.UsableSampleCount))
	b.WriteString(fmt.Sprintf("置信等级: %s\n", state.ConfidenceLevel))
	b.WriteString(fmt.Sprintf("是否可用于运行时止损: %t\n", state.RuntimeReady))
	b.WriteString(fmt.Sprintf("平均基础估算 token: %.2f\n", state.AvgBaseEstimate))
	b.WriteString(fmt.Sprintf("平均真实输入 token: %.2f\n", state.AvgInputTokens))
	b.WriteString(fmt.Sprintf("平均缓存命中 token: %.2f\n", state.AvgCachedTokens))
	b.WriteString(fmt.Sprintf("平均未缓存输入 token: %.2f\n", state.AvgUncachedInputTokens))
	b.WriteString(fmt.Sprintf("建议未缓存修正系数: %.4f\n", state.RollingUncachedCorrection))
	return b.String()
}
