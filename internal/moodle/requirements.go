package moodle

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var tags = regexp.MustCompile(`<[^>]+>`)
var whitespace = regexp.MustCompile(`\s+`)
var sentenceBreak = regexp.MustCompile(`[.!?]\s+`)
var words = regexp.MustCompile(`[a-zA-Zа-яА-ЯіІєЄїЇґҐ]{3,}`)

func cleanHTML(value string) string {
	return strings.TrimSpace(whitespace.ReplaceAllString(tags.ReplaceAllString(value, " "), " "))
}
func sentences(text string) []string {
	out := []string{}
	start := 0
	for _, match := range sentenceBreak.FindAllStringIndex(text, -1) {
		out = append(out, text[start:match[0]+1])
		start = match[1]
	}
	if start < len(text) {
		out = append(out, text[start:])
	}
	return out
}
func requirements(intro string) Items {
	result := Items{}
	categories := []struct {
		name  string
		words []string
	}{
		{"format", []string{"format", "length", "words", "pages", "font", "size", "spacing", "margin", "apa", "mla", "chicago", "harvard", "docx", "pdf", "pptx", "zip", "формат", "обсяг", "сторінк", "шрифт"}},
		{"evaluation", []string{"grade", "score", "rubric", "criteria", "evaluat", "assess", "mark", "weight", "percent", "point", "bonus", "penalty", "оцінк", "бал", "критер", "штра"}},
		{"deadline", []string{"deadline", "due", "submit by", "no later than", "before", "дедлайн", "до", "не пізніш"}},
		{"deliverable", []string{"submit", "write", "create", "develop", "design", "implement", "build", "prepare", "present", "report", "essay", "paper", "project", "portfolio", "зда", "напиш", "створ", "розроб", "підготув", "звіт"}},
		{"constraint", []string{"must", "should", "required", "mandatory", "must not", "do not", "cannot", "limit", "maximum", "minimum", "at least", "no more than", "обов'язк", "необхід", "заборон", "ліміт", "максимум", "мінімум"}},
	}
	for _, sentence := range sentences(cleanHTML(intro)) {
		sentence = strings.TrimSpace(sentence)
		if utf8.RuneCountInString(sentence) < 5 {
			continue
		}
		lower := strings.ToLower(sentence)
		category := "deliverable"
		for _, c := range categories {
			if contains(lower, c.words) {
				category = c.name
				break
			}
		}
		priority := "optional"
		if contains(lower, []string{"must", "required", "mandatory", "обов'язк"}) {
			priority = "must"
		} else if contains(lower, []string{"should", "recommend", "бажано"}) {
			priority = "should"
		}
		result = append(result, Object{"requirement": sentence, "category": category, "priority": priority, "source": "intro"})
	}
	return result
}

func (s *Session) Requirements(ctx context.Context, id int64) (Object, error) {
	a, err := s.assignment(ctx, id)
	if err != nil {
		return nil, err
	}
	intro := str(a["intro"])
	var clean any
	if intro != "" {
		clean = cleanHTML(intro)
	}
	reqs := requirements(intro)
	deliverables, constraints, evaluation, deadlines := []string{}, []string{}, []string{}, []string{}
	for _, r := range reqs {
		text := str(r["requirement"])
		switch r["category"] {
		case "deliverable":
			deliverables = append(deliverables, text)
		case "constraint":
			constraints = append(constraints, text)
		case "evaluation":
			evaluation = append(evaluation, text)
		case "deadline":
			deadlines = append(deadlines, text)
		}
	}
	if due := integer(a["duedate"]); due > 0 {
		deadlines = append(deadlines, "Official due date: "+iso(due))
	}
	if len(deliverables) == 0 && truth(clean) {
		deliverables = append(deliverables, "Complete the assignment as described")
	}
	parts := []string{}
	for _, item := range []struct {
		name  string
		count int
	}{{"Deliverables", len(deliverables)}, {"Constraints", len(constraints)}, {"Evaluation criteria", len(evaluation)}, {"Deadlines", len(deadlines)}} {
		if item.count > 0 {
			parts = append(parts, fmt.Sprintf("%s: %d", item.name, item.count))
		}
	}
	summary := fmt.Sprintf("Assignment '%s' in %s", a["name"], a["coursename"])
	if len(parts) > 0 {
		summary += " — " + strings.Join(parts, ", ")
	}
	return Object{"assignment_id": id, "assignment_name": a["name"], "course_name": a["coursename"], "intro": clean, "requirements": reqs,
		"deliverables": deliverables, "constraints": constraints, "evaluation_criteria": evaluation, "deadlines": deadlines, "summary": summary}, nil
}

func (s *Session) Analyze(ctx context.Context, id int64) (Object, error) {
	a, err := s.assignment(ctx, id)
	if err != nil {
		return nil, err
	}
	status, _ := s.Status(ctx, id)
	submitted, graded := truth(status["submitted"]), truth(status["graded"])
	due := integer(a["duedate"])
	var remaining any
	if due > 0 {
		remaining = days(due - s.now.Unix())
	}
	intro := str(a["intro"])
	reqs := requirements(intro)
	courseID := integer(a["courseid"])
	var progress, grade any
	if items, err := s.Progress(ctx, courseID); err == nil {
		for _, p := range items {
			if integer(p["courseid"]) == courseID {
				progress = p["progress_percentage"]
				break
			}
		}
	}
	if items, err := s.Grades(ctx, courseID); err == nil {
		for _, g := range items {
			if truth(g["grade"]) && strings.Contains(strings.ToLower(str(g["itemname"])), strings.ToLower(str(a["name"]))) {
				grade = g["grade"]
				break
			}
		}
	}
	materials := 0
	if sections, err := s.Content(ctx, courseID); err == nil {
		for _, section := range sections {
			for _, m := range list(section["modules"]) {
				if len(list(m["contents"])) > 0 || truth(m["url"]) {
					materials++
				}
			}
		}
	}
	return Object{"assignment_id": id, "assignment_name": a["name"], "course_name": a["coursename"], "status": status["submission_status"],
		"submitted": submitted, "graded": graded, "duedate": due, "duedate_formatted": optionalDate(due), "days_remaining": remaining,
		"is_overdue": due > 0 && due < s.now.Unix() && !submitted, "intro_length": utf8.RuneCountInString(intro), "has_intro": strings.TrimSpace(intro) != "",
		"requirements_count": len(reqs), "requirements": take(reqs, 10), "relevant_materials_count": materials, "course_progress": progress, "course_grade": grade}, nil
}

func keywords(name string) []string {
	stop := strings.Fields("the a an and or but in on at to for of with by from is are was were be been being have has had do does did will would could should may might shall can this that these those it its not no lab hw homework task assignment project work exercise lesson лаб практ завдання робота")
	result := []string{}
	for _, word := range words.FindAllString(name, -1) {
		word = strings.ToLower(word)
		found := false
		for _, skip := range stop {
			if word == skip {
				found = true
				break
			}
		}
		if !found {
			result = append(result, word)
		}
	}
	return result
}
func relevance(text string, keys []string) float64 {
	if text == "" || len(keys) == 0 {
		return 0
	}
	matches := 0
	text = strings.ToLower(text)
	for _, key := range keys {
		if strings.Contains(text, key) {
			matches++
		}
	}
	return float64(matches) / float64(len(keys))
}
func (s *Session) Materials(ctx context.Context, id int64) (Object, error) {
	a, err := s.assignment(ctx, id)
	if err != nil {
		return nil, err
	}
	keys := keywords(str(a["name"]))
	all := append([]string{}, keys...)
	if str(a["intro"]) != "" {
		for _, w := range take(words.FindAllString(tags.ReplaceAllString(str(a["intro"]), " "), -1), 20) {
			all = append(all, strings.ToLower(w))
		}
		seen := map[string]bool{}
		unique := []string{}
		for _, key := range all {
			if !seen[key] {
				seen[key] = true
				unique = append(unique, key)
			}
		}
		all = unique
	}
	content := Items{}
	if sections, err := s.Content(ctx, integer(a["courseid"])); err == nil {
		for _, section := range sections {
			for _, m := range list(section["modules"]) {
				score := relevance(str(section["name"])+" "+str(m["name"]), all)
				for _, file := range list(m["contents"]) {
					score = max(score, relevance(str(file["filename"]), all))
				}
				if score > 0 || len(all) == 0 {
					title := str(m["name"])
					if title == "" {
						title = str(section["name"])
					}
					content = append(content, Object{"title": title, "type": valueOr(m, "modname", "unknown"), "course_name": a["coursename"],
						"relevance_score": round(score, 2), "url": m["url"], "description": nil, "section": str(section["name"])})
				}
			}
		}
	}
	content = take(sorted(content, "relevance_score", true), 20)
	search := Items{}
	if len(keys) > 0 {
		if results, err := s.Search(ctx, strings.Join(take(keys, 5), " ")); err == nil {
			for _, r := range take(results, 10) {
				courseName := first(r["course_name"], a["coursename"])
				search = append(search, Object{"title": str(r["title"]), "type": "search_result", "course_name": courseName,
					"relevance_score": round(relevance(str(r["title"])+" "+str(r["content"]), all), 2), "url": r["url"], "description": r["content"], "section": nil})
			}
		}
	}
	return Object{"assignment_id": id, "assignment_name": a["name"], "course_name": a["coursename"],
		"course_content_items": content, "search_results": search, "total_count": len(content) + len(search)}, nil
}
