package server

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AlexeyLars/go-moodle/internal/config"
	"github.com/AlexeyLars/go-moodle/internal/moodle"
	"github.com/AlexeyLars/go-moodle/internal/testmoodle"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connect(t *testing.T, token string) (*mcp.ClientSession, *testmoodle.Fixture) {
	t.Helper()
	fixture := testmoodle.New(t)
	client := moodle.NewClient(config.Config{URL: fixture.Server.URL + "/moodle/webservice/rest/server.php", Token: token})
	t.Cleanup(client.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	one, two := mcp.NewInMemoryTransports()
	ss, err := New(client).Connect(ctx, one, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, two, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, fixture
}

func TestAllToolsOverMCP(t *testing.T) {
	cs, fixture := connect(t, "local-test-token")
	cases := []struct {
		name  string
		args  map[string]any
		array bool
	}{
		{"get_my_courses", nil, true}, {"get_upcoming_events", nil, true}, {"get_course_content", map[string]any{"courseid": 11}, true},
		{"get_assignments", map[string]any{"courseids": []int{11}}, true}, {"get_assignment_status", map[string]any{"assignid": 101}, false},
		{"get_upcoming_deadlines", nil, true}, {"get_grades", nil, true}, {"search_course_materials", map[string]any{"query": "Лаба3"}, true},
		{"semester_dashboard", nil, false}, {"get_actionable_tasks", nil, true}, {"get_overdue_assignments", nil, true},
		{"get_recent_activity", nil, true}, {"get_course_announcements", nil, true}, {"get_course_health", map[string]any{"courseid": 11}, false},
		{"get_course_progress", nil, true}, {"get_study_load", nil, false}, {"daily_briefing", nil, false}, {"weekly_review", nil, false},
		{"ask_moodle", map[string]any{"question": "courses deadlines grades tasks announcements schedule search Systems"}, false},
		{"analyze_assignment", map[string]any{"assignid": 101}, false}, {"extract_assignment_requirements", map[string]any{"assignid": 101}, false},
		{"find_relevant_materials", map[string]any{"assignid": 101}, false}, {"decompose_task", map[string]any{"assignid": 101}, false},
		{"create_implementation_plan", map[string]any{"assignid": 101}, false},
		{"read_file", map[string]any{"fileurl": fixture.Server.URL + "/moodle/webservice/pluginfile.php/1/mod_resource/content/1/lecture.txt"}, false},
	}
	catalogue, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want, got := []string{}, []string{}
	for _, test := range cases {
		want = append(want, test.name)
	}
	for _, tool := range catalogue.Tools {
		got = append(got, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("%s lacks read-only annotation", tool.Name)
		}
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		t.Fatalf("tool inventory: %v", got)
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			args := test.args
			if args == nil {
				args = map[string]any{}
			}
			result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: test.name, Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			if result.IsError {
				t.Fatalf("tool error: %+v", result.Content)
			}
			if len(result.Content) != 1 {
				t.Fatalf("expected one JSON content block")
			}
			text := result.Content[0].(*mcp.TextContent).Text
			if strings.Contains(text, `\u04`) {
				t.Fatal("escaped Cyrillic")
			}
			var decoded any
			if err := json.Unmarshal([]byte(text), &decoded); err != nil {
				t.Fatal(err)
			}
			if test.array {
				if _, ok := decoded.([]any); !ok {
					t.Fatalf("expected JSON array, got %T", decoded)
				}
			} else if _, ok := decoded.(map[string]any); !ok {
				t.Fatalf("expected object, got %T", decoded)
			}
		})
	}
}

func TestArgumentsAndAuthentication(t *testing.T) {
	cs, _ := connect(t, "local-test-token")
	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{"get_course_content", map[string]any{}}, {"get_course_content", map[string]any{"courseid": "eleven"}},
		{"get_course_content", map[string]any{"courseid": -1}}, {"get_grades", map[string]any{"courseid": -1}},
		{"get_assignments", map[string]any{"courseids": []int{-1}}}, {"get_assignment_status", map[string]any{"assignid": 999}},
		{"read_file", map[string]any{"fileurl": ""}},
		{"read_file", map[string]any{"fileurl": "https://example.edu/file", "limit": -1}},
		{"read_file", map[string]any{"fileurl": "https://example.edu/file", "mode": "invalid"}},
	} {
		result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: test.name, Arguments: test.args})
		if err == nil && !result.IsError {
			t.Fatalf("accepted invalid args for %s", test.name)
		}
	}
	for _, test := range []struct{ name, key string }{{"get_grades", "courseid"}, {"get_assignments", "courseids"}, {"get_recent_activity", "since"}} {
		result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: test.name, Arguments: map[string]any{test.key: nil}})
		if err != nil || result.IsError {
			t.Fatalf("nullable optional input %s rejected: %v", test.name, err)
		}
	}
	expired, _ := connect(t, "invalid-token")
	result, err := expired.CallTool(t.Context(), &mcp.CallToolParams{Name: "daily_briefing", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("authentication failure presented as an empty briefing")
	}
}
