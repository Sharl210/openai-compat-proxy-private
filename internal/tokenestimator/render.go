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
	b.WriteString("\n")
	b.WriteString("学到的请求特征:\n")
	b.WriteString(fmt.Sprintf("- 文本字符: %.0f\n", state.AvgTextChars))
	b.WriteString(fmt.Sprintf("- 输入项数量: %.2f\n", state.AvgInputItemCount))
	b.WriteString(fmt.Sprintf("- 推理项数量: %.2f\n", state.AvgReasoningItemCount))
	b.WriteString(fmt.Sprintf("- 工具调用数量: %.2f\n", state.AvgToolCallCount))
	b.WriteString(fmt.Sprintf("- 工具结果数量: %.2f\n", state.AvgToolResultCount))
	b.WriteString(fmt.Sprintf("- 多模态项数量: %.2f\n", state.AvgMultimodalItemCount))
	b.WriteString("\n")
	b.WriteString("当前估算规则:\n")
	b.WriteString("- 基础估算: 先统计请求特征里的文本字符总数，再按 字符数 ÷ 4 估出基础 token\n")
	b.WriteString(fmt.Sprintf("- 平均基础估算 token: %.2f\n", state.AvgBaseEstimate))
	b.WriteString(fmt.Sprintf("- 平均真实输入 token: %.2f\n", state.AvgInputTokens))
	b.WriteString(fmt.Sprintf("- 平均缓存命中 token: %.2f\n", state.AvgCachedTokens))
	b.WriteString(fmt.Sprintf("- 平均未缓存输入 token: %.2f\n", state.AvgUncachedInputTokens))
	b.WriteString("- 总修正方法: 用 真实输入 token ÷ 基础估算 token 得到修正倍率\n")
	b.WriteString(fmt.Sprintf("- 平均总修正系数: %.4f\n", state.AvgTotalRatio))
	b.WriteString(fmt.Sprintf("- 当前总修正系数: %.4f\n", state.RollingTotalCorrection))
	b.WriteString(fmt.Sprintf("- 当前未缓存修正系数: %.4f\n", state.RollingUncachedCorrection))
	b.WriteString("\n")
	b.WriteString("可信度判断:\n")
	b.WriteString("- 学习对象是单次请求样本的特征，不区分你来自哪个终端或哪段会话\n")
	b.WriteString("- 只有样本量足够且修正稳定后，才适合参与运行时止损\n")
	b.WriteString(fmt.Sprintf("- 当前可用性: %t\n", state.RuntimeReady))
	return b.String()
}
