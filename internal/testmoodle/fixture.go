// Package testmoodle supplies synthetic Moodle responses for integration tests
package testmoodle

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

type Object = map[string]any
type Fixture struct {
	Server *httptest.Server
	Now    time.Time
	mu     sync.Mutex
	counts map[string]int
}

func New(t testing.TB) *Fixture {
	t.Helper()
	f := &Fixture{Now: time.Now().UTC().Truncate(time.Hour), counts: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Server.Close)
	return f
}
func (f *Fixture) Count(function string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[function]
}
func (f *Fixture) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	write := func(value any) { _ = json.NewEncoder(w).Encode(value) }
	if r.Method != http.MethodPost || r.URL.RawQuery != "" {
		http.Error(w, "POST body required", 400)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	if r.URL.Path == "/moodle/webservice/pluginfile.php/1/mod_resource/content/1/lecture.txt" {
		if r.Form.Get("token") != "local-test-token" {
			http.Error(w, "invalid token", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("Лекция: коллективное принятие решений"))
		return
	}
	if r.URL.Path == "/moodle/login/token.php" {
		if r.Form.Get("username") != "student" || r.Form.Get("password") != "local-test-password" || r.Form.Get("service") != "moodle_mobile_app" {
			write(Object{"errorcode": "invalidlogin", "error": "invalid credentials"})
			return
		}
		write(Object{"token": "local-test-token", "privatetoken": "never-save-this"})
		return
	}
	if r.URL.Path != "/moodle/webservice/rest/server.php" {
		http.NotFound(w, r)
		return
	}
	function := r.Form.Get("wsfunction")
	f.mu.Lock()
	f.counts[function]++
	f.mu.Unlock()
	if r.Form.Get("wstoken") != "local-test-token" {
		write(Object{"errorcode": "invalidtoken", "message": "invalid token"})
		return
	}
	now := f.Now.Unix()
	switch function {
	case "core_webservice_get_site_info":
		write(Object{"userid": 7, "sitename": "Test Moodle", "release": "4.5"})
	case "core_enrol_get_users_courses":
		write([]Object{{"id": 11, "fullname": "Теория систем", "shortname": "ТС", "category": 1, "progress": 25, "format": "topics", "startdate": 0, "enddate": 0},
			{"id": 12, "fullname": "Algorithms", "shortname": "ALG", "category": 1, "progress": 50, "format": "topics", "startdate": 0, "enddate": 0}})
	case "core_course_get_contents":
		write([]Object{{"name": "Лабораторные работы", "section": 1, "visible": 1, "uservisible": true, "modules": []Object{
			{"id": 900, "name": "Лабораторная № 3 — Systems", "modname": "resource", "url": f.Server.URL + "/resource/900", "contents": []Object{
				{"filename": "Лаба3.pdf", "fileurl": f.Server.URL + "/file/900", "type": "file", "filesize": 120, "timemodified": now, "mimetype": "application/pdf"},
			}},
			{"id": 901, "name": "Systems exercise", "modname": "assign", "url": f.Server.URL + "/assign/101"},
		}}})
	case "mod_assign_get_assignments":
		courses := []Object{
			{"id": 11, "fullname": "Теория систем", "assignments": []Object{
				{"id": 101, "name": "Systems exercise", "duedate": now + 3*3600, "cutoffdate": 0, "intro": "<p>You must create a model. Write a report. Use PDF format.</p>"},
				{"id": 102, "name": "Overdue work", "duedate": now - 2*86400, "cutoffdate": 0, "intro": "<p>Submit the result.</p>"},
				{"id": 103, "name": "Completed work", "duedate": now - 86400, "cutoffdate": 0, "intro": ""},
			}},
			{"id": 12, "fullname": "Algorithms", "assignments": []Object{{"id": 104, "name": "No date", "duedate": 0, "cutoffdate": 0, "intro": nil}}},
		}
		filtered := []Object{}
		for _, course := range courses {
			match := r.Form.Get("courseids[0]") == ""
			for i := 0; ; i++ {
				v := r.Form.Get("courseids[" + strconv.Itoa(i) + "]")
				if v == "" {
					break
				}
				if v == strconv.Itoa(course["id"].(int)) {
					match = true
				}
			}
			if match {
				filtered = append(filtered, course)
			}
		}
		write(Object{"courses": filtered, "warnings": []any{}})
	case "mod_assign_get_submission_status":
		id := r.Form.Get("assignid")
		status := "new"
		graded := false
		if id == "101" {
			status = "draft"
		} else if id == "103" {
			status = "submitted"
			graded = true
		}
		if id == "999" {
			write(Object{"errorcode": "invalidrecord", "message": "not found"})
			return
		}
		write(Object{"lastattempt": Object{"submission": Object{"status": status}, "gradingstatus": "graded", "graded": graded, "late": false, "extensionduedate": 0},
			"assignment": Object{"duedate": now + 3*3600, "cutoffdate": 0}})
	case "gradereport_overview_get_course_grades":
		write(Object{"grades": []Object{{"courseid": 11, "grade": "85", "rank": "2"}, {"courseid": 12, "grade": "", "rank": nil}}})
	case "gradereport_user_get_grade_items":
		write(Object{"usergrades": []Object{{"gradeitems": []Object{{"itemname": Object{"value": "Systems exercise"}, "gradeformatted": "85", "percentageformatted": "85%", "feedback": nil}, {"itemname": "Course total", "gradeformatted": "85", "percentageformatted": "85%", "feedback": "Good"}}}}})
	case "core_calendar_get_calendar_upcoming_view":
		write(Object{"events": []Object{{"id": 801, "name": "Защита", "formattedtime": "Today", "url": f.Server.URL + "/event/801", "description": "<p>Лабораторная работа</p>", "popupname": "Event"}}})
	case "core_course_get_updates_since":
		write(Object{"instances": []Object{{"component": "assign", "name": "Systems exercise", "timecreated": now - 3600}}})
	case "mod_forum_get_forums_by_courses":
		write([]Object{{"id": 301, "type": "news", "course": 11}, {"id": 302, "type": "general", "course": 11}})
	case "mod_forum_get_discussions":
		write(Object{"discussions": []Object{{"id": 401, "subject": "Объявление", "message": "<p>Сдаём лабораторную</p>", "created": now - 3600, "userfullname": "Teacher"}}})
	case "core_completion_get_course_completion_status":
		write(Object{"completionstatus": Object{"completed": false, "completions": []Object{{"complete": true}, {"complete": false}}}})
	default:
		write(Object{"errorcode": "unknownfunction", "message": "unexpected function"})
	}
}
