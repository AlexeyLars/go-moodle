package moodle

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Object = map[string]any
type Items = []Object

func obj(v any) Object {
	o, _ := v.(map[string]any)
	if o == nil {
		return Object{}
	}
	return o
}
func list(v any) Items {
	if items, ok := v.([]Object); ok {
		return items
	}
	result := Items{}
	if items, ok := v.([]any); ok {
		for _, item := range items {
			if o, ok := item.(map[string]any); ok {
				result = append(result, o)
			}
		}
	}
	return result
}
func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
func number(v any) float64 {
	switch n := v.(type) {
	case json.Number:
		value, _ := n.Float64()
		return value
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case string:
		value, _ := strconv.ParseFloat(n, 64)
		return value
	}
	return 0
}
func integer(v any) int64 { return int64(number(v)) }
func truth(v any) bool {
	switch value := v.(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		return value != ""
	case []any:
		return len(value) > 0
	case []Object:
		return len(value) > 0
	default:
		return number(v) != 0
	}
}
func valueOr(o Object, key string, fallback any) any {
	if value, ok := o[key]; ok && value != nil {
		return value
	}
	return fallback
}
func first(values ...any) any {
	for _, v := range values {
		if truth(v) {
			return v
		}
	}
	return nil
}
func fields(o Object, names ...string) Object {
	result := Object{}
	for _, key := range names {
		result[key] = o[key]
	}
	return result
}
func short(s string, count int) string {
	r := []rune(s)
	if len(r) > count {
		return string(r[:count])
	}
	return s
}
func iso(ts int64) string { return time.Unix(ts, 0).UTC().Format("2006-01-02T15:04:05+00:00") }
func optionalDate(ts int64) any {
	if ts <= 0 {
		return nil
	}
	return iso(ts)
}
func days(delta int64) int64 { return int64(math.Floor(float64(delta) / 86400)) }
func round(v float64, digits int) float64 {
	scale := math.Pow10(digits)
	return math.RoundToEven(v*scale) / scale
}
func sorted(items Items, key string, reverse bool) Items {
	sort.SliceStable(items, func(i, j int) bool {
		if reverse {
			return number(items[i][key]) > number(items[j][key])
		}
		return number(items[i][key]) < number(items[j][key])
	})
	return items
}
func take[T any](items []T, n int) []T {
	if len(items) > n {
		return items[:n]
	}
	return items
}
func arrayParams(key string, ids []int64) url.Values {
	p := url.Values{}
	for i, id := range ids {
		p.Set(fmt.Sprintf("%s[%d]", key, i), strconv.FormatInt(id, 10))
	}
	return p
}
func idParams(key string, id int64) url.Values { return url.Values{key: {strconv.FormatInt(id, 10)}} }
func contains(text string, keywords []string) bool {
	for _, word := range keywords {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

// A session belongs to a single tool call: repeated reads are deduplicated, but
// a subsequent user request always sees fresh Moodle data
type Session struct {
	client *Client
	now    time.Time
	mu     sync.Mutex
	cache  map[string]*pending
}
type pending struct {
	ready chan struct{}
	value any
	err   error
}

func NewSession(client *Client) *Session {
	return &Session{client: client, now: time.Now().UTC(), cache: map[string]*pending{}}
}
func (s *Session) Validate(ctx context.Context) error {
	_, err := s.userID(ctx)
	return err
}
func (s *Session) call(ctx context.Context, function string, params url.Values) (any, error) {
	key := function + "?" + params.Encode()
	s.mu.Lock()
	if old := s.cache[key]; old != nil {
		s.mu.Unlock()
		select {
		case <-old.ready:
			return old.value, old.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	p := &pending{ready: make(chan struct{})}
	s.cache[key] = p
	s.mu.Unlock()
	p.value, p.err = s.client.Call(ctx, function, params)
	close(p.ready)
	return p.value, p.err
}
func (s *Session) userID(ctx context.Context) (int64, error) {
	value, err := s.call(ctx, "core_webservice_get_site_info", nil)
	if err != nil {
		return 0, err
	}
	id := integer(obj(value)["userid"])
	if id <= 0 {
		return 0, fmt.Errorf("Moodle не вернул userid")
	}
	return id, nil
}

// Keep output order stable while limiting parallel fan-out to four workers
func parallel[T any, R any](ctx context.Context, input []T, fn func(T) (R, error)) ([]R, []error) {
	values := make([]R, len(input))
	errs := make([]error, len(input))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(4, len(input)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					errs[i] = ctx.Err()
					continue
				}
				values[i], errs[i] = fn(input[i])
			}
		}()
	}
	for i := range input {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return values, errs
}
