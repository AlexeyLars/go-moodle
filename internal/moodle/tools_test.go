package moodle

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestToolResults(t *testing.T) {
	client, fixture := fixtureClient(t)
	s := NewSession(client)
	s.now = fixture.Now
	ctx := t.Context()
	courses, err := s.Courses(ctx)
	if err != nil || len(courses) != 2 || courses[0]["fullname"] != "Теория систем" {
		t.Fatalf("courses: %v", err)
	}
	assignments, err := s.Assignments(ctx, []int64{11})
	if err != nil || len(assignments) != 3 {
		t.Fatalf("filter assignments: %v", err)
	}
	status, err := s.Status(ctx, 103)
	if err != nil || !truth(status["submitted"]) || !truth(status["graded"]) {
		t.Fatalf("status: %v", status)
	}
	deadlines, err := s.Deadlines(ctx)
	if err != nil || len(deadlines) != 1 || integer(deadlines[0]["assignment_id"]) != 101 {
		t.Fatalf("deadlines: %v %v", deadlines, err)
	}
	overdue, err := s.Overdue(ctx)
	if err != nil || len(overdue) != 1 || integer(overdue[0]["days_overdue"]) != 2 {
		t.Fatalf("overdue: %v %v", overdue, err)
	}
	tasks, err := s.Tasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotIDs := []int64{}
	for _, task := range tasks {
		gotIDs = append(gotIDs, integer(task["id"]))
	}
	if !reflect.DeepEqual(gotIDs, []int64{102, 101, 103, 104}) {
		t.Fatalf("urgency ordering: %v", gotIDs)
	}
	grades, err := s.Grades(ctx, 11)
	if err != nil || grades[0]["itemname"] != "Systems exercise" || grades[0]["grade"] != "85" {
		t.Fatalf("grade normalization: %v %v", grades, err)
	}
	matches, err := s.Search(ctx, "лАбА3")
	if err != nil || len(matches) != 2 {
		t.Fatalf("Cyrillic search: %v %v", matches, err)
	}
	announcements, err := s.Announcements(ctx, 0)
	if err != nil || len(announcements) != 1 || announcements[0]["message"] != "Сдаём лабораторную" {
		t.Fatalf("announcements: %v %v", announcements, err)
	}
	progress, err := s.Progress(ctx, 11)
	if err != nil || len(progress) != 1 || number(progress[0]["progress_percentage"]) != 50 {
		t.Fatalf("official completion response: %v %v", progress, err)
	}
	load, err := s.StudyLoad(ctx)
	if err != nil || integer(load["total_assignments"]) != 3 {
		t.Fatalf("study load: %v %v", load, err)
	}
	health, err := s.Health(ctx, 11)
	if err != nil || integer(health["unsubmitted_count"]) != 2 || integer(health["overdue_count"]) != 1 {
		t.Fatalf("health: %v %v", health, err)
	}
	daily, err := s.Daily(ctx)
	if err != nil || integer(daily["overdue_count"]) != 1 {
		t.Fatalf("daily: %v %v", daily, err)
	}
	if count := fixture.Count("core_enrol_get_users_courses"); count != 1 {
		t.Fatalf("repeated course fetches: %d", count)
	}
}

func TestPlanningWithoutCourseSections(t *testing.T) {
	client, _ := fixtureClient(t)
	s := NewSession(client)
	// Cache an empty content response to exercise the plan with no research step
	ready := make(chan struct{})
	close(ready)
	s.cache["core_course_get_contents?courseid=11"] = &pending{ready: ready, value: []any{}}
	plan, err := s.Decompose(t.Context(), 101)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, task := range list(plan["subtasks"]) {
		id := str(task["id"])
		if seen[id] {
			t.Fatalf("duplicate subtask %s", id)
		}
		for _, dependency := range stringList(task["dependencies"]) {
			if !seen[dependency] {
				t.Fatalf("forward, missing, or cyclic dependency %s -> %s", id, dependency)
			}
		}
		seen[id] = true
	}
	for _, id := range stringList(plan["critical_path"]) {
		if !seen[id] {
			t.Fatalf("unknown path step %s", id)
		}
	}
	req, err := s.Requirements(t.Context(), 101)
	if err != nil {
		t.Fatal(err)
	}
	if len(stringList(req["deliverables"])) != 2 {
		t.Fatalf("deliverables: %v", req)
	}
	if len(stringList(req["deadlines"])) != 1 {
		t.Fatalf("deadline: %v", req)
	}
	analysis, err := s.Analyze(t.Context(), 101)
	if err != nil {
		t.Fatal(err)
	}
	if analysis["course_grade"] != "85" || integer(analysis["requirements_count"]) != 3 {
		t.Fatalf("analysis: %v", analysis)
	}
}

func TestDateAndUnicodeSemantics(t *testing.T) {
	if days(-1) != -1 {
		t.Fatal("negative day counts must round down")
	}
	sunday := time.Date(2026, 1, 4, 23, 59, 0, 0, time.UTC)
	if monday(sunday).Format(time.DateOnly) != "2025-12-29" {
		t.Fatal("ISO week boundary")
	}
	text := "Теория систем 🧪"
	if short(text, 6) != "Теория" {
		t.Fatal("truncated UTF-8")
	}
	req := requirements("<p>Create a model.</p><p>Use PDF format.</p>")
	if len(req) != 2 || req[1]["category"] != "format" {
		t.Fatalf("HTML sentences: %v", req)
	}
	if strings.Contains(cleanHTML("<p>Теория</p>"), "<p>") {
		t.Fatal("HTML not stripped")
	}
}
