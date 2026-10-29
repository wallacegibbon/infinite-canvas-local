package prompt

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// H3 prompt hygiene.
//
// The model reliably produces the field structure but is inconsistent about two
// things the renderer needs: every on-screen subject must carry a speaker id like
// "(S1)", and off-screen voices must say so explicitly. Both are repaired here
// rather than retried, because a retry costs a full generation.

var (
	reDialogTag    = regexp.MustCompile(`(?i)<d(?:\s[^>]*)?>[\s\S]*?</d>`)
	reSpeakerIDs   = regexp.MustCompile(`\((S\d+(?:\s*[,，]\s*S\d+)*)\)`)
	reOffscreen    = regexp.MustCompile(`(?i)画(?:面)?[^，。！？!?；;\n]{0,10}外|画外音|旁白|\boff-screen\b|\bvoice-?over\b`)
	reSubject      = regexp.MustCompile(`(?i)(老头儿|老头|老太太|老人|男人|女人|男孩|女孩|孩子|少年|少女|旁白|\bold man\b|\bold woman\b|\bman\b|\bwoman\b|\bboy\b|\bgirl\b|\bnarrator\b)`)
	reSubjectWithI = regexp.MustCompile(`(?i)(老头儿|老头|老太太|老人|男人|女人|男孩|女孩|孩子|少年|少女|旁白|\bold man\b|\bold woman\b|\bman\b|\bwoman\b|\bboy\b|\bgirl\b|\bnarrator\b)\s*\((S\d+)`)
	reSegHasID     = regexp.MustCompile(`\(S\d+`)
	reDescription  = regexp.MustCompile(`(?i)(integrated_multimodal_description|detailed_description)\s*:`)
	reDescEnd      = regexp.MustCompile(`(?i)\n\s*(?:overall_soundscape|non_diegetic_music)\s*:`)
	reFullWidthIDs = regexp.MustCompile(`（\s*(S\d+(?:\s*[,，]\s*S\d+)*)\s*）`)
	reIDSeparator  = regexp.MustCompile(`\s*[,，]\s*`)
	reOffscreenID  = regexp.MustCompile(`([^，。！？!?；;\n]{0,60}?)(\((?:S\d+)(?:\s*[,，]\s*S\d+)*\))([^，。！？!?；;\n]{0,30})`)
	reHasNote      = regexp.MustCompile(`(?i)不在画面|不对应任何参考图|not in any reference picture`)
	reTrailingStop = regexp.MustCompile(`^[\s，。！？!?；;、：:）)]$`)
	reHanText      = regexp.MustCompile(`[\x{4e00}-\x{9fa5}]`)
	reOffscreenLit = regexp.MustCompile(`(?i)\boff-screen\b`)
)

const (
	offscreenNoteZh = "（不在画面中，不对应任何参考图）"
	offscreenNoteEn = " (off-screen voice, not in any reference picture)"
)

// SpeakerIssue records a dialog tag that had no attributable subject.
type SpeakerIssue struct {
	Reason string `json:"reason"`
	Text   string `json:"text"`
}

// SpeakerResult is the repaired prompt plus a repair log.
type SpeakerResult struct {
	Prompt string
	Fixed  int
	Issues []SpeakerIssue
}

// EnsureSpeakerTags repairs speaker ids and off-screen markers, leaving every other
// byte of the prompt untouched.
func EnsureSpeakerTags(output string) SpeakerResult {
	start, end := descriptionBounds(output)
	description := normalizeSpeakerIDs(output[start:end])

	missing := addMissingSpeakerIDs(description)
	offscreen := addOffScreenMarkers(missing.description)

	issues := missing.issues
	if issues == nil {
		issues = []SpeakerIssue{}
	}
	return SpeakerResult{
		Prompt: output[:start] + offscreen.description + output[end:],
		Fixed:  missing.fixed + offscreen.fixed,
		Issues: issues,
	}
}

// descriptionBounds locates the description field inside an H3 prompt.
func descriptionBounds(prompt string) (int, int) {
	match := reDescription.FindStringIndex(prompt)
	if match == nil {
		return 0, len(prompt)
	}
	start := match[1]
	if next := reDescEnd.FindStringIndex(prompt[start:]); next != nil {
		return start, start + next[0]
	}
	return start, len(prompt)
}

// normalizeSpeakerIDs rewrites full-width parentheses around speaker ids.
func normalizeSpeakerIDs(text string) string {
	return reFullWidthIDs.ReplaceAllStringFunc(text, func(match string) string {
		groups := reFullWidthIDs.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		return "(" + reIDSeparator.ReplaceAllString(groups[1], ",") + ")"
	})
}

// subjectKey folds the many spellings of a speaker into one identity.
func subjectKey(value string) string {
	text := strings.ToLower(value)
	switch {
	case strings.Contains(text, "老头") || strings.Contains(text, "old man"):
		return "old man"
	case strings.Contains(text, "老太太") || strings.Contains(text, "old woman"):
		return "old woman"
	case strings.Contains(text, "男人") || text == "man":
		return "man"
	case strings.Contains(text, "女人") || text == "woman":
		return "woman"
	case strings.Contains(text, "男孩") || text == "boy":
		return "boy"
	case strings.Contains(text, "女孩") || text == "girl":
		return "girl"
	case strings.Contains(text, "旁白") || text == "narrator":
		return "narrator"
	}
	return text
}

func nextSpeakerID(used map[string]bool) string {
	for index := 1; index <= 9; index++ {
		id := "S" + strconv.Itoa(index)
		if !used[id] {
			return id
		}
	}
	return "S1"
}

type missingResult struct {
	description string
	fixed       int
	issues      []SpeakerIssue
}

// addMissingSpeakerIDs inserts an id right after the last subject that precedes each
// dialog tag.
//
// All offsets are byte offsets: Go regexp reports byte indices, so mixing in rune
// counts would corrupt multi-byte CJK text.
func addMissingSpeakerIDs(description string) missingResult {
	result := missingResult{issues: []SpeakerIssue{}}

	used := map[string]bool{}
	for _, match := range reSpeakerIDs.FindAllStringSubmatch(description, -1) {
		first := strings.Split(strings.TrimSpace(match[1]), ",")[0]
		used[strings.TrimSpace(first)] = true
	}

	subjectIDs := map[string]string{}
	for _, match := range reSubjectWithI.FindAllStringSubmatch(description, -1) {
		subjectIDs[subjectKey(match[1])] = match[2]
	}

	type insertion struct {
		at   int
		text string
	}
	var insertions []insertion

	// segmentStart is the end of the previous dialog tag: the start of the text that
	// introduces the current one.
	segmentStart := 0
	for _, tag := range reDialogTag.FindAllStringIndex(description, -1) {
		segment := description[segmentStart:tag[0]]
		segmentEnd := tag[1]

		if reSegHasID.MatchString(segment) {
			segmentStart = segmentEnd
			continue
		}

		subjects := reSubject.FindAllStringIndex(segment, -1)
		if len(subjects) == 0 {
			result.issues = append(result.issues, SpeakerIssue{
				Reason: "missing-speaker",
				Text:   truncateRunes(description[tag[0]:tag[1]], 80),
			})
			segmentStart = segmentEnd
			continue
		}

		last := subjects[len(subjects)-1]
		key := subjectKey(segment[last[0]:last[1]])
		id, known := subjectIDs[key]
		if !known {
			id = nextSpeakerID(used)
			subjectIDs[key] = id
			used[id] = true
		}

		at := segmentStart + last[1]
		trailing := ""
		if at < len(description) {
			if next, _ := utf8.DecodeRuneInString(description[at:]); !reTrailingStop.MatchString(string(next)) {
				trailing = " "
			}
		}
		insertions = append(insertions, insertion{at: at, text: " (" + id + ")" + trailing})
		result.fixed++
		segmentStart = segmentEnd
	}

	output := description
	// Apply back to front so the earlier byte offsets stay valid.
	for i := len(insertions) - 1; i >= 0; i-- {
		item := insertions[i]
		if item.at < 0 || item.at > len(output) {
			continue
		}
		output = output[:item.at] + item.text + output[item.at:]
	}
	result.description = output
	return result
}

type offscreenResult struct {
	description string
	fixed       int
}

// addOffScreenMarkers tells the renderer which voices have no reference picture.
func addOffScreenMarkers(description string) offscreenResult {
	hasNote := reHasNote.MatchString(description)
	fixed := 0

	output := reOffscreenID.ReplaceAllStringFunc(description, func(match string) string {
		groups := reOffscreenID.FindStringSubmatch(match)
		if len(groups) < 4 {
			return match
		}
		before, id, after := groups[1], groups[2], groups[3]
		if !reOffscreen.MatchString(before) {
			return match
		}
		if strings.Contains(strings.ToLower(before), "off-screen") ||
			strings.Contains(strings.ToLower(after), "off-screen") {
			return match
		}
		note := ""
		if !hasNote {
			if reHanText.MatchString(before + after) {
				note = offscreenNoteZh
			} else {
				note = offscreenNoteEn
			}
		}
		hasNote = true
		fixed++
		return before + id + " off-screen" + note + after
	})

	if !hasNote {
		output = reOffscreenLit.ReplaceAllStringFunc(output, func(match string) string {
			fixed++
			if reHanText.MatchString(output) {
				return match + offscreenNoteZh
			}
			return match + offscreenNoteEn
		})
	}
	return offscreenResult{description: output, fixed: fixed}
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
