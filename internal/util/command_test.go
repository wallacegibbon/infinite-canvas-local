package util

import "testing"

func TestCommandLineIsCopyPasteable(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"zenity", []string{"--file-selection", "--title=选择模型文件"}, "zenity --file-selection --title=选择模型文件"},
		{"ffprobe", []string{"-select_streams", "v:0", "/tmp/a b.mp4"}, "ffprobe -select_streams v:0 '/tmp/a b.mp4'"},
		{"x", []string{`it's`}, `x 'it'\''s'`},
		{"x", []string{""}, "x ''"},
	}
	for _, testCase := range cases {
		if got := CommandLine(testCase.name, testCase.args...); got != testCase.want {
			t.Errorf("CommandLine(%q, %v) = %q, want %q", testCase.name, testCase.args, got, testCase.want)
		}
	}
}

func TestCommandLineQuotesMetacharacters(t *testing.T) {
	// Anything that a shell would interpret must be quoted, or pasting the logged
	// line back would run something different from what the gateway ran.
	for _, unsafe := range []string{"a;rm -rf /", "a|b", "$HOME", "`whoami`", "a&b", "a>b", "a*b"} {
		got := CommandLine("cmd", unsafe)
		if got != "cmd '"+unsafe+"'" && got != `cmd '`+unsafe+`'` {
			t.Errorf("CommandLine(%q) = %q, expected the argument to be quoted", unsafe, got)
		}
	}
	// A digest or path without metacharacters stays readable.
	if got := CommandLine("cmd", "models/unet/a.safetensors"); got != "cmd models/unet/a.safetensors" {
		t.Errorf("CommandLine left an unnecessary quote: %q", got)
	}
}
