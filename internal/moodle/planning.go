package moodle

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

func stringList(value any) []string {
	if items, ok := value.([]string); ok {
		return items
	}
	result := []string{}
	if items, ok := value.([]any); ok {
		for _, item := range items {
			result = append(result, str(item))
		}
	}
	return result
}

func (s *Session) Decompose(ctx context.Context, id int64) (Object, error) {
	a, err := s.assignment(ctx, id)
	if err != nil {
		return nil, err
	}
	req, err := s.Requirements(ctx, id)
	if err != nil {
		return nil, err
	}
	sections := []string{}
	if content, err := s.Content(ctx, integer(a["courseid"])); err == nil {
		for _, section := range content {
			if name := str(section["name"]); name != "" {
				sections = append(sections, name)
			}
		}
	}
	tasks := Items{}
	add := func(title, description, effort, category string, dependencies []string) string {
		taskID := strconv.Itoa(len(tasks) + 1)
		tasks = append(tasks, Object{"id": taskID, "title": title, "description": description, "estimated_effort": effort, "dependencies": dependencies, "category": category})
		return taskID
	}
	add("Understand the assignment", fmt.Sprintf("Read and analyze the assignment description for '%s'. Identify all requirements, deliverables, and constraints.", a["name"]), "low", "research", []string{})
	researchID := ""
	if len(sections) > 0 {
		researchID = add("Review course materials", "Study relevant course sections: "+strings.Join(take(sections, 5), ", ")+". Take notes on key concepts related to the assignment.", "medium", "research", []string{"1"})
	}
	planID := add("Create a work plan", "Outline the structure and approach for completing the assignment. Define milestones and allocate time.", "low", "review", []string{"1"})
	dependencies := []string{planID}
	if researchID != "" {
		dependencies = append(dependencies, researchID)
	}
	lastID := planID
	for _, deliverable := range take(stringList(req["deliverables"]), 5) {
		title := "Complete: " + short(deliverable, 60)
		if utf8.RuneCountInString(deliverable) > 60 {
			title += "..."
		}
		lastID = add(title, "Work on deliverable: "+deliverable+". Follow the format and quality requirements specified in the assignment.", "high", "writing", append([]string{}, dependencies...))
		dependencies = []string{lastID}
	}
	reviewID := add("Review and refine", "Review all deliverables against the requirements and constraints. Check for completeness and quality.", "medium", "review", []string{lastID})
	add("Submit the assignment", fmt.Sprintf("Prepare final submission and upload to Moodle before the deadline. Assignment ID: %d.", id), "low", "submission", []string{reviewID})
	path := []string{}
	current := "1"
	seen := map[string]bool{}
	effortRank := map[string]int{"high": 0, "medium": 1, "low": 2}
	for current != "" && !seen[current] {
		seen[current] = true
		path = append(path, current)
		next := ""
		rank := 100
		for _, task := range tasks {
			for _, dependency := range stringList(task["dependencies"]) {
				if dependency == current {
					r := effortRank[str(task["estimated_effort"])]
					if r < rank {
						next = str(task["id"])
						rank = r
					}
				}
			}
		}
		current = next
	}
	high, medium := 0, 0
	for _, task := range tasks {
		switch task["estimated_effort"] {
		case "high":
			high++
		case "medium":
			medium++
		}
	}
	totalEffort := "low"
	if high >= 3 {
		totalEffort = "high"
	} else if high >= 1 || medium >= 2 {
		totalEffort = "medium"
	}
	return Object{"assignment_id": id, "assignment_name": a["name"], "course_name": a["coursename"], "total_subtasks": len(tasks),
		"subtasks": tasks, "critical_path": path, "estimated_total_effort": totalEffort}, nil
}

func (s *Session) Plan(ctx context.Context, id int64) (Object, error) {
	a, err := s.assignment(ctx, id)
	if err != nil {
		return nil, err
	}
	decomposition, err := s.Decompose(ctx, id)
	if err != nil {
		return nil, err
	}
	due := integer(a["duedate"])
	var remaining any
	if due > 0 {
		remaining = days(due - s.now.Unix())
	}
	materials := Items{}
	if result, err := s.Materials(ctx, id); err == nil {
		materials = take(list(result["course_content_items"]), 5)
	}
	durations := map[string]string{"high": "5-10 hours", "medium": "2-4 hours", "low": "1-2 hours"}
	if remaining != nil && integer(remaining) > 0 {
		switch n := integer(remaining); {
		case n <= 1:
			durations = map[string]string{"high": "1-2 hours", "medium": "30-60 minutes", "low": "15-30 minutes"}
		case n <= 3:
			durations = map[string]string{"high": "3-5 hours", "medium": "1-3 hours", "low": "30-60 minutes"}
		case n <= 7:
			durations = map[string]string{"high": "5-10 hours", "medium": "2-4 hours", "low": "1-2 hours"}
		default:
			durations = map[string]string{"high": "1-3 days", "medium": "4-8 hours", "low": "1-2 hours"}
		}
	}
	tasks := list(decomposition["subtasks"])
	steps := Items{}
	for i, task := range tasks {
		count := 0
		switch task["category"] {
		case "research":
			count = 3
		case "writing":
			count = 2
		}
		resources := []string{}
		for _, m := range take(materials, count) {
			resources = append(resources, str(m["title"]))
		}
		steps = append(steps, Object{"step_number": i + 1, "title": task["title"], "description": task["description"], "subtask_ids": []string{str(task["id"])},
			"estimated_duration": durations[str(task["estimated_effort"])], "resources": resources})
	}
	milestones := []string{}
	path := stringList(decomposition["critical_path"])
	for i, id := range path {
		for _, task := range tasks {
			if task["id"] == id {
				prefix := "→ Checkpoint: "
				if i == 0 {
					prefix = "✓ Start: "
				} else if i == len(path)-1 {
					prefix = "🎯 Final: "
				}
				milestones = append(milestones, prefix+str(task["title"]))
				break
			}
		}
	}
	risks := []string{}
	if remaining != nil {
		n := integer(remaining)
		switch {
		case n < 0:
			risks = append(risks, fmt.Sprintf("Assignment is overdue by %d day(s)", -n))
		case n <= 1:
			risks = append(risks, "Very tight deadline — less than 1 day remaining")
		case n <= 3:
			risks = append(risks, "Short deadline — prioritize critical path tasks")
		}
	}
	if utf8.RuneCountInString(str(a["intro"])) < 50 {
		risks = append(risks, "Assignment description is minimal — clarify requirements with instructor")
	}
	if decomposition["estimated_total_effort"] == "high" {
		risks = append(risks, "High overall effort required — start early and allocate sufficient time")
	}
	if len(materials) == 0 {
		risks = append(risks, "Limited course materials found — may need external resources")
	}
	return Object{"assignment_id": id, "assignment_name": a["name"], "course_name": a["coursename"], "duedate": due, "duedate_formatted": optionalDate(due),
		"days_remaining": remaining, "total_steps": len(steps), "steps": steps, "milestones": milestones, "risk_factors": risks}, nil
}
