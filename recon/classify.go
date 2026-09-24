package recon

// ClassifyLine 是包内 classifyLine 的导出包装。
//
// 之所以导出：server 侧「资产文件导入」接口（POST /api/assets/import，见
// server/assets_import.go）需要对手工上传的 txt/csv/xlsx 逐行做与文本导入
// 收集器（ManualCollector）完全同一套语义分类（URL / 域名 / IP / host:port
// → RawAsset），而 classifyLine 本身是包内未导出函数。导出这个包装让两个
// 入口复用同一份分类实现，避免逻辑漂移；原函数保持原位不移动。
func ClassifyLine(line string) *RawAsset {
	return classifyLine(line)
}
