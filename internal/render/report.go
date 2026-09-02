package render

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"defenseunicorns/uds-compose-bridge/internal/model"
)

func WriteConversionReport(root string, report model.ConversionReport) error {
	docsDir := filepath.Join(root, "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		return fmt.Errorf("create conversion report directory %s: %w", docsDir, err)
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode conversion report: %w", err)
	}
	data = append(data, '\n')
	jsonPath := filepath.Join(root, "conversion.json")
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		return fmt.Errorf("write conversion report %s: %w", jsonPath, err)
	}

	markdownPath := filepath.Join(docsDir, "conversion.md")
	if err := os.WriteFile(markdownPath, []byte(buildConversionMarkdown(report)), 0o644); err != nil {
		return fmt.Errorf("write conversion report %s: %w", markdownPath, err)
	}
	return nil
}

func buildConversionMarkdown(report model.ConversionReport) string {
	var content strings.Builder
	content.WriteString("# Conversion Report\n\n")
	content.WriteString("This report records how settings from the Compose model were handled during UDS package conversion.\n\n")
	writeDecisionSection(&content, "Translated settings", report.Translated, false)
	writeDecisionSection(&content, "Inferred settings", report.Inferred, false)
	writeDecisionSection(&content, "Ignored settings", report.Ignored, false)
	writeDecisionSection(&content, "Rejected settings", report.Rejected, true)
	return content.String()
}

func writeDecisionSection(content *strings.Builder, title string, decisions []model.ConversionDecision, rejected bool) {
	fmt.Fprintf(content, "## %s\n\n", title)
	if len(decisions) == 0 {
		content.WriteString("None.\n\n")
		return
	}

	if rejected {
		content.WriteString("| Setting | Code | Message | Remediation |\n")
		content.WriteString("| --- | --- | --- | --- |\n")
		for _, decision := range decisions {
			fmt.Fprintf(content, "| `%s` | `%s` | %s | %s |\n",
				markdownReportValue(decision.Path),
				markdownReportValue(decision.Code),
				markdownReportValue(decision.Message),
				markdownReportValue(decision.Remediation),
			)
		}
		content.WriteString("\n")
		return
	}

	content.WriteString("| Setting | Result | Details |\n")
	content.WriteString("| --- | --- | --- |\n")
	for _, decision := range decisions {
		result := decision.Target
		if decision.Value != "" {
			if result != "" {
				result += ": "
			}
			result += decision.Value
		}
		fmt.Fprintf(content, "| `%s` | %s | %s |\n",
			markdownReportValue(decision.Path),
			markdownReportValue(result),
			markdownReportValue(decision.Message),
		)
	}
	content.WriteString("\n")
}

func markdownReportValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\r\n", "<br>")
	value = strings.ReplaceAll(value, "\n", "<br>")
	return value
}
