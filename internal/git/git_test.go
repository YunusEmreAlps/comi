package git_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"comi/internal/git"
	"comi/internal/gittest"
)

var ctx = context.Background()

func TestResolveAll(t *testing.T) {
	repo, _ := gittest.NewRepo(t)
	ids, err := git.ResolveAll(ctx, repo, "HEAD", "refs/heads/missing", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{gittest.Git(t, repo, "rev-parse", "HEAD"), "", gittest.Git(t, repo, "rev-parse", "HEAD^{tree}")}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Errorf("ResolveAll = %q, want %q", ids, want)
	}
}

func TestLines(t *testing.T) {
	repo, _ := gittest.NewRepo(t)
	for i := range 5 {
		gittest.Git(t, repo, "commit", "-q", "--allow-empty", "-m", fmt.Sprint(i))
	}
	collect := func(max int, args ...string) ([]string, error) {
		var got []string
		err := git.Lines(ctx, repo, func(line string) bool {
			got = append(got, line)
			return len(got) < max
		}, args...)
		return got, err
	}

	if got, err := collect(2, "log", "--format=%s"); err != nil || strings.Join(got, ",") != "4,3" {
		t.Errorf("stopped after 2: %q, %v", got, err)
	}
	if got, err := collect(100, "log", "--format=%s"); err != nil || strings.Join(got, ",") != "4,3,2,1,0,init" {
		t.Errorf("all lines: %q, %v", got, err)
	}
	if _, err := collect(100, "log", "no-such-rev"); err == nil || !strings.Contains(err.Error(), "no-such-rev") {
		t.Errorf("got %v, want git's error", err)
	}
}
