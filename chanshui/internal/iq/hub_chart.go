package iq

import (
	"fmt"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
	"strings"
	"unicode"
)

func ChartPanelTemplates() []string {
	return []string{
		"ONE INTEGER ONLY / 只回复一个整数",
		"把下图 {{bar_count}} 根K线的「收盘价个位数字」全部相加，只返回这个和，不要解释。",
		"Sum the units digits of the {{bar_count}} closing prices. Reply with the number only.",
		"禁止联网、使用工具或写代码计算。只回答一个整数，不要单位、解释、标点或代码块。",
		"No web, no tools, no code. Reply with a single integer only -",
		"no units, no explanation, no punctuation, no code block.   Example: close 127 -> 7",
	}
}
func ValidateChartPanel(lines []string) error {
	if len(lines) != 6 {
		return fmt.Errorf("识图说明必须保持六行，请逐行编辑")
	}
	f := chartFaces()
	defer closeChartFaces(f)
	faces := []font.Face{f.en, f.big, f.sub, f.ban, f.sm, f.sm}
	ys := []int{34, 78, 126, 156, 196, 214}
	bottom := []int{78, 126, 156, 196, 214, 236}
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			return fmt.Errorf("识图说明第%d行不能为空", i+1)
		}
		face := faces[i]
		for _, r := range line {
			if unicode.IsControl(r) {
				return fmt.Errorf("识图说明第%d行含换行或控制字符，请删除", i+1)
			}
			if _, _, ok := face.GlyphBounds(r); !ok {
				return fmt.Errorf("识图说明第%d行的字符“%c”不在随包字库中，请换字", i+1, r)
			}
		}
		bounds, _ := font.BoundString(face, line)
		ascent := face.Metrics().Ascent
		x := fixed.I(52)
		y := fixed.I(ys[i]) + ascent
		if strW(face, line) > 1256 || bounds.Min.X+x < fixed.I(52) || bounds.Max.X+x > fixed.I(1308) || bounds.Min.Y+y < fixed.I(ys[i]) || bounds.Max.Y+y > fixed.I(bottom[i]) {
			return fmt.Errorf("识图说明第%d行超出图片中的固定区域，请缩短文字", i+1)
		}
	}
	return nil
}
func HubChart(seed int64, lines []string) (Question, error) {
	if err := ValidateChartPanel(lines); err != nil {
		return Question{}, err
	}
	return chartFromSeedWithPanel(seed, lines), nil
}

func closeChartFaces(f *chartFaceSet) {
	for _, face := range []font.Face{f.en, f.big, f.ban, f.sub, f.sm, f.title, f.tick} {
		_ = face.Close()
	}
}
