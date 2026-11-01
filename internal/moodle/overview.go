package moodle

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

func gradeSummaries(grades Items) Items {
	result := Items{}
	for _, g := range grades {
		if truth(g["grade"]) {
			result = append(result, Object{"itemname": g["course_name"], "grade": g["grade"], "percentage": nil, "feedback": nil})
		}
	}
	return result
}

func (s *Session) Dashboard(ctx context.Context) (Object, error) {
	courses, _ := s.Courses(ctx)
	deadlines, _ := s.Deadlines(ctx)
	grades, _ := s.Grades(ctx, 0)
	result := Items{}
	for _, c := range courses {
		item := fields(c, "id", "fullname", "shortname", "progress")
		item["grade"] = nil
		for _, g := range grades {
			if integer(g["courseid"]) == integer(c["id"]) && truth(g["grade"]) {
				item["grade"] = g["grade"]
				break
			}
		}
		result = append(result, item)
	}
	return Object{"courses": result, "upcoming_deadlines": nonNil(deadlines), "recent_grades": gradeSummaries(grades)}, nil
}
func nonNil(items Items) Items {
	if items == nil {
		return Items{}
	}
	return items
}

func monday(t time.Time) time.Time {
	t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return t.AddDate(0, 0, -(int(t.Weekday())+6)%7)
}

func (s *Session) StudyLoad(ctx context.Context) (Object, error) {
	assignments, err := s.Assignments(ctx, nil)
	if err != nil {
		return nil, err
	}
	groups := map[string]Items{}
	for _, a := range assignments {
		if due := integer(a["duedate"]); due > 0 {
			key := monday(time.Unix(due, 0).UTC()).Format(time.DateOnly)
			groups[key] = append(groups[key], a)
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	weeks := Items{}
	total, heaviestCount := 0, 0
	var heaviest any
	for _, key := range keys {
		items := groups[key]
		count := len(items)
		total += count
		if count > heaviestCount {
			heaviestCount = count
			heaviest = key
		}
		set := map[string]bool{}
		for _, a := range items {
			set[str(a["coursename"])] = true
		}
		courses := []string{}
		for name := range set {
			courses = append(courses, name)
		}
		sort.Strings(courses)
		level := "heavy"
		if count <= 2 {
			level = "light"
		} else if count <= 4 {
			level = "medium"
		}
		start, _ := time.Parse(time.DateOnly, key)
		weeks = append(weeks, Object{"week_start": key, "week_end": start.AddDate(0, 0, 6).Format(time.DateOnly),
			"assignment_count": count, "courses": courses, "load_level": level})
	}
	average := 0.0
	if len(weeks) > 0 {
		average = round(float64(total)/float64(len(weeks)), 1)
	}
	return Object{"weeks": weeks, "total_assignments": total, "heaviest_week": heaviest, "average_per_week": average}, nil
}

func (s *Session) Daily(ctx context.Context) (Object, error) {
	overdue, _ := s.Overdue(ctx)
	deadlines, _ := s.Deadlines(ctx)
	grades, _ := s.Grades(ctx, 0)
	events, _ := s.Events(ctx)
	tasks, _ := s.Tasks(ctx)
	date := s.now.Format(time.DateOnly)
	today := Items{}
	for _, d := range deadlines {
		if time.Unix(integer(d["duedate"]), 0).UTC().Format(time.DateOnly) == date {
			today = append(today, d)
		}
	}
	counts := map[string]int{}
	order := []string{}
	for _, t := range tasks {
		key := str(t["urgency"])
		if counts[key] == 0 {
			order = append(order, key)
		}
		counts[key]++
	}
	summary := Items{}
	for _, key := range order {
		summary = append(summary, Object{"urgency": key, "count": counts[key]})
	}
	return Object{"date": date, "overdue_count": len(overdue), "today_deadlines": today, "recent_grades": gradeSummaries(grades),
		"upcoming_events": nonNil(events), "actionable_tasks_summary": summary}, nil
}

func (s *Session) Weekly(ctx context.Context) (Object, error) {
	start := monday(s.now)
	end := start.AddDate(0, 0, 7)
	assignments, _ := s.Assignments(ctx, nil)
	thisWeek := Items{}
	for _, a := range assignments {
		due := integer(a["duedate"])
		if due >= start.Unix() && due < end.Unix() {
			thisWeek = append(thisWeek, a)
		}
	}
	statuses, errs := s.statuses(ctx, thisWeek)
	submitted := 0
	for i, status := range statuses {
		if errs[i] == nil && truth(status["submitted"]) {
			submitted++
		}
	}
	grades, _ := s.Grades(ctx, 0)
	graded := 0
	for _, g := range grades {
		if truth(g["grade"]) {
			graded++
		}
	}
	deadlines, _ := s.Deadlines(ctx)
	upcoming := Items{}
	for _, d := range deadlines {
		due := integer(d["duedate"])
		if due >= start.Unix() && due < end.Unix()+7*86400 {
			upcoming = append(upcoming, d)
		}
	}
	overdue, _ := s.Overdue(ctx)
	progress, _ := s.Progress(ctx, 0)
	return Object{"week_start": start.Format(time.DateOnly), "week_end": end.Format(time.DateOnly), "submitted_count": submitted,
		"graded_count": graded, "upcoming_deadlines": upcoming, "overdue_count": len(overdue), "progress_summary": nonNil(progress)}, nil
}

var questionKeywords = map[string][]string{
	"deadlines":     {"deadline", "due", "due date", "overdue", "late", "submit", "дедлайн", "завдання", "срок", "просроч"},
	"grades":        {"grade", "score", "mark", "result", "gpa", "оцінк", "бал", "оценк"},
	"courses":       {"course", "class", "enroll", "progress", "курс", "предмет", "прогресс"},
	"assignments":   {"assignment", "homework", "task", "project", "робот", "практ", "задан", "лаборатор"},
	"announcements": {"announcement", "news", "notice", "forum", "post", "оголошення", "новин", "объявлен", "новост"},
	"schedule":      {"schedule", "calendar", "event", "upcoming", "timetable", "розклад", "расписан", "событ"},
	"search":        {"search", "find", "look for", "material", "resource", "знайти", "пошук", "найди", "найти", "материал"},
}

func (s *Session) Ask(ctx context.Context, question string) (Object, error) {
	q := strings.ToLower(question)
	sources := map[string]bool{}
	for name, words := range questionKeywords {
		if contains(q, words) {
			sources[name] = true
		}
	}
	if len(sources) == 0 {
		sources["deadlines"] = true
		sources["courses"] = true
	}
	used := []string{}
	items := Items{}
	answers := []string{}
	add := func(kind string, rows Items) {
		for _, row := range rows {
			items = append(items, Object{"type": kind, "data": row})
		}
	}
	report := func(source string, err error) bool {
		if err != nil {
			answers = append(answers, fmt.Sprintf("Could not fetch %s: %v", source, err))
			return true
		}
		return false
	}
	if sources["deadlines"] {
		rows, err := s.Deadlines(ctx)
		if !report("deadlines", err) {
			used = append(used, "upcoming_deadlines")
			add("deadline", take(rows, 5))
			if len(rows) > 0 {
				answers = append(answers, fmt.Sprintf("You have %d upcoming deadline(s).", len(rows)))
			} else {
				answers = append(answers, "You have no upcoming deadlines.")
			}
		}
	}
	if sources["grades"] {
		rows, err := s.Grades(ctx, 0)
		if !report("grades", err) {
			used = append(used, "grades_overview")
			graded := Items{}
			for _, g := range rows {
				if truth(g["grade"]) {
					graded = append(graded, g)
				}
			}
			add("grade", graded)
			if len(graded) > 0 {
				answers = append(answers, fmt.Sprintf("Grades available for %d course(s).", len(graded)))
			}
		}
	}
	if sources["courses"] {
		rows, err := s.Courses(ctx)
		if !report("courses", err) {
			used = append(used, "my_courses")
			for _, c := range rows {
				items = append(items, Object{"type": "course", "data": Object{"id": c["id"], "name": c["fullname"], "progress": c["progress"]}})
			}
			answers = append(answers, fmt.Sprintf("You are enrolled in %d course(s).", len(rows)))
		}
	}
	if sources["assignments"] {
		rows, err := s.Tasks(ctx)
		if !report("assignments", err) {
			used = append(used, "actionable_tasks")
			urgent := Items{}
			for _, t := range rows {
				if t["urgency"] == "overdue" || t["urgency"] == "due_soon" {
					urgent = append(urgent, t)
				}
			}
			add("task", take(urgent, 5))
			if len(urgent) > 0 {
				answers = append(answers, fmt.Sprintf("You have %d urgent task(s).", len(urgent)))
			}
		}
	}
	if sources["announcements"] {
		rows, err := s.Announcements(ctx, 0)
		if !report("announcements", err) {
			used = append(used, "course_announcements")
			add("announcement", take(rows, 5))
			if len(rows) > 0 {
				answers = append(answers, fmt.Sprintf("There are %d recent announcement(s).", len(rows)))
			}
		}
	}
	if sources["schedule"] {
		rows, err := s.Events(ctx)
		if !report("events", err) {
			used = append(used, "upcoming_events")
			add("event", take(rows, 5))
			if len(rows) > 0 {
				answers = append(answers, fmt.Sprintf("You have %d upcoming event(s).", len(rows)))
			}
		}
	}
	if sources["search"] {
		query := q
		for _, word := range []string{"search", "find", "look for", "знайти", "пошук", "найди", "найти"} {
			query = strings.ReplaceAll(query, word, "")
		}
		query = strings.TrimSpace(query)
		if query != "" {
			rows, err := s.Search(ctx, query)
			if !report("search", err) {
				used = append(used, "search")
				add("search_result", take(rows, 5))
				if len(rows) > 0 {
					answers = append(answers, fmt.Sprintf("Found %d result(s) for '%s'.", len(rows), query))
				} else {
					answers = append(answers, fmt.Sprintf("No results found for '%s'.", query))
				}
			}
		}
	}
	answer := strings.Join(answers, " ")
	if answer == "" {
		answer = "No relevant information found."
	}
	return Object{"question": question, "answer": answer, "data_sources": used, "relevant_items": items}, nil
}
