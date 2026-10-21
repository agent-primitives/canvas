package canvas

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// EditSection is a selector, never a fallback to a whole-document edit.
type EditSection struct {
	Kind        string   `json:"kind"`
	Pointer     *string  `json:"pointer,omitempty"`
	HeadingPath []string `json:"headingPath,omitempty"`
	Occurrence  int      `json:"occurrence,omitempty"`
}

// EditScope freezes selection evidence at the supplied base. HeadingPath is
// copied from the request; consumers must validate this value against the base.
type EditScope struct {
	Kind        string   `json:"kind"`
	Pointer     string   `json:"pointer,omitempty"`
	HeadingPath []string `json:"headingPath,omitempty"`
	Occurrence  int      `json:"occurrence,omitempty"`
	Start       int      `json:"start"`
	End         int      `json:"end"`
	Hash        string   `json:"hash"`
}

// ErrEditScope identifies invalid selectors, stale evidence, and scope escapes.
var ErrEditScope = errors.New("edit scope invalid or changed")

func editScopeHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func editScopeText(content string) bool {
	return utf8.ValidString(content) && safeText(content, 65536)
}

func resolveEditScope(content string, section *EditSection) (EditScope, error) {
	if !editScopeText(content) {
		return EditScope{}, ErrEditScope
	}
	if section == nil {
		return EditScope{Kind: "whole", End: len(content), Hash: editScopeHash(content)}, nil
	}
	switch section.Kind {
	case "json-pointer":
		if section.Pointer == nil || len(section.HeadingPath) != 0 || section.Occurrence != 0 {
			return EditScope{}, ErrEditScope
		}
		parts, err := editScopePointerParts(*section.Pointer)
		if err != nil {
			return EditScope{}, err
		}
		value, err := editScopeJSON(content)
		if err != nil {
			return EditScope{}, err
		}
		selected, ok := editScopeJSONSelect(value, parts)
		if !ok {
			return EditScope{}, ErrEditScope
		}
		// Marshal sorts object keys, retaining json.Number's exact spelling.
		canonical, err := json.Marshal(selected)
		if err != nil {
			return EditScope{}, ErrEditScope
		}
		return EditScope{Kind: section.Kind, Pointer: *section.Pointer, Hash: editScopeHash(string(canonical))}, nil
	case "markdown-heading":
		if section.Pointer != nil || len(section.HeadingPath) == 0 || len(section.HeadingPath) > 16 || section.Occurrence < 1 {
			return EditScope{}, ErrEditScope
		}
		for _, title := range section.HeadingPath {
			if len(title) > 128 || !utf8.ValidString(title) {
				return EditScope{}, ErrEditScope
			}
		}
		start, end, err := editScopeMarkdownRange(content, section.HeadingPath, section.Occurrence)
		if err != nil {
			return EditScope{}, err
		}
		return EditScope{Kind: section.Kind, HeadingPath: slices.Clone(section.HeadingPath), Occurrence: section.Occurrence, Start: start, End: end, Hash: editScopeHash(content[start:end])}, nil
	default:
		return EditScope{}, ErrEditScope
	}
}

func validateEditScope(before, after string, scope EditScope) error {
	if !editScopeText(after) {
		return ErrEditScope
	}
	var section *EditSection
	switch scope.Kind {
	case "whole":
	case "json-pointer":
		pointer := scope.Pointer
		section = &EditSection{Kind: scope.Kind, Pointer: &pointer}
	case "markdown-heading":
		section = &EditSection{Kind: scope.Kind, HeadingPath: scope.HeadingPath, Occurrence: scope.Occurrence}
	default:
		return ErrEditScope
	}
	frozen, err := resolveEditScope(before, section)
	if err != nil || frozen.Kind != scope.Kind || frozen.Pointer != scope.Pointer ||
		!slices.Equal(frozen.HeadingPath, scope.HeadingPath) || frozen.Occurrence != scope.Occurrence ||
		frozen.Start != scope.Start || frozen.End != scope.End || frozen.Hash != scope.Hash {
		return ErrEditScope
	}
	switch scope.Kind {
	case "whole":
		return nil
	case "json-pointer":
		original, err := editScopeJSON(before)
		if err != nil {
			return ErrEditScope
		}
		candidate, err := editScopeJSON(after)
		if err != nil {
			return ErrEditScope
		}
		parts, err := editScopePointerParts(scope.Pointer)
		if err != nil {
			return ErrEditScope
		}
		if !editScopeJSONOutsideEqual(original, candidate, parts) {
			return ErrEditScope
		}
	case "markdown-heading":
		// The candidate range may grow or shrink, but the frozen prefix and suffix
		// cannot overlap or be absorbed by a shorter replacement.
		candidateEnd := scope.End + len(after) - len(before)
		if candidateEnd < scope.Start || candidateEnd > len(after) ||
			after[:scope.Start] != before[:scope.Start] || after[candidateEnd:] != before[scope.End:] {
			return ErrEditScope
		}
	}
	return nil
}

func editScopeJSON(content string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	value, err := editJSONValue(decoder, 0)
	if err != nil {
		return nil, ErrEditScope
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, ErrEditScope
	}
	return value, nil
}

// Token decoding retains arbitrary numeric lexemes. Suite's JCS decoder is not
// used here: it intentionally rejects numbers outside its float64 domain.
func editJSONValue(d *json.Decoder, depth int) (any, error) {
	if depth > 128 {
		return nil, ErrEditScope
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			m := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				s, ok := key.(string)
				if !ok || strings.ContainsRune(s, '\uFFFD') {
					return nil, ErrEditScope
				}
				if _, exists := m[s]; exists {
					return nil, ErrEditScope
				}
				v, err := editJSONValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				m[s] = v
			}
			close, err := d.Token()
			if err != nil || close != json.Delim('}') {
				return nil, ErrEditScope
			}
			return m, nil
		case '[':
			a := []any{}
			for d.More() {
				v, err := editJSONValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			close, err := d.Token()
			if err != nil || close != json.Delim(']') {
				return nil, ErrEditScope
			}
			return a, nil
		default:
			return nil, ErrEditScope
		}
	}
	if s, ok := t.(string); ok && strings.ContainsRune(s, '\uFFFD') {
		return nil, ErrEditScope
	}
	return t, nil
}

func editScopePointerParts(pointer string) ([]string, error) {
	if len(pointer) > 512 || !utf8.ValidString(pointer) {
		return nil, ErrEditScope
	}
	if pointer == "" {
		return nil, nil
	}
	if pointer[0] != '/' {
		return nil, ErrEditScope
	}
	parts := strings.Split(pointer[1:], "/")
	for i, part := range parts {
		var decoded strings.Builder
		for j := 0; j < len(part); j++ {
			if part[j] != '~' {
				decoded.WriteByte(part[j])
				continue
			}
			j++
			if j >= len(part) {
				return nil, ErrEditScope
			}
			switch part[j] {
			case '0':
				decoded.WriteByte('~')
			case '1':
				decoded.WriteByte('/')
			default:
				return nil, ErrEditScope
			}
		}
		parts[i] = decoded.String()
	}
	return parts, nil
}

func editScopeArrayIndex(part string, length int) (int, bool) {
	if part == "" || len(part) > 1 && part[0] == '0' {
		return 0, false
	}
	for i := 0; i < len(part); i++ {
		if part[i] < '0' || part[i] > '9' {
			return 0, false
		}
	}
	index, err := strconv.Atoi(part)
	return index, err == nil && index >= 0 && index < length
}

func editScopeJSONSelect(value any, parts []string) (any, bool) {
	for _, part := range parts {
		switch node := value.(type) {
		case map[string]any:
			child, ok := node[part]
			if !ok {
				return nil, false
			}
			value = child
		case []any:
			index, ok := editScopeArrayIndex(part, len(node))
			if !ok {
				return nil, false
			}
			value = node[index]
		default:
			return nil, false
		}
	}
	return value, true
}

// Ancestors must retain shape and all siblings must retain their decoded value.
// Only the selected node may change type; json.Number equality is lexical.
func editScopeJSONOutsideEqual(before, after any, parts []string) bool {
	if len(parts) == 0 {
		return true
	}
	part := parts[0]
	switch original := before.(type) {
	case map[string]any:
		candidate, ok := after.(map[string]any)
		if !ok || len(original) != len(candidate) {
			return false
		}
		selected, ok := original[part]
		replacement, exists := candidate[part]
		if !ok || !exists {
			return false
		}
		for key, value := range original {
			other, exists := candidate[key]
			if !exists || key != part && !reflect.DeepEqual(value, other) {
				return false
			}
		}
		return editScopeJSONOutsideEqual(selected, replacement, parts[1:])
	case []any:
		candidate, ok := after.([]any)
		if !ok || len(original) != len(candidate) {
			return false
		}
		index, ok := editScopeArrayIndex(part, len(original))
		if !ok {
			return false
		}
		for i, value := range original {
			if i != index && !reflect.DeepEqual(value, candidate[i]) {
				return false
			}
		}
		return editScopeJSONOutsideEqual(original[index], candidate[index], parts[1:])
	default:
		return false
	}
}

type editScopeHeading struct {
	level, start int
	title        string
}

// This intentionally supports conservative ATX headings, not CommonMark.
// Unclosed fences and ambiguous Setext paragraphs refuse rather than widen.
func editScopeMarkdownRange(content string, path []string, occurrence int) (int, int, error) {
	var stack []editScopeHeading
	start, end, selectedLevel, matched := -1, len(content), 0, 0
	fenceByte := byte(0)
	fenceLength := 0
	previousParagraph := false
	for offset := 0; offset < len(content); {
		next := strings.IndexByte(content[offset:], '\n')
		lineEnd := len(content)
		if next >= 0 {
			lineEnd = offset + next + 1
		}
		line := strings.TrimSuffix(strings.TrimSuffix(content[offset:lineEnd], "\n"), "\r")
		indent := 0
		for indent < len(line) && line[indent] == ' ' {
			indent++
		}
		text := line[indent:]
		if fenceByte != 0 {
			if indent <= 3 && len(text) > 0 && text[0] == fenceByte {
				count := 0
				for count < len(text) && text[count] == fenceByte {
					count++
				}
				if count >= fenceLength && strings.TrimSpace(text[count:]) == "" {
					fenceByte = 0
				}
			}
			previousParagraph = false
			offset = lineEnd
			continue
		}
		if indent >= 4 || strings.HasPrefix(text, "\t") {
			previousParagraph = false
			offset = lineEnd
			continue
		}
		if len(text) > 0 && (text[0] == 96 || text[0] == '~') {
			count := 0
			for count < len(text) && text[count] == text[0] {
				count++
			}
			if count >= 3 {
				// Backticks in a backtick info string are unsupported/ambiguous.
				if text[0] == 96 && strings.IndexByte(text[count:], 96) >= 0 {
					return 0, 0, ErrEditScope
				}
				fenceByte, fenceLength = text[0], count
				previousParagraph = false
				offset = lineEnd
				continue
			}
		}
		if previousParagraph && editScopeSetext(text) {
			return 0, 0, ErrEditScope
		}
		level, title := editScopeATX(text)
		if level != 0 {
			if start >= 0 && end == len(content) && level <= selectedLevel {
				end = offset
			}
			for len(stack) > 0 && stack[len(stack)-1].level >= level {
				stack = stack[:len(stack)-1]
			}
			stack = append(stack, editScopeHeading{level: level, start: offset, title: title})
			equal := len(stack) == len(path)
			if equal {
				for i, heading := range stack {
					if heading.title != path[i] {
						equal = false
						break
					}
				}
			}
			if equal {
				matched++
				if matched == occurrence {
					start, selectedLevel = offset, level
				}
			}
			previousParagraph = false
		} else {
			previousParagraph = strings.TrimSpace(text) != ""
		}
		offset = lineEnd
	}
	if fenceByte != 0 || start < 0 {
		return 0, 0, ErrEditScope
	}
	return start, end, nil
}

func editScopeATX(text string) (int, string) {
	level := 0
	for level < len(text) && text[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level < len(text) && text[level] != ' ' && text[level] != '\t' {
		return 0, ""
	}
	title := strings.TrimSpace(text[level:])
	last := len(title)
	for last > 0 && title[last-1] == '#' {
		last--
	}
	if last < len(title) && (last == 0 || title[last-1] == ' ' || title[last-1] == '\t') {
		title = strings.TrimSpace(title[:last])
	}
	return level, title
}

func editScopeSetext(text string) bool {
	text = strings.TrimRight(text, " \t")
	if text == "" || text[0] != '=' && text[0] != '-' {
		return false
	}
	for i := 1; i < len(text); i++ {
		if text[i] != text[0] {
			return false
		}
	}
	return true
}
