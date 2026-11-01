package moodle

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

func (s *Session) Courses(ctx context.Context) (Items, error) {
	id, err := s.userID(ctx)
	if err != nil {
		return nil, err
	}
	data, err := s.call(ctx, "core_enrol_get_users_courses", idParams("userid", id))
	if err != nil {
		return nil, err
	}
	result := Items{}
	for _, c := range list(data) {
		o := fields(c, "id", "fullname", "shortname", "progress")
		o["category"] = valueOr(c, "category", 0)
		o["format"] = valueOr(c, "format", "")
		o["startdate"] = valueOr(c, "startdate", 0)
		o["enddate"] = valueOr(c, "enddate", 0)
		result = append(result, o)
	}
	return result, nil
}

func (s *Session) Content(ctx context.Context, id int64) (Items, error) {
	data, err := s.call(ctx, "core_course_get_contents", idParams("courseid", id))
	if err != nil {
		return nil, err
	}
	result := Items{}
	for _, section := range list(data) {
		modules := Items{}
		for _, m := range list(section["modules"]) {
			item := fields(m, "id", "name", "modname", "url")
			if m["contents"] != nil {
				files := Items{}
				for _, raw := range list(m["contents"]) {
					c := Object{}
					for k, v := range raw {
						c[k] = v
					}
					for _, key := range []string{"filepath", "timecreated", "timemodified", "sortorder", "isexternalfile", "repositorytype", "userid", "author", "license", "mimetype"} {
						delete(c, key)
					}
					files = append(files, c)
				}
				item["contents"] = files
			} else {
				item["contents"] = nil
			}
			modules = append(modules, item)
		}
		result = append(result, Object{"name": section["name"], "section": section["section"],
			"visible": valueOr(section, "visible", 1), "uservisible": valueOr(section, "uservisible", true), "modules": modules})
	}
	return result, nil
}

func (s *Session) Events(ctx context.Context) (Items, error) {
	data, err := s.call(ctx, "core_calendar_get_calendar_upcoming_view", nil)
	if err != nil {
		return nil, err
	}
	result := Items{}
	for _, e := range list(obj(data)["events"]) {
		result = append(result, fields(e, "id", "name", "formattedtime", "url", "description", "popupname"))
	}
	return result, nil
}

func (s *Session) Grades(ctx context.Context, courseID int64) (Items, error) {
	userID, err := s.userID(ctx)
	if err != nil {
		return nil, err
	}
	params := idParams("userid", userID)
	if courseID != 0 {
		params.Set("courseid", strconv.FormatInt(courseID, 10))
		data, err := s.call(ctx, "gradereport_user_get_grade_items", params)
		if err != nil {
			return nil, err
		}
		users := list(obj(data)["usergrades"])
		result := Items{}
		if len(users) == 0 {
			return result, nil
		}
		for _, g := range list(users[0]["gradeitems"]) {
			name := valueOr(g, "itemname", "")
			if o, ok := name.(map[string]any); ok {
				name = valueOr(o, "value", "")
			}
			result = append(result, Object{"itemname": name, "grade": first(g["gradeformatted"], g["grade"]),
				"percentage": first(g["percentageformatted"], g["percentage"]), "feedback": g["feedback"]})
		}
		return result, nil
	}
	data, err := s.call(ctx, "gradereport_overview_get_course_grades", params)
	if err != nil {
		return nil, err
	}
	courses, err := s.Courses(ctx)
	if err != nil {
		return nil, err
	}
	names := map[int64]any{}
	for _, c := range courses {
		names[integer(c["id"])] = c["fullname"]
	}
	result := Items{}
	for _, g := range list(obj(data)["grades"]) {
		id := integer(g["courseid"])
		name := names[id]
		if name == nil {
			name = "Unknown"
		}
		result = append(result, Object{"courseid": id, "course_name": name, "grade": valueOr(g, "grade", ""), "rank": g["rank"]})
	}
	return result, nil
}

func (s *Session) Search(ctx context.Context, query string) (Items, error) {
	courses, err := s.Courses(ctx)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	result := Items{}
	if needle == "" {
		return result, nil
	}
	sections, errs := parallel(ctx, courses, func(c Object) (Items, error) { return s.Content(ctx, integer(c["id"])) })
	for i, course := range courses {
		if errs[i] != nil {
			continue
		}
		for _, section := range sections[i] {
			for _, m := range list(section["modules"]) {
				parts := []string{str(m["name"]), str(section["name"])}
				for _, f := range list(m["contents"]) {
					parts = append(parts, str(f["filename"]))
				}
				if strings.Contains(strings.ToLower(strings.Join(parts, " ")), needle) {
					result = append(result, Object{"title": str(m["name"]), "url": str(m["url"]), "content": str(section["name"]), "course_name": course["fullname"]})
				}
			}
		}
	}
	return result, nil
}

func (s *Session) Announcements(ctx context.Context, courseID int64) (Items, error) {
	courses, err := s.Courses(ctx)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	names := map[int64]string{}
	for _, c := range courses {
		id := integer(c["id"])
		names[id] = str(c["fullname"])
		if courseID == 0 || courseID == id {
			ids = append(ids, id)
		}
	}
	if courseID != 0 {
		ids = []int64{courseID}
	}
	result := Items{}
	if len(ids) == 0 {
		return result, nil
	}
	data, err := s.call(ctx, "mod_forum_get_forums_by_courses", arrayParams("courseids", ids))
	if err != nil {
		return result, nil
	}
	forums := list(data)
	if _, ok := data.(map[string]any); ok {
		forums = list(obj(data)["forums"])
	}
	news := Items{}
	for _, f := range forums {
		if str(f["type"]) == "news" {
			news = append(news, f)
		}
	}
	discussions, errs := parallel(ctx, news, func(f Object) (any, error) {
		params := idParams("forumid", integer(f["id"]))
		params.Set("perpage", "20")
		return s.call(ctx, "mod_forum_get_discussions", params)
	})
	for i, f := range news {
		if errs[i] != nil {
			continue
		}
		items := list(discussions[i])
		if _, ok := discussions[i].(map[string]any); ok {
			items = list(obj(discussions[i])["discussions"])
		}
		for _, d := range items {
			created := integer(first(d["created"], d["timemodified"]))
			date := ""
			if created > 0 {
				date = iso(created)
			}
			result = append(result, Object{"id": valueOr(d, "id", 0), "subject": valueOr(d, "subject", ""),
				"message": strings.TrimSpace(tags.ReplaceAllString(str(d["message"]), "")),
				"course":  names[integer(f["course"])], "author": valueOr(d, "userfullname", ""), "date": created, "date_formatted": date})
		}
	}
	return sorted(result, "date", true), nil
}

func (s *Session) RecentActivity(ctx context.Context, since *int64) (Items, error) {
	start := s.now.Unix() - 7*86400
	if since != nil {
		start = *since
	}
	courses, err := s.Courses(ctx)
	if err != nil {
		return nil, err
	}
	updates, errs := parallel(ctx, courses, func(c Object) (any, error) {
		p := idParams("courseid", integer(c["id"]))
		p.Set("since", strconv.FormatInt(start, 10))
		return s.call(ctx, "core_course_get_updates_since", p)
	})
	result := Items{}
	for i, c := range courses {
		if errs[i] != nil {
			continue
		}
		for _, u := range list(obj(updates[i])["instances"]) {
			timestamp := integer(valueOr(u, "timecreated", start))
			result = append(result, Object{"course": c["fullname"], "type": "update",
				"description": fmt.Sprintf("Update in %v: %s", valueOr(u, "component", "unknown"), str(u["name"])),
				"timestamp":   timestamp, "timestamp_formatted": iso(timestamp), "url": nil})
		}
	}
	if len(result) == 0 {
		grades, err := s.Grades(ctx, 0)
		if err == nil {
			for _, g := range grades {
				if truth(g["grade"]) {
					result = append(result, Object{"course": g["course_name"], "type": "grade", "description": "Grade received: " + str(g["grade"]),
						"timestamp": start, "timestamp_formatted": iso(start), "url": nil})
				}
			}
		}
	}
	return sorted(result, "timestamp", true), nil
}

func (s *Session) Progress(ctx context.Context, courseID int64) (Items, error) {
	userID, err := s.userID(ctx)
	if err != nil {
		return nil, err
	}
	all, err := s.Courses(ctx)
	if err != nil {
		return nil, err
	}
	courses := Items{}
	for _, c := range all {
		if courseID == 0 || integer(c["id"]) == courseID {
			courses = append(courses, c)
		}
	}
	results, _ := parallel(ctx, courses, func(c Object) (Object, error) {
		id := integer(c["id"])
		percentage := c["progress"]
		completed, total := 0, 0
		p := idParams("courseid", id)
		p.Set("userid", strconv.FormatInt(userID, 10))
		data, err := s.call(ctx, "core_completion_get_course_completion_status", p)
		if err == nil {
			statuses := list(obj(data)["status"])
			// Moodle's official response nests completion criteria under completionstatus
			if statuses == nil || len(statuses) == 0 {
				statuses = list(obj(obj(data)["completionstatus"])["completions"])
			}
			total = len(statuses)
			for _, status := range statuses {
				if truth(status["complete"]) {
					completed++
				}
			}
			if total > 0 {
				percentage = round(float64(completed)/float64(total)*100, 1)
			}
		} else {
			assignments, e := s.Assignments(ctx, []int64{id})
			if e == nil {
				total = len(assignments)
				statuses, errs := s.statuses(ctx, assignments)
				for i, status := range statuses {
					if errs[i] == nil && truth(status["submitted"]) {
						completed++
					}
				}
				if total > 0 {
					percentage = round(float64(completed)/float64(total)*100, 1)
				}
			}
		}
		return Object{"courseid": id, "coursename": c["fullname"], "progress_percentage": percentage, "completed_activities": completed, "total_activities": total}, nil
	})
	return results, nil
}

func (s *Session) Health(ctx context.Context, courseID int64) (Object, error) {
	courses, err := s.Courses(ctx)
	if err != nil {
		return nil, err
	}
	result := Object{"courseid": courseID, "coursename": "", "progress": nil, "grade": nil, "last_activity": nil}
	for _, c := range courses {
		if integer(c["id"]) == courseID {
			result["coursename"] = c["fullname"]
			result["progress"] = c["progress"]
		}
	}
	if grades, err := s.Grades(ctx, courseID); err == nil {
		for _, g := range grades {
			if truth(g["grade"]) && strings.Contains(strings.ToLower(str(g["itemname"])), "course") {
				result["grade"] = g["grade"]
				break
			}
		}
	}
	assignments, err := s.Assignments(ctx, []int64{courseID})
	if err != nil {
		return nil, err
	}
	result["total_assignments"] = len(assignments)
	unsubmitted, overdue := 0, 0
	statuses, errs := s.statuses(ctx, assignments)
	for i, a := range assignments {
		if errs[i] != nil {
			continue
		}
		if !truth(statuses[i]["submitted"]) {
			unsubmitted++
			due := integer(a["duedate"])
			if due > 0 && due < s.now.Unix() {
				overdue++
			}
		}
	}
	result["unsubmitted_count"] = unsubmitted
	result["overdue_count"] = overdue
	if sections, err := s.Content(ctx, courseID); err == nil {
		var latest int64
		for _, section := range sections {
			for _, m := range list(section["modules"]) {
				for _, f := range list(m["contents"]) {
					latest = max(latest, integer(first(f["timemodified"], f["timecreated"])))
				}
			}
		}
		result["last_activity"] = optionalDate(latest)
	}
	return result, nil
}
