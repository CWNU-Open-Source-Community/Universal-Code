package application

import "testing"

func TestThreadActivityOutputPreviewUnicodeWorkspace(t *testing.T) {
	root := `C:\work\K`
	output := root + "\n" + `c:\WORK\k\file.go` + "\n"
	got, _ := threadActivityOutputPreview(output, root, int64(len(output)), nil)
	if want := ".\n.\\file.go\n"; got != want {
		t.Fatalf("preview=%q want=%q", got, want)
	}
}

func TestReplaceFoldPreservesOriginalUnicodeOffsets(t *testing.T) {
	for _, test := range []struct{ value, old, replacement, want string }{
		{"K before /WORK/K then /work/K!", "/work/k", ".", "K before . then .!"},
		{"ſ/K ſ/k", "S/K", "$1", "$1 $1"},
		{"[a.b] [A.B] axb", "[a.b]", ".", ". . axb"},
		{"untouched", "missing", ".", "untouched"},
		{"untouched", "", ".", "untouched"},
	} {
		if got := replaceFold(test.value, test.old, test.replacement); got != test.want {
			t.Fatalf("replaceFold(%q, %q)=%q want=%q", test.value, test.old, got, test.want)
		}
	}
}
