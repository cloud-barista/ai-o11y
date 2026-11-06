package promapi

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
	"github.com/cloud-barista/ai-o11y/internal/retry"
)

// request is what the fake API saw.
type request struct {
	method, path, contentType, header string
	form                              url.Values
}

// fakeAPI answers every request with status and body and records it.
func fakeAPI(t *testing.T, status int, body string) (*Client, *[]request) {
	t.Helper()
	var seen []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		seen = append(seen, request{r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("X-Test"), r.PostForm})
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(Config{
		BaseURL: srv.URL + "/",
		Timeout: 30 * time.Second,
		Headers: map[string]string{"X-Test": "yes"},
		Params:  url.Values{"dedup": {"true"}},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.retry = retry.Policy{MaxRetries: retry.DefaultMaxRetries, Base: time.Millisecond, MaxWait: time.Millisecond}
	return c, &seen
}

func TestInstant(t *testing.T) {
	c, seen := fakeAPI(t, 200, `{"status":"success","warnings":["store a unreachable"],"data":{"resultType":"vector","result":[
		{"metric":{"entity_id":"k1"},"value":[1700000000.5,"0.25"]},
		{"metric":{"entity_id":"k2"},"value":[1700000000.5,"NaN"]}]}}`)
	at := time.Unix(1700000000, 500*int64(time.Millisecond))

	got, err := c.Instant(context.Background(), `up{entity_id=~"k1|k2"}`, at)
	if err != nil {
		t.Fatalf("Instant: %v", err)
	}
	if len(got) != 2 || got[0].Labels["entity_id"] != "k1" || got[0].Points[0].V != 0.25 ||
		!got[0].Points[0].T.Equal(at) || !math.IsNaN(got[1].Points[0].V) {
		t.Errorf("Instant = %+v", got)
	}
	want := request{
		method: "POST", path: "/api/v1/query", contentType: "application/x-www-form-urlencoded", header: "yes",
		form: url.Values{"query": {`up{entity_id=~"k1|k2"}`}, "time": {"1700000000.5"}, "timeout": {"30"}, "dedup": {"true"}},
	}
	if !reflect.DeepEqual((*seen)[0], want) {
		t.Errorf("request = %+v\nwant %+v", (*seen)[0], want)
	}
}

func TestInstantNow(t *testing.T) {
	c, seen := fakeAPI(t, 200, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	if _, err := c.Instant(context.Background(), "up", time.Time{}); err != nil {
		t.Fatalf("Instant: %v", err)
	}
	if _, ok := (*seen)[0].form["time"]; ok {
		t.Error("a zero time was sent; the API's own now should be used")
	}
}

func TestRange(t *testing.T) {
	c, seen := fakeAPI(t, 200, `{"status":"success","data":{"resultType":"matrix","result":[
		{"metric":{"entity_id":"k1"},"values":[[1700000000,"1"],[1700000030,"+Inf"]]}]}}`)
	start := time.Unix(1700000000, 0)

	got, err := c.Range(context.Background(), "up", start, start.Add(time.Minute), 30*time.Second)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	want := []monitoring.Series{{Labels: map[string]string{"entity_id": "k1"}, Points: []monitoring.Point{
		{T: start.UTC(), V: 1}, {T: start.Add(30 * time.Second).UTC(), V: math.Inf(1)},
	}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Range = %+v, want %+v", got, want)
	}
	form := (*seen)[0].form
	if (*seen)[0].path != "/api/v1/query_range" || form.Get("start") != "1700000000" ||
		form.Get("end") != "1700000060" || form.Get("step") != "30" {
		t.Errorf("request = %+v", (*seen)[0])
	}
}

func TestScalar(t *testing.T) {
	c, _ := fakeAPI(t, 200, `{"status":"success","data":{"resultType":"scalar","result":[1700000000,"3"]}}`)
	got, err := c.Instant(context.Background(), "3", time.Time{})
	if err != nil || len(got) != 1 || got[0].Points[0].V != 3 {
		t.Errorf("Instant scalar = %+v, %v", got, err)
	}
}

func TestErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		status   int
		body     string
		wantType string
		wantMsg  string
	}{
		"bad query":         {400, `{"status":"error","errorType":"bad_data","error":"parse error"}`, "bad_data", "parse error"},
		"store failed":      {422, `{"status":"error","errorType":"execution","error":"receive series from store: unavailable"}`, "execution", "receive series from store: unavailable"},
		"proxy page":        {502, `<html>Bad Gateway</html>`, "", "<html>Bad Gateway</html>"},
		"error with 200 OK": {200, `{"status":"error","errorType":"internal","error":"boom"}`, "internal", "boom"},
	} {
		c, _ := fakeAPI(t, tc.status, tc.body)
		_, err := c.Instant(context.Background(), "up", time.Time{})
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.status || apiErr.Type != tc.wantType || apiErr.Message != tc.wantMsg {
			t.Errorf("%s: err = %#v", name, err)
		}
	}
}

func TestRetryOnRateLimit(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= retry.DefaultMaxRetries {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer srv.Close()
	c, _ := NewClient(Config{BaseURL: srv.URL, Timeout: time.Second})
	c.retry = retry.Policy{MaxRetries: retry.DefaultMaxRetries, Base: time.Millisecond, MaxWait: time.Millisecond}
	if _, err := c.Instant(context.Background(), "up", time.Time{}); err != nil {
		t.Fatalf("Instant after %d 429s: %v", retry.DefaultMaxRetries, err)
	}
	if calls != retry.DefaultMaxRetries+1 {
		t.Errorf("calls = %d, want %d", calls, retry.DefaultMaxRetries+1)
	}

	calls = -100 // every answer is a 429 now
	_, err := c.Instant(context.Background(), "up", time.Time{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("err = %v, want the last 429", err)
	}
}

func TestSeries(t *testing.T) {
	c, seen := fakeAPI(t, 200, `{"status":"success","data":[{"__name__":"up","entity_id":"k1"}]}`)
	got, err := c.Series(context.Background(), `up{entity_id="k1"}`)
	if err != nil || len(got) != 1 || got[0]["entity_id"] != "k1" {
		t.Errorf("Series = %v, %v", got, err)
	}
	if (*seen)[0].path != "/api/v1/series" || (*seen)[0].form.Get("match[]") != `up{entity_id="k1"}` {
		t.Errorf("request = %+v", (*seen)[0])
	}
}

// TestTimeoutInSeconds: Prometheus reads a float number of seconds, not Go's
// duration text ("1.5s" does not parse there).
func TestTimeoutInSeconds(t *testing.T) {
	c, seen := fakeAPI(t, 200, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	c.cfg.Timeout = 1500 * time.Millisecond
	if _, err := c.Instant(context.Background(), "up", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if got := (*seen)[0].form.Get("timeout"); got != "1.5" {
		t.Errorf("timeout = %q, want 1.5", got)
	}
}
