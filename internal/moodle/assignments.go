package moodle

import (
	"context"
	"fmt"
	"math"
	"sort"
)

func (s *Session) Assignments(ctx context.Context, courseIDs []int64) (Items, error) {
	data, err := s.call(ctx, "mod_assign_get_assignments", arrayParams("courseids", courseIDs))
	if err != nil {
		return nil, err
	}
	result := Items{}
	for _, course := range list(obj(data)["courses"]) {
		for _, a := range list(course["assignments"]) {
			result = append(result, Object{"id": valueOr(a, "id", 0), "name": valueOr(a, "name", ""), "duedate": valueOr(a, "duedate", 0),
				"cutoffdate": valueOr(a, "cutoffdate", 0), "intro": a["intro"], "courseid": valueOr(course, "id", 0), "coursename": valueOr(course, "fullname", "")})
		}
	}
	return result, nil
}

func (s *Session) Status(ctx context.Context, id int64) (Object, error) {
	data, err := s.call(ctx, "mod_assign_get_submission_status", idParams("assignid", id))
	if err != nil {
		return nil, err
	}
	o := obj(data)
	attempt := obj(o["lastattempt"])
	submission := obj(attempt["submission"])
	assignment := obj(o["assignment"])
	if o["assignment"] == nil {
		assignment = obj(o["config"])
	}
	return Object{"submission_status": submission["status"], "grading_status": attempt["gradingstatus"],
		"duedate": valueOr(assignment, "duedate", 0), "cutoffdate": valueOr(assignment, "cutoffdate", 0),
		"extensionduedate": valueOr(attempt, "extensionduedate", 0), "late": valueOr(attempt, "late", false),
		"submitted": str(submission["status"]) == "submitted", "graded": valueOr(attempt, "graded", false)}, nil
}

func (s *Session) statuses(ctx context.Context, assignments Items) (Items, []error) {
	return parallel(ctx, assignments, func(a Object) (Object, error) { return s.Status(ctx, integer(a["id"])) })
}

func (s *Session) Deadlines(ctx context.Context) (Items, error) {
	assignments, err := s.Assignments(ctx, nil)
	if err != nil {
		return nil, err
	}
	future := Items{}
	for _, a := range assignments {
		if integer(a["duedate"]) > s.now.Unix() {
			future = append(future, a)
		}
	}
	statuses, errs := s.statuses(ctx, future)
	result := Items{}
	for i, a := range future {
		submitted := false
		var status any
		if errs[i] == nil {
			submitted = truth(statuses[i]["submitted"])
			status = statuses[i]["submission_status"]
		}
		result = append(result, Object{"assignment_id": a["id"], "assignment_name": a["name"], "course_name": a["coursename"],
			"duedate": a["duedate"], "duedate_formatted": iso(integer(a["duedate"])), "submitted": submitted, "submission_status": status})
	}
	return sorted(result, "duedate", false), nil
}

func (s *Session) Overdue(ctx context.Context) (Items, error) {
	assignments, err := s.Assignments(ctx, nil)
	if err != nil {
		return nil, err
	}
	past := Items{}
	for _, a := range assignments {
		if due := integer(a["duedate"]); due > 0 && due < s.now.Unix() {
			past = append(past, a)
		}
	}
	statuses, errs := s.statuses(ctx, past)
	result := Items{}
	for i, a := range past {
		if errs[i] != nil || truth(statuses[i]["submitted"]) {
			continue
		}
		result = append(result, Object{"id": a["id"], "name": a["name"], "course": a["coursename"], "duedate": a["duedate"],
			"duedate_formatted": iso(integer(a["duedate"])), "days_overdue": days(s.now.Unix() - integer(a["duedate"])), "submission_status": statuses[i]["submission_status"]})
	}
	return sorted(result, "days_overdue", true), nil
}

func (s *Session) Tasks(ctx context.Context) (Items, error) {
	assignments, err := s.Assignments(ctx, nil)
	if err != nil {
		return nil, err
	}
	statuses, errs := s.statuses(ctx, assignments)
	result := Items{}
	for i, a := range assignments {
		if errs[i] != nil {
			continue
		}
		status := statuses[i]
		submitted, graded := truth(status["submitted"]), truth(status["graded"])
		due := integer(a["duedate"])
		urgency := ""
		switch {
		case !submitted && due > 0 && due < s.now.Unix():
			urgency = "overdue"
		case !submitted && due > 0 && due-s.now.Unix() < 3*86400:
			urgency = "due_soon"
		case !submitted && due > 0:
			urgency = "upcoming"
		case !submitted:
			urgency = "no_due_date"
		case graded:
			urgency = "recently_graded"
		default:
			continue
		}
		taskStatus := str(status["submission_status"])
		if taskStatus == "" {
			taskStatus = "not_started"
		}
		if submitted {
			taskStatus = "graded"
		}
		result = append(result, Object{"id": a["id"], "name": a["name"], "course": a["coursename"], "duedate": due,
			"duedate_formatted": optionalDate(due), "status": taskStatus, "urgency": urgency, "type": "assignment"})
	}
	order := map[string]int{"overdue": 0, "due_soon": 1, "upcoming": 2, "recently_graded": 3, "no_due_date": 4}
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]
		x, y := order[str(a["urgency"])], order[str(b["urgency"])]
		if x != y {
			return x < y
		}
		dx, dy := number(a["duedate"]), number(b["duedate"])
		if dx <= 0 {
			dx = math.Inf(1)
		}
		if dy <= 0 {
			dy = math.Inf(1)
		}
		return dx < dy
	})
	return result, nil
}

func (s *Session) assignment(ctx context.Context, id int64) (Object, error) {
	assignments, err := s.Assignments(ctx, nil)
	if err != nil {
		return nil, err
	}
	for _, a := range assignments {
		if integer(a["id"]) == id {
			return a, nil
		}
	}
	return nil, &APIError{"not_found", "assignment", fmt.Sprintf("Assignment %d not found", id)}
}
