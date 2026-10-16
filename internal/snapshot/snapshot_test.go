package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"comi/internal/git"
	"comi/internal/gittest"
)

var (
	ctx = context.Background()
	t0  = time.Date(2026, 1, 15, 10, 0, 0, 0, time.Local)
)

func take(t *testing.T, repo string, opts Options, now time.Time) string {
	t.Helper()
	msg, err := Take(ctx, repo, opts, now)
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

// repoState captures everything a snapshot must not change.
func repoState(t *testing.T, repo string) string {
	t.Helper()
	return strings.Join([]string{
		gittest.Git(t, repo, "rev-parse", "HEAD"),
		gittest.Git(t, repo, "symbolic-ref", "-q", "HEAD"),
		gittest.Git(t, repo, "status", "--porcelain"),
		gittest.Git(t, repo, "diff", "--cached", "--name-only"),
		gittest.Git(t, repo, "for-each-ref", "refs/heads"),
	}, "\n")
}

func TestTakeLeavesRepoUntouched(t *testing.T) {
	repo, _ := gittest.NewRepo(t)
	gittest.WriteFile(t, repo, ".gitignore", "*.log\n")
	gittest.Git(t, repo, "add", ".gitignore")
	gittest.Git(t, repo, "commit", "-q", "-m", "ignore logs")

	gittest.WriteFile(t, repo, "README.md", "edited\n")  // tracked, modified
	gittest.WriteFile(t, repo, "staged.txt", "staged\n") // staged
	gittest.Git(t, repo, "add", "staged.txt")
	gittest.WriteFile(t, repo, "src/new.go", "package x\n") // untracked
	gittest.WriteFile(t, repo, "debug.log", "noise\n")      // ignored
	before := repoState(t, repo)

	msg := take(t, repo, Options{}, t0)
	if !strings.HasPrefix(msg, "snapshot ") || !strings.Contains(msg, "(3 files changed)") {
		t.Errorf("message = %q", msg)
	}
	if after := repoState(t, repo); after != before {
		t.Errorf("repository changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	ref := Ref("main")
	for file, want := range map[string]string{"README.md": "edited\n", "staged.txt": "staged\n", "src/new.go": "package x\n"} {
		if got := gittest.Git(t, repo, "show", ref+":"+file); got != strings.TrimSpace(want) {
			t.Errorf("%s in snapshot = %q, want %q", file, got, want)
		}
	}
	if _, err := git.Run(ctx, repo, "cat-file", "-e", ref+":debug.log"); err == nil {
		t.Error("ignored file was included in the snapshot")
	}
	if branches := gittest.Git(t, repo, "branch", "--format=%(refname:short)"); branches != "main" {
		t.Errorf("branches = %q, snapshots must not create branches", branches)
	}
}

func TestTakeSkipsWhenNothingChanged(t *testing.T) {
	repo, _ := gittest.NewRepo(t)
	if msg := take(t, repo, Options{}, t0); msg != "no changes" {
		t.Errorf("clean repo: %q", msg)
	}
	if git.Resolve(ctx, repo, Ref("main")) != "" {
		t.Error("a snapshot was created for a clean repo")
	}

	gittest.WriteFile(t, repo, "a.txt", "a")
	take(t, repo, Options{}, t0)
	if msg := take(t, repo, Options{}, t0.Add(time.Minute)); msg != "no changes" {
		t.Errorf("unchanged since last snapshot: %q", msg)
	}

	// Once the work is committed there is nothing left to protect.
	gittest.Git(t, repo, "add", "-A")
	gittest.Git(t, repo, "commit", "-q", "-m", "work")
	if msg := take(t, repo, Options{}, t0.Add(2*time.Minute)); msg != "no changes" {
		t.Errorf("after commit: %q", msg)
	}
}

func TestListAndFind(t *testing.T) {
	repo, _ := gittest.NewRepo(t)
	for i, content := range []string{"one", "two", "three"} {
		gittest.WriteFile(t, repo, "a.txt", content)
		take(t, repo, Options{}, t0.Add(time.Duration(i)*time.Hour))
	}
	gittest.WriteFile(t, repo, "b.txt", "b")
	take(t, repo, Options{}, t0.Add(3*time.Hour))

	snaps, err := List(ctx, repo, "main", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 4 {
		t.Fatalf("got %d snapshots, want 4", len(snaps))
	}
	if !snaps[0].Time.Equal(t0.Add(3*time.Hour)) || snaps[0].Files != 2 || snaps[3].Files != 1 {
		t.Errorf("newest = %+v, oldest = %+v", snaps[0], snaps[3])
	}
	if limited, _ := List(ctx, repo, "main", 2); len(limited) != 2 {
		t.Errorf("limit 2: got %d", len(limited))
	}

	now := t0.Add(3*time.Hour + 10*time.Minute)
	// Snapshots are at t0+3h, +2h, +1h and t0. At +3h10m, the newest one at
	// least 90 minutes old is the one from t0+1h.
	got, err := Find(ctx, repo, "main", "90m", now)
	if err != nil || got.ID != snaps[2].ID {
		t.Errorf("Find(90m) = %+v, %v; want %s", got, err, snaps[2].Short())
	}
	if got, err := Find(ctx, repo, "main", snaps[1].Short(), now); err != nil || got.ID != snaps[1].ID {
		t.Errorf("Find(id) = %+v, %v", got, err)
	}
	if got, err := Find(ctx, repo, "main", "latest", now); err != nil || got.ID != snaps[0].ID {
		t.Errorf("Find(latest) = %+v, %v", got, err)
	}
	if got, err := Find(ctx, repo, "main", "0d", now); err != nil || got.ID != snaps[0].ID {
		t.Errorf("Find(0d) = %+v, %v", got, err)
	}
	if _, err := Find(ctx, repo, "main", "1d", now); err == nil {
		t.Error("Find(1d): expected an error, no snapshot is that old")
	}
	if _, err := Find(ctx, repo, "main", "10h", now); err == nil {
		t.Error("Find(10h): expected an error, no snapshot is that old")
	}
	if _, err := Find(ctx, repo, "main", "zzzz", now); err == nil {
		t.Error("Find(unknown id): expected an error")
	}
}

func TestTakeWorksMidMergeAndDetached(t *testing.T) {
	repo, _ := gittest.NewRepo(t)
	gittest.StartMerge(t, repo)
	gitDir, _ := git.Dir(ctx, repo)

	if msg := take(t, repo, Options{}, t0); !strings.HasPrefix(msg, "snapshot ") {
		t.Fatalf("mid-merge: %q", msg)
	}
	if _, err := os.Stat(filepath.Join(gitDir, "MERGE_HEAD")); err != nil {
		t.Error("the merge was disturbed")
	}
	if conflicted := gittest.Git(t, repo, "diff", "--name-only", "--diff-filter=U"); conflicted != "README.md" {
		t.Errorf("conflict state changed: %q", conflicted)
	}

	gittest.Git(t, repo, "merge", "--abort")
	gittest.Git(t, repo, "checkout", "-q", "--detach")
	gittest.WriteFile(t, repo, "a.txt", "a")
	take(t, repo, Options{}, t0)
	if git.Resolve(ctx, repo, Ref("detached")) == "" {
		t.Error("no snapshot ref for detached HEAD")
	}
}

func TestTakeInRepoWithoutCommits(t *testing.T) {
	repo := gittest.NewLocalRepo(t, filepath.Join(t.TempDir(), "fresh"))
	if msg := take(t, repo, Options{}, t0); msg != "no changes" {
		t.Errorf("empty repository: %q", msg)
	}
	gittest.WriteFile(t, repo, "a.txt", "a")
	if msg := take(t, repo, Options{}, t0); !strings.Contains(msg, "(1 file changed)") {
		t.Errorf("message = %q", msg)
	}
}

func TestTakeBranchLock(t *testing.T) {
	repo, _ := gittest.NewRepo(t)
	gittest.Git(t, repo, "checkout", "-q", "-b", "feature")
	gittest.WriteFile(t, repo, "a.txt", "a")
	if msg := take(t, repo, Options{Branch: "main"}, t0); !strings.HasPrefix(msg, "skipped:") {
		t.Errorf("message = %q", msg)
	}
}

func TestTakePushesToHiddenRef(t *testing.T) {
	repo, remote := gittest.NewRepo(t)
	gittest.WriteFile(t, repo, "a.txt", "a")

	if msg := take(t, repo, Options{Push: true}, t0); !strings.HasSuffix(msg, ", pushed") {
		t.Fatalf("message = %q", msg)
	}
	if got, want := git.Resolve(ctx, remote, Ref("main")), git.Resolve(ctx, repo, Ref("main")); got == "" || got != want {
		t.Errorf("remote snapshot ref = %q, want %q", got, want)
	}
	if branches := gittest.Git(t, remote, "branch", "--format=%(refname:short)"); branches != "main" {
		t.Errorf("remote branches = %q, snapshots must not appear as branches", branches)
	}

	// A failed push is retried on the next run, even with nothing new to save.
	gittest.Git(t, repo, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	gittest.WriteFile(t, repo, "a.txt", "changed")
	if _, err := Take(ctx, repo, Options{Push: true}, t0.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "push failed") {
		t.Fatalf("got %v, want a push failure", err)
	}
	gittest.Git(t, repo, "remote", "set-url", "origin", remote)
	if msg := take(t, repo, Options{Push: true}, t0.Add(2*time.Minute)); msg != "pushed pending snapshot" {
		t.Errorf("retry: %q", msg)
	}
	if msg := take(t, repo, Options{Push: true}, t0.Add(3*time.Minute)); msg != "no changes" {
		t.Errorf("nothing left to push: %q", msg)
	}
}

func TestRestore(t *testing.T) {
	repo, _ := gittest.NewRepo(t)
	gittest.WriteFile(t, repo, "a.txt", "good")
	gittest.WriteFile(t, repo, "b.txt", "good")
	take(t, repo, Options{}, t0)
	good, err := Find(ctx, repo, "main", "0s", t0)
	if err != nil {
		t.Fatal(err)
	}

	// Something destroys the work.
	gittest.WriteFile(t, repo, "a.txt", "broken")
	gittest.WriteFile(t, repo, "b.txt", "broken")

	backup, err := Restore(ctx, repo, good, []string{filepath.Join(repo, "a.txt")}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got := gittest.ReadFile(t, repo, "a.txt"); got != "good" {
		t.Errorf("a.txt = %q, want good", got)
	}
	if got := gittest.ReadFile(t, repo, "b.txt"); got != "broken" {
		t.Errorf("b.txt = %q, only a.txt should be restored", got)
	}
	if backup == nil || gittest.Git(t, repo, "show", backup.ID+":a.txt") != "broken" {
		t.Fatal("the state before the restore was not saved")
	}

	// The whole tree, and undoing a restore with its backup.
	if _, err := Restore(ctx, repo, good, nil, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if gittest.ReadFile(t, repo, "b.txt") != "good" {
		t.Error("whole-tree restore did not restore b.txt")
	}
	if _, err := Restore(ctx, repo, *backup, nil, t0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if gittest.ReadFile(t, repo, "b.txt") != "broken" {
		t.Error("restoring the backup did not undo the restore")
	}
	if staged := gittest.Git(t, repo, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("restore changed the index: %q", staged)
	}
}

func TestTakeKeepsItsIndex(t *testing.T) {
	repo, _ := gittest.NewRepo(t)
	gitDir, _ := git.Dir(ctx, repo)

	// Same size and modification time, so only the content tells these apart
	// from what comi's index remembers. git rechecks the content because the
	// last snapshot was taken in the same second the file was written.
	mtime := time.Now().Add(-time.Minute).Truncate(time.Second)
	for i, content := range []string{"aaaa", "bbbb", "cccc"} {
		gittest.WriteFile(t, repo, "a.txt", content)
		os.Chtimes(filepath.Join(repo, "a.txt"), mtime, mtime)
		if i > 0 {
			os.Chtimes(filepath.Join(gitDir, indexName), mtime, mtime)
		}
		if msg := take(t, repo, Options{}, t0.Add(time.Duration(i)*time.Minute)); !strings.HasPrefix(msg, "snapshot ") {
			t.Fatalf("change %d: %q", i, msg)
		}
		if got := gittest.Git(t, repo, "show", Ref("main")+":a.txt"); got != content {
			t.Errorf("change %d: snapshot has %q, want %q", i, got, content)
		}
	}

	if _, err := os.Stat(filepath.Join(gitDir, indexName)); err != nil {
		t.Errorf("comi's index was not kept: %v", err)
	}
	entries, _ := os.ReadDir(gitDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), indexName+"-") {
			t.Errorf("temporary index %s left behind", e.Name())
		}
	}
}

func TestTakeSeesDeletesAndNewFolders(t *testing.T) {
	repo, _ := gittest.NewRepo(t)
	gittest.WriteFile(t, repo, "a.txt", "a")
	take(t, repo, Options{}, t0)

	os.Remove(filepath.Join(repo, "a.txt"))
	os.Remove(filepath.Join(repo, "README.md"))
	gittest.WriteFile(t, repo, "new/deep/b.txt", "b")
	take(t, repo, Options{}, t0.Add(time.Minute))
	if files := gittest.Git(t, repo, "ls-tree", "-r", "--name-only", Ref("main")); files != "new/deep/b.txt" {
		t.Errorf("snapshot has %q, want only new/deep/b.txt", files)
	}
}

// inLatest reports whether the newest snapshot of main has file.
func inLatest(repo, file string) bool {
	_, err := git.Run(ctx, repo, "cat-file", "-e", Ref("main")+":"+file)
	return err == nil
}

func TestTakeLeavesOutLargeFiles(t *testing.T) {
	defer func(old int64) { maxFileSize = old }(maxFileSize)
	maxFileSize = 10
	repo, _ := gittest.NewRepo(t)

	gittest.WriteFile(t, repo, "big.bin", "more than ten bytes")
	gittest.WriteFile(t, repo, "grows.txt", "small")
	msg := take(t, repo, Options{}, t0)
	if !strings.HasPrefix(msg, "snapshot ") || !strings.HasSuffix(msg, "left out big.bin (over 10 bytes)") {
		t.Errorf("message = %q", msg)
	}
	if inLatest(repo, "big.bin") || !inLatest(repo, "grows.txt") {
		t.Error("want grows.txt saved and big.bin left out")
	}
	// Reported again while it is there, even with nothing else to save.
	if msg := take(t, repo, Options{}, t0.Add(time.Minute)); msg != "no changes, left out big.bin (over 10 bytes)" {
		t.Errorf("message = %q", msg)
	}

	// A saved file that grows too large is dropped, not kept at its old size.
	gittest.WriteFile(t, repo, "grows.txt", "now more than ten bytes")
	gittest.WriteFile(t, repo, "c.txt", "c")
	msg = take(t, repo, Options{}, t0.Add(2*time.Minute))
	if !strings.Contains(msg, "left out 2 files over 10 bytes (big.bin, grows.txt)") || inLatest(repo, "grows.txt") {
		t.Errorf("grown file: message = %q, saved = %v", msg, inLatest(repo, "grows.txt"))
	}

	// Large files the user tracks are saved: a restore would delete them otherwise.
	gittest.Git(t, repo, "add", "big.bin")
	msg = take(t, repo, Options{}, t0.Add(3*time.Minute))
	if !inLatest(repo, "big.bin") || strings.Contains(msg, "big.bin") {
		t.Errorf("tracked large file: message = %q, saved = %v", msg, inLatest(repo, "big.bin"))
	}
}

func TestTakeFollowsIgnoreChanges(t *testing.T) {
	repo, _ := gittest.NewRepo(t)
	saved := func(file string) bool {
		_, err := git.Run(ctx, repo, "cat-file", "-e", Ref("main")+":"+file)
		return err == nil
	}

	gittest.WriteFile(t, repo, "secret.env", "v1")
	take(t, repo, Options{}, t0)
	if !saved("secret.env") {
		t.Fatal("untracked file was not saved")
	}

	// Ignored after it was saved: left out from now on.
	gittest.WriteFile(t, repo, ".gitignore", "*.env\n*.log\n")
	gittest.WriteFile(t, repo, "secret.env", "v2")
	take(t, repo, Options{}, t0.Add(time.Minute))
	if saved("secret.env") {
		t.Error("a file ignored since the last snapshot is still saved")
	}

	// Ignored, but tracked anyway: saved, as `git add -A` would.
	gittest.WriteFile(t, repo, "keep.log", "log")
	gittest.Git(t, repo, "add", "-f", "keep.log")
	gittest.WriteFile(t, repo, "b.txt", "b")
	take(t, repo, Options{}, t0.Add(2*time.Minute))
	if !saved("keep.log") || !saved("b.txt") {
		t.Error("an ignored file tracked with git add -f was left out")
	}
	if saved("secret.env") {
		t.Error("secret.env came back")
	}
}
