package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCalculate(t *testing.T) {
	for in, want := range map[string]string{
		"1234 * 5678":     "7006652",
		"2^10":            "1024",
		"(1 + 2) / 4":     "0.75",
		"sqrt(2)":         "1.414213562",
		"round(pi * 100)": "314",
		"10 % 4":          "2",
		// Past 2^53, where float64 can no longer hold every integer.
		"123456789 * 987654321":     "121932631112635269",
		"2^62":                      "4611686018427387904",
		"7 / 2 * 2":                 "7",
		"99999999999 * 99999999999": "9999999999800000000001",
		"-2^2 + 2^3^2":              "508",
		"-7 % 3":                    "-1",
	} {
		got, err := Calculate(in)
		if err != nil || got != want {
			t.Errorf("%s = %q, %v; want %s", in, got, err, want)
		}
	}
	for _, in := range []string{"1/0", "os.Exit(1)", `"a" + "b"`, "sqrt(-1)"} {
		if got, err := Calculate(in); err == nil {
			t.Errorf("%s should fail, got %s", in, got)
		}
	}
}

func TestSearchAndObserver(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" || r.URL.Query().Get("q") != "adytus" {
			t.Errorf("unexpected query %s", r.URL)
		}
		w.Write([]byte(`{"results":[{"title":"A","url":"https://a.example","content":"first"},{"title":"B","url":"https://b.example","content":"second"}]}`))
	}))
	defer srv.Close()

	reg, err := NewRegistry(NewSearch(srv.URL), nil)
	if err != nil {
		t.Fatal(err)
	}
	if ids := reg.Enabled(nil); len(ids) != 1 || ids[0] != Calculator {
		t.Fatalf("defaults: %v (search must be opt-in)", ids)
	}
	if ids := reg.Enabled(map[string]bool{WebSearch: true, Calculator: false}); len(ids) != 1 || ids[0] != WebSearch {
		t.Fatalf("choices: %v", ids)
	}

	ts := reg.Tools([]string{WebSearch}, nil)
	out, err := ts[0].(*observed).InvokableRun(context.Background(), `{"query":"adytus"}`)
	if err != nil {
		t.Fatal(err)
	}
	var res []SearchResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res) != 2 || res[1].URL != "https://b.example" {
		t.Fatalf("search output %q", out)
	}
}

func TestObservedReportsSteps(t *testing.T) {
	reg, err := NewRegistry(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var steps []Step
	ts := reg.Tools([]string{Calculator}, func(s Step) { steps = append(steps, s) })
	inv := ts[0].(*observed)
	out, err := inv.InvokableRun(context.Background(), `{"expression":"6*7"}`)
	if err != nil || out != "42" {
		t.Fatalf("%q %v", out, err)
	}
	if len(steps) != 2 || steps[0].Done || !steps[1].Done || steps[1].Output != "42" || steps[0].Tool != Calculator {
		t.Fatalf("steps %+v", steps)
	}
	var in map[string]string
	if json.Unmarshal([]byte(steps[0].Input), &in) != nil || in["expression"] != "6*7" {
		t.Fatalf("input %q", steps[0].Input)
	}
	if out, _ := inv.InvokableRun(context.Background(), `{"expression":"1/0"}`); !strings.HasPrefix(out, "error:") {
		t.Fatalf("errors should go back to the model: %q", out)
	}
}
