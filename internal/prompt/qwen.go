package prompt

import (
	"regexp"
	"strings"
)

var (
	reRefLabelZh  = regexp.MustCompile(`(?s)^参考图片编号：.*?\r?\n\r?\n`)
	reRefLabelEn  = regexp.MustCompile(`(?s)^Reference image labels:.*?\r?\n\r?\n`)
	reTargetImage = regexp.MustCompile(`原图|底图|目标图`)
	reImageNumber = regexp.MustCompile(`图片\s*([1-9]|10)`)
	// Go's regexp engine has no lookahead, so `图N` is matched directly. That is
	// equivalent here: "图片N" was already rewritten by the pass above, and `图`
	// followed by `片` can never match `\s*([1-9]|10)`.
	reTuNumber    = regexp.MustCompile(`图\s*([1-9]|10)`)
	reImageHan    = regexp.MustCompile(`图片([一二三四五六七八九十])`)
	reTuHan       = regexp.MustCompile(`图([一二三四五六七八九十])`)
	reZhangNumber = regexp.MustCompile(`第\s*([1-9]|10)\s*张`)
	reZhangHan    = regexp.MustCompile(`第\s*([一二三四五六七八九十])\s*张`)
)

var cnNumerals = []string{"一", "二", "三", "四", "五", "六", "七", "八", "九", "十"}

// ReorderQwenEditPrompt renumbers "图片N" references after the file order changed.
//
// Qwen multi-image edit sends the LAST uploaded image as the target and the earlier
// ones as references, so every "图片2" in the user's prompt now points at the wrong
// picture. Rewriting the labels keeps the user's intent intact.
func ReorderQwenEditPrompt(text string, count int) string {
	total := count
	if total < 2 {
		return strings.TrimSpace(text)
	}

	cleaned := strings.TrimSpace(strings.TrimPrefix(text, "\uFEFF"))
	cleaned = reRefLabelZh.ReplaceAllString(cleaned, "")
	cleaned = reRefLabelEn.ReplaceAllString(cleaned, "")
	cleaned = strings.TrimSpace(cleaned)

	// Image 1 becomes image N (the target), and images 2..N shift down by one.
	mapped := func(index int) int {
		switch {
		case index == 1:
			return total
		case index >= 2 && index <= total:
			return index - 1
		}
		return index
	}

	cleaned = reTargetImage.ReplaceAllString(cleaned, "__TARGET_IMAGE__")
	cleaned = reImageNumber.ReplaceAllStringFunc(cleaned, func(match string) string {
		groups := reImageNumber.FindStringSubmatch(match)
		return "图片" + itoaSmall(mapped(atoiSmall(groups[1])))
	})
	cleaned = reTuNumber.ReplaceAllStringFunc(cleaned, func(match string) string {
		groups := reTuNumber.FindStringSubmatch(match)
		return "图片" + itoaSmall(mapped(atoiSmall(groups[1])))
	})
	cleaned = reImageHan.ReplaceAllStringFunc(cleaned, func(match string) string {
		groups := reImageHan.FindStringSubmatch(match)
		return "图片" + itoaSmall(mapped(hanIndex(groups[1])))
	})
	cleaned = reTuHan.ReplaceAllStringFunc(cleaned, func(match string) string {
		groups := reTuHan.FindStringSubmatch(match)
		return "图片" + itoaSmall(mapped(hanIndex(groups[1])))
	})
	cleaned = reZhangNumber.ReplaceAllStringFunc(cleaned, func(match string) string {
		groups := reZhangNumber.FindStringSubmatch(match)
		return "图片" + itoaSmall(mapped(atoiSmall(groups[1])))
	})
	cleaned = reZhangHan.ReplaceAllStringFunc(cleaned, func(match string) string {
		groups := reZhangHan.FindStringSubmatch(match)
		return "图片" + itoaSmall(mapped(hanIndex(groups[1])))
	})
	cleaned = strings.ReplaceAll(cleaned, "__TARGET_IMAGE__", "图片"+itoaSmall(total))

	labels := make([]string, 0, total)
	for index := 1; index <= total; index++ {
		labels = append(labels, "图片"+itoaSmall(index))
	}
	header := "图片顺序：" + strings.Join(labels, "、") +
		"；其中图片" + itoaSmall(total) + "是目标图/原图，只编辑图片" + itoaSmall(total) +
		"，其他图片仅作为元素参考。\n\n"
	return header + cleaned
}

func hanIndex(value string) int {
	for index, numeral := range cnNumerals {
		if numeral == value {
			return index + 1
		}
	}
	return 0
}

func atoiSmall(value string) int {
	number := 0
	for _, char := range value {
		if char < '0' || char > '9' {
			return number
		}
		number = number*10 + int(char-'0')
	}
	return number
}

func itoaSmall(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
