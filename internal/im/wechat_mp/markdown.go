package wechatmp

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const defaultWechatTextChunkLimit = 550

var (
	thinkBlockRe        = regexp.MustCompile(`(?is)<think\b[^>]*>.*?</think>`)
	citationTagRe       = regexp.MustCompile(`(?is)<(?:kb|web)\b[^>]*/?>`)
	fencedCodeBlockRe   = regexp.MustCompile("(?s)```[^\\n`]*\\n?(.*?)```")
	markdownImageRe     = regexp.MustCompile(`!\[([^\]]*)\]\([^)]+\)`)
	markdownLinkRe      = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	markdownEmphasisRe  = regexp.MustCompile(`(\*\*|__|\*|_|~~)([^*_~]+)(\*\*|__|\*|_|~~)`)
	markdownHeadingRe   = regexp.MustCompile(`^(#{1,6})\s*(.+)$`)
	markdownTableSepRe  = regexp.MustCompile(`^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)+\|?\s*$`)
	inlineCodeRe        = regexp.MustCompile("`([^`]+)`")
	multipleBlankLineRe = regexp.MustCompile(`\n{3,}`)
)

func simplifyForWeChat(input string) string {
	text := strings.TrimSpace(input)
	if text == "" {
		return ""
	}

	text = thinkBlockRe.ReplaceAllString(text, "")
	text = citationTagRe.ReplaceAllString(text, "")
	text = fencedCodeBlockRe.ReplaceAllStringFunc(text, func(block string) string {
		matches := fencedCodeBlockRe.FindStringSubmatch(block)
		if len(matches) < 2 {
			return ""
		}
		lines := strings.Split(strings.Trim(matches[1], "\n"), "\n")
		if len(lines) > 20 {
			lines = append(lines[:20], "...(已省略更多代码)")
		}
		for i, line := range lines {
			lines[i] = "    " + line
		}
		return strings.Join(lines, "\n")
	})
	text = markdownImageRe.ReplaceAllStringFunc(text, func(match string) string {
		parts := markdownImageRe.FindStringSubmatch(match)
		if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
			return "[图片] " + strings.TrimSpace(parts[1])
		}
		return "[图片]"
	})
	text = markdownLinkRe.ReplaceAllString(text, "$1")
	text = inlineCodeRe.ReplaceAllString(text, "$1")
	for {
		next := markdownEmphasisRe.ReplaceAllString(text, "$2")
		if next == text {
			break
		}
		text = next
	}

	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if markdownTableSepRe.MatchString(trimmed) {
			continue
		}
		if matches := markdownHeadingRe.FindStringSubmatch(trimmed); len(matches) == 3 {
			out = append(out, "【"+strings.TrimSpace(matches[2])+"】")
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			out = append(out, "｜"+strings.TrimSpace(strings.TrimLeft(trimmed, ">")))
			continue
		}
		if strings.Contains(trimmed, "|") {
			cols := strings.Split(strings.Trim(trimmed, "|"), "|")
			for i, col := range cols {
				cols[i] = strings.TrimSpace(col)
			}
			line = strings.Join(cols, " | ")
		}
		out = append(out, line)
	}

	text = strings.Join(out, "\n")
	text = multipleBlankLineRe.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

func splitForWeChat(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if limit <= 0 {
		limit = defaultWechatTextChunkLimit
	}
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}

	var chunks []string
	remaining := text
	for strings.TrimSpace(remaining) != "" {
		if utf8.RuneCountInString(remaining) <= limit {
			chunks = append(chunks, strings.TrimSpace(remaining))
			break
		}
		cut := bestSplitIndex(remaining, limit)
		chunk := strings.TrimSpace(remaining[:cut])
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		remaining = strings.TrimSpace(remaining[cut:])
	}
	return chunks
}

func bestSplitIndex(text string, limit int) int {
	runesSeen := 0
	byteLimit := len(text)
	for idx := range text {
		if runesSeen == limit {
			byteLimit = idx
			break
		}
		runesSeen++
	}

	prefix := text[:byteLimit]
	for _, sep := range []string{"\n\n", "\n", "。", "？", "！", ". ", "? ", "! ", "，", ", "} {
		if idx := strings.LastIndex(prefix, sep); idx > 0 {
			return idx + len(sep)
		}
	}
	return byteLimit
}
