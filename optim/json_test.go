package optim

import (
	"testing"
)

func TestMinifyJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"whitespace", `  { "a" : 1 , "b" : 2 }  `, `{"a":1,"b":2}`},
		{"newlines", "{\n  \"a\": 1,\n  \"b\": 2\n}", `{"a":1,"b":2}`},
		{"linecomment", `{"a":1}// trailing
{"b":2}`, `{"a":1}{"b":2}`},
		{"blockcomment", `{"a":1}/* block
comment */{"b":2}`, `{"a":1}{"b":2}`},
		{"string_with_slash", `{"url":"http://example.com"}`, `{"url":"http://example.com"}`},
		{"string_with_quote", `{"a":"he said \"hi\""}`, `{"a":"he said \"hi\""}`},
		{"string_with_hash", `{"a":"# not a comment"}`, `{"a":"# not a comment"}`},
		{"string_with_newline_escape", `{"a":"foo\nbar"}`, `{"a":"foo\nbar"}`},
		{"empty_array", `  [ ]  `, `[]`},
		{"nested", `{"a":{"b":[1,  2,  3]}}`, `{"a":{"b":[1,2,3]}}`},
		{"comment_in_string", `{"a":"//notcomment"}`, `{"a":"//notcomment"}`},
		{"block_comment_in_string", `{"a":"/*not*/"}`, `{"a":"/*not*/"}`},
		{"already_min", `{"a":1}`, `{"a":1}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := minifyJSON([]byte(c.in))
			if string(got) != c.want {
				t.Errorf("minifyJSON(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestMinifyJSONDetectsComments(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{`{"a":1}`, false},
		{`{"a":1}// c`, true},
		{`{"a":1}/* c */`, true},
		{`{"a":"//not a comment"}`, false},
		{`{"a":"/*not a comment*/"}`, false},
	}
	for _, c := range cases {
		_, got := minifyJSON([]byte(c.in))
		if got != c.want {
			t.Errorf("minifyJSON(%q) comment detected=%v, want %v", c.in, got, c.want)
		}
	}
}

func TestOptimizeJSONRoundTripWithComments(t *testing.T) {
	in := []byte(`{
  // player model
  "a": 1,
  "b": 2 /* end */
}`)
	res, err := optimizeJSON("foo.json", in, Options{JSONMinify: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Data) != `{"a":1,"b":2}` {
		t.Errorf("got %q", res.Data)
	}
	if res.Note == "" {
		t.Errorf("expected warning note for .json with comments")
	}
}

func TestOptimizeJSONNoWarningForJSONC(t *testing.T) {
	in := []byte(`{
  // player model
  "a": 1
}`)
	res, err := optimizeJSON("foo.jsonc", in, Options{JSONMinify: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Note != "" {
		t.Errorf("did not expect warning for .jsonc file, got: %s", res.Note)
	}
}
