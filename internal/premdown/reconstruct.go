// Reconstruction of the fields Premiere 2026 drops from its sparser
// serialisation but that older releases require present.
//
// Two halves: a formatting-preserving mini-DOM (parseXML/el/render) and the
// pass that uses it (rebuild/reconstructPositionalClasses/verifyDowngraded).

package premdown

import (
	"fmt"
	"regexp"
	"strings"
)

// Fields that 2026 drops but that Premiere 2025 REQUIRES to be present for a
// 2026 project to open at all. 2025 reads these by name (field order is
// irrelevant); it is their absence that makes 2025 report the project as
// damaged, so we re-insert them.
//
// This set is deliberately narrow and was established by ablation. Only
// VideoComponentParam's LowerBound and UpperBound are required - removing
// either crashes 2025. Every other field 2026 omits was verified
// presence-optional: all IsTimeVarying, every ParameterControlType, the
// VideoTransition and AudioTransition defaults, and the TimeComponentParam
// booleans.
//
// reconstructFields lists the required fields per class; reconstructDefaults
// gives the value to insert. Order within each list only affects the order
// appended (cosmetic).
var reconstructFields = []struct {
	tag    string
	fields []string
}{
	{"VideoComponentParam", []string{"LowerBound", "UpperBound"}},
}

var reconstructFieldsByTag = func() map[string][]string {
	m := make(map[string][]string, len(reconstructFields))
	for _, c := range reconstructFields {
		m[c.tag] = c.fields
	}
	return m
}()

type fieldKey struct{ tag, field string }

// The values inserted for the required fields.
//
// These are NOT the parameter's real bounds. The true bounds vary per parameter
// and 2026 has already discarded them; no fixed constant could reproduce them.
// false/true are sentinels meaning "no explicit override": on load, 2025 reads
// them and repopulates each parameter's real default bound from its effect
// definition. This was verified by round-tripping two independent projects
// through 2025 (downgrade -> open -> re-save) and diffing the re-saved bounds
// against natively-saved 2025 output - they match exactly.
//
// The specific tokens are load-bearing: 2025 treats a numeric value as an
// explicit override and keeps it, silently corrupting the parameter's range;
// while false/true trigger repopulation.
var reconstructDefaults = map[fieldKey]string{
	{"VideoComponentParam", "LowerBound"}: "false",
	{"VideoComponentParam", "UpperBound"}: "true",
}

type classField struct{ classID, field string }

// Per-ClassID overrides for the value inserted, consulted before
// reconstructDefaults. Needed for the rare parameter class whose real bound is
// NOT one 2025 recomputes from the false/true sentinel - there the sentinel
// would round-trip to the wrong value, so we insert the literal instead.
//
// The only known case is the Lumetri Color selector param class, whose
// UpperBound is the "unbounded" marker (2^64-1). Inserting true there makes
// 2025 collapse it to 1; inserting the literal marker is preserved by 2025 and
// the project still opens (both verified by round-trip). The color value itself
// lives in the keyframes and is unaffected either way - this only restores the
// declared range. ClassID identifies the parameter's type and is stable across
// projects.
var reconstructClassOverrides = map[classField]string{
	{"0fde4e9f-f895-4ba3-b0fe-9a6feafda583", "UpperBound"}: "18446744073709551615",
}

var classIDRe = regexp.MustCompile(`\bClassID="([^"]+)"`)

// --------------------------------------------------------------------------
// Formatting-preserving mini-DOM, used only to rebuild the reconstruct
// classes. Each element keeps its exact opening tag and a content list (raw
// text + child elements), so re-serialising an untouched node is
// byte-identical to the input.
//
// This is NOT a general XML parser; it leans on the shapes Premiere's
// serialiser actually emits. In particular it assumes no raw '>' inside
// attribute values (legal XML, but Premiere escapes it) and no CDATA
// sections. A document that breaks those assumptions tokenises incorrectly
// and surfaces as an unbalanced/mismatched-XML error for that file - never as a
// silently corrupted output.
// --------------------------------------------------------------------------

type el struct {
	tag         string
	openTag     string
	selfClosing bool
	content     []any // string or *el
	closeTag    string
}

func (e *el) children() []*el {
	var out []*el
	for _, c := range e.content {
		if ce, ok := c.(*el); ok {
			out = append(out, ce)
		}
	}
	return out
}

func (e *el) render(b *strings.Builder) {
	if e.selfClosing {
		b.WriteString(e.openTag)
		return
	}
	b.WriteString(e.openTag)
	for _, c := range e.content {
		switch v := c.(type) {
		case string:
			b.WriteString(v)
		case *el:
			v.render(b)
		}
	}
	b.WriteString(e.closeTag)
}

var (
	tagRe     = regexp.MustCompile(`<[^>]+>`)
	tagNameRe = regexp.MustCompile(`^<([\w.\-]+)`)
)

func parseXML(s string) ([]any, error) {
	var roots []any
	var stack []*el
	appendItem := func(item any) {
		if len(stack) > 0 {
			top := stack[len(stack)-1]
			top.content = append(top.content, item)
		} else {
			roots = append(roots, item)
		}
	}
	pos := 0
	for _, loc := range tagRe.FindAllStringIndex(s, -1) {
		if loc[0] > pos {
			appendItem(s[pos:loc[0]])
		}
		tok := s[loc[0]:loc[1]]
		pos = loc[1]
		switch {
		case strings.HasPrefix(tok, "<?") || strings.HasPrefix(tok, "<!"):
			appendItem(tok)
		case strings.HasPrefix(tok, "</"):
			if len(stack) == 0 {
				return nil, fmt.Errorf("unbalanced XML near %q", tok)
			}
			top := stack[len(stack)-1]
			if name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(tok, "</"), ">")); name != top.tag {
				return nil, fmt.Errorf("mismatched XML: %q closes <%s>", tok, top.tag)
			}
			stack = stack[:len(stack)-1]
			top.closeTag = tok
		default:
			m := tagNameRe.FindStringSubmatch(tok)
			if m == nil {
				appendItem(tok)
				continue
			}
			e := &el{tag: m[1], openTag: tok, selfClosing: strings.HasSuffix(tok, "/>")}
			appendItem(e)
			if !e.selfClosing {
				stack = append(stack, e)
			}
		}
	}
	if pos < len(s) {
		appendItem(s[pos:])
	}
	if len(stack) > 0 {
		return nil, fmt.Errorf("unbalanced XML: <%s> never closed", stack[len(stack)-1].tag)
	}
	return roots, nil
}

func leaf(tag, value string) *el {
	return &el{
		tag:      tag,
		openTag:  "<" + tag + ">",
		content:  []any{value},
		closeTag: "</" + tag + ">",
	}
}

// rebuild re-inserts the default fields 2026 dropped, so a reconstruct class
// has every field 2025 requires present. Recurses into children first
// (depth-first).
//
// Field order is irrelevant, but we preserve it, and we append the handful of
// missing leaves before the closing tag.
//
// It returns the exact number of bytes the insertions add when rendered, so the
// caller can verify that rendering changed nothing else.
func rebuild(e *el, stats map[fieldKey]int) (insertedBytes int) {
	for _, child := range e.children() {
		insertedBytes += rebuild(child, stats)
	}
	if e.selfClosing {
		return insertedBytes
	}
	fields, ok := reconstructFieldsByTag[e.tag]
	if !ok {
		return insertedBytes
	}

	presentEls := map[string]*el{}
	for _, c := range e.children() {
		presentEls[c.tag] = c
	}

	classID := ""
	if m := classIDRe.FindStringSubmatch(e.openTag); m != nil {
		classID = m[1]
	}

	sep := "\n"
	bestLen := 0
	for _, c := range e.content {
		if s, isStr := c.(string); isStr && strings.TrimSpace(s) == "" &&
			strings.Contains(s, "\n") && len(s) > bestLen {
			sep, bestLen = s, len(s)
		}
	}

	var missing []*el
	for _, field := range fields {
		if _, has := presentEls[field]; has {
			continue
		}
		value, hasDefault := reconstructDefaults[fieldKey{e.tag, field}]
		if v, ok := reconstructClassOverrides[classField{classID, field}]; ok {
			value, hasDefault = v, true
		}
		if !hasDefault {
			continue
		}
		missing = append(missing, leaf(field, value))
		stats[fieldKey{e.tag, field}]++
	}

	if len(missing) == 0 {
		return insertedBytes // leave content untouched -> byte-identical to what Premiere wrote
	}

	content := e.content
	trailing := ""
	if len(content) > 0 {
		if s, isStr := content[len(content)-1].(string); isStr && strings.TrimSpace(s) == "" {
			trailing = s
			content = content[:len(content)-1]
		}
	}
	for _, l := range missing {
		content = append(content, sep, l)
		var lb strings.Builder
		l.render(&lb)
		insertedBytes += len(sep) + lb.Len()
	}
	if trailing != "" {
		content = append(content, trailing)
	}
	e.content = content
	return insertedBytes
}

// reconstructPositionalClasses rebuilds each reconstruct class so every field
// required is present. Operates on one class instance at a time (they
// don't self-nest) to avoid parsing the whole multi-hundred-MB document at
// once. A malformed instance is reported as an error for the whole file (the
// input is corrupt); the caller treats it per-file so a batch keeps going.
func reconstructPositionalClasses(xml string) (string, map[fieldKey]int, error) {
	stats := map[fieldKey]int{}
	for _, c := range reconstructFields {
		// The Python original uses <tag(?=[ >]); RE2 has no lookahead, but a
		// definition tag always carries attributes (Version= at minimum), so a
		// whitespace char after the name is equivalent.
		re := regexp.MustCompile(`(?s)<` + c.tag + `[ \t\r\n][^>]*\bVersion="\d+"[^>]*>.*?</` + c.tag + `>`)
		var parseErr error
		xml = re.ReplaceAllStringFunc(xml, func(m string) string {
			if parseErr != nil {
				return m
			}
			roots, err := parseXML(m)
			if err != nil {
				parseErr = err
				return m
			}
			var b strings.Builder
			b.Grow(len(m) + 256)
			regionInserted := 0
			for _, r := range roots {
				switch v := r.(type) {
				case string:
					b.WriteString(v)
				case *el:
					regionInserted += rebuild(v, stats)
					v.render(&b)
				}
			}
			out := b.String()
			// Render-fidelity guard: parsing then rendering this class instance
			// must reproduce the original bytes exactly, save for the leaves we
			// deliberately appended. With no insertion the output must be
			// byte-identical to the input; otherwise it must be longer by
			// precisely the bytes rebuild reports adding. Any other delta means
			// the parse/render round-trip dropped, moved, or mangled bytes, so we
			// abort the whole file.
			if regionInserted == 0 {
				if out != m {
					parseErr = fmt.Errorf("render fidelity: <%s> instance changed with no field inserted", c.tag)
					return m
				}
			} else if len(out) != len(m)+regionInserted {
				parseErr = fmt.Errorf("render fidelity: <%s> instance grew by %d bytes, expected %d",
					c.tag, len(out)-len(m), regionInserted)
				return m
			}
			return out
		})
		if parseErr != nil {
			return "", nil, parseErr
		}
	}
	return xml, stats, nil
}
