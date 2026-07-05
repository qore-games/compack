package optim

import (
	"bytes"
	"testing"
)

func TestKeepKeyValueLines(t *testing.T) {
	cases := []struct {
		name string
		in   string
		mark []byte
		want string
	}{
		{"basic", "a=1\nb=2\n", []byte{'#'}, "a=1\nb=2\n"},
		{"with_blank", "a=1\n\n\nb=2\n", []byte{'#'}, "a=1\nb=2\n"},
		{"with_comment", "# header\na=1\n# tail\nb=2\n", []byte{'#'}, "a=1\nb=2\n"},
		{"drop_no_eq", "garbage line\na=1\n", []byte{'#'}, "a=1\n"},
		{"bang_marker", "! header\na=1\n", []byte{'#', '!'}, "a=1\n"},
		{"crlf", "a=1\r\nb=2\r\n", []byte{'#'}, "a=1\nb=2\n"},
		{"empty_input", "", []byte{'#'}, ""},
		{"all_comments", "# a\n# b\n", []byte{'#'}, ""},
		{"no_trailing_nl", "a=1", []byte{'#'}, "a=1\n"},
		{"empty_value", "a=\nb=2\n", []byte{'#'}, "a=\nb=2\n"},
		{"spacing_around_eq", "a = 1\nb =2\n", []byte{'#'}, "a = 1\nb =2\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := keepKeyValueLines([]byte(c.in), c.mark...)
			if !bytes.Equal(got, []byte(c.want)) {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestMinifyGLSL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"line_comment", "void main(){// comment\n}",
			"void main(){\n}"},
		{"block_comment", "/* multi\nline\n*/ void main(){}",
			"void main(){}"},
		{"string_with_slash", `void main(){"// not a comment"}`,
			`void main(){"// not a comment"}`},
		{"char_with_slash", `char c='/';`,
			`char c='/';`},
		{"collapse_spaces", "void   main  (  )  {}",
			"void main ( ) {}"},
		{"preserve_directive", "#version 120\nvoid main(){}",
			"#version 120\nvoid main(){}"},
		{"inline_block_comment", "int a/* x */=1;",
			"int a=1;"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := minifyGLSL([]byte(c.in))
			if string(got) != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestOptimizeLangEnd2End(t *testing.T) {
	in := []byte("# comment\na=1\n\nno_separator\nb=2\n")
	res, err := optimizeLang("en_us.lang", in, Options{TextMinify: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Data) != "a=1\nb=2\n" {
		t.Errorf("got %q", res.Data)
	}
	if !res.Changed {
		t.Errorf("expected Changed=true")
	}
}

func TestOptimizeShaderEnd2End(t *testing.T) {
	in := []byte("// vertex shader\n#version 120\nvoid main(){\n  gl_Position=vec4(1.0);\n}\n")
	res, err := optimizeShader("foo.vsh", in, Options{TextMinify: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Data) != "#version 120\nvoid main(){\ngl_Position=vec4(1.0);\n}" {
		t.Errorf("got %q", res.Data)
	}
	if !res.Changed {
		t.Errorf("expected Changed=true")
	}
}
