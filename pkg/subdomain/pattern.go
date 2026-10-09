// Package subdomain 提供子域名模式的解析与匹配。
// 模板占位符展开（捕获组 + 内置变量）见 pkg/vars。
//
// 模式语法：
//   - `*`：匹配 `[a-z0-9-]+`（小写字母、数字与连字符，不可为空，不含 `.`），
//     每处 `*` 在结果正则中形成一个捕获组，按出现顺序编号 1、2、3 ...
//   - `/.../`：原始正则段。两斜杠之间的内容按正则语法直接拼入结果正则，
//     支持反斜杠转义（`\/` 表示字面 `/`、`\\` 表示字面 `\`）。
//     每段用 `(?:...)` 包裹，使段内的 `|` 等元字符不会越界影响整体结构
//     （也避免非管理员前缀被 `|` 绕过）。可出现多段，也可整体为一正则。
//   - 其他字符按字面量处理（自动转义正则元字符）。
//
// 整个模式以 `^...$` 锚定，要求子域名完整匹配。
//
// 字面量字符数（LiteralLen）：模式中非 `*` 且不在 `/.../` 段内的字符数，
// 用于模糊匹配优先级排序——字面量越多表示模式越具体，优先级越高。
// 例：`local-*` → 6，`*-dev` → 4，`/.*/ ` → 0。
//
// 例：
//
//	*-dev          → ^([a-z0-9-]+)\-dev$
//	*-*-dev         → ^([a-z0-9-]+)\-([a-z0-9-]+)\-dev$
//	dev-/([0-9]{2,5})/   → ^dev\-(?:([0-9]{2,5}))$
//	pre-/([a-z]{2})/-*-post → ^pre\-(?:([a-z]{2}))\-([a-z0-9-]+)\-post$
package subdomain

import (
	"fmt"
	"regexp"
	"strings"
)

// Pattern 已编译的子域名模式。
type Pattern struct {
	raw        string
	regexp     *regexp.Regexp
	literalLen int // 字面量字符数（非 * 且不在 /.../ 段内的字符数），用于模糊匹配优先级排序
}

// IsFuzzy 判断模式字符串是否为模糊匹配（包含 `*` 或 `/`）。
// 非模糊模式走精确匹配路径，无需调用 Parse。
func IsFuzzy(pattern string) bool {
	return strings.ContainsAny(pattern, "*/")
}

// Parse 将模式字符串编译为 Pattern。
func Parse(pattern string) (*Pattern, error) {
	re, litLen, err := parseToRegexp(pattern)
	if err != nil {
		return nil, err
	}
	return &Pattern{raw: pattern, regexp: re, literalLen: litLen}, nil
}

// Raw 返回原始模式字符串。
func (p *Pattern) Raw() string { return p.raw }

// Regexp 返回编译后的底层正则表达式。
func (p *Pattern) Regexp() *regexp.Regexp { return p.regexp }

// LiteralLen 返回模式中字面量字符数（非 `*` 且不在 `/.../` 段内的字符数）。
// 用于模糊匹配优先级排序：字面量越多表示模式越具体，优先级越高。
func (p *Pattern) LiteralLen() int { return p.literalLen }

// Match 尝试匹配子域名，返回捕获组（不含完整匹配项）；无匹配返回 nil。
func (p *Pattern) Match(subdomain string) []string {
	m := p.regexp.FindStringSubmatch(subdomain)
	if m == nil {
		return nil
	}
	return m[1:]
}

// parseToRegexp 将模式字符串转换为已编译的正则表达式，同时返回字面量字符数。
func parseToRegexp(pattern string) (*regexp.Regexp, int, error) {
	if pattern == "" {
		return nil, 0, fmt.Errorf("子域名模式不能为空")
	}
	var sb strings.Builder
	sb.WriteByte('^')
	literalLen := 0
	i := 0
	for i < len(pattern) {
		c := pattern[i]
		switch c {
		case '*':
			// `-` 置于字符类末尾，是字面量而非范围符，无需转义
			sb.WriteString("([a-z0-9-]+)")
			i++
		case '/':
			// 寻找下一个未转义的 '/' 作为闭合
			j := i + 1
			var seg strings.Builder
			closed := false
			for j < len(pattern) {
				if pattern[j] == '\\' && j+1 < len(pattern) {
					// 反斜杠转义：保留下一字符（如 \/ → /，\\ → \）
					seg.WriteByte(pattern[j+1])
					j += 2
					continue
				}
				if pattern[j] == '/' {
					closed = true
					break
				}
				seg.WriteByte(pattern[j])
				j++
			}
			if !closed {
				return nil, 0, fmt.Errorf("子域名模式存在未闭合的 // 正则段")
			}
			// 用 (?:...) 包裹，防止段内 | 等元字符越界
			sb.WriteString("(?:")
			sb.WriteString(seg.String())
			sb.WriteByte(')')
			// /.../ 段内字符不计入字面量长度（整段视为模糊）
			i = j + 1
		default:
			if isRegexpMeta(c) {
				sb.WriteByte('\\')
			}
			sb.WriteByte(c)
			literalLen++
			i++
		}
	}
	sb.WriteByte('$')
	re, err := regexp.Compile(sb.String())
	if err != nil {
		return nil, 0, fmt.Errorf("子域名模式编译失败: %w", err)
	}
	return re, literalLen, nil
}

// isRegexpMeta 判断字节是否为正则元字符（需要转义）。
func isRegexpMeta(c byte) bool {
	switch c {
	case '.', '+', '?', '(', ')', '[', ']', '{', '}', '^', '$', '|', '\\':
		return true
	}
	return false
}

// Expand 已迁移至 pkg/vars.Expand（#50）：统一处理捕获组占位与命名内置变量，
// 子域名包只保留模式的解析与匹配职责。

// parseDigits 已随 Expand 迁移至 pkg/vars。
