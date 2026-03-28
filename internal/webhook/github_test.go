package webhook

import (
	"testing"

	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestPushChangedFilesKnownAndCombined(t *testing.T) {
	t.Parallel()

	push := pushEvent{
		Commits: []pushCommit{
			{
				Added:    []string{"web/landing/src/new.tsx"},
				Modified: []string{"README.md"},
				Removed:  []string{"web/landing/src/old.tsx"},
			},
			{
				Modified: []string{"web/landing/package.json"},
			},
		},
	}

	files, ok := pushChangedFiles(push)
	if !ok {
		t.Fatalf("expected changed files to be known")
	}

	want := []string{
		"web/landing/src/new.tsx",
		"README.md",
		"web/landing/src/old.tsx",
		"web/landing/package.json",
	}
	if len(files) != len(want) {
		t.Fatalf("got %d files, want %d: %#v", len(files), len(want), files)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Fatalf("file %d = %q, want %q", i, files[i], want[i])
		}
	}
}

func TestPushChangedFilesUnknownWithoutCommitFileLists(t *testing.T) {
	t.Parallel()

	push := pushEvent{
		Commits: []pushCommit{{}},
	}

	files, ok := pushChangedFiles(push)
	if ok {
		t.Fatalf("expected changed files to be unknown, got %#v", files)
	}
}

func TestPushChangedFilesUnknownWhenCommitListMayBeTruncated(t *testing.T) {
	t.Parallel()

	commits := make([]pushCommit, 2048)
	for i := range commits {
		commits[i] = pushCommit{Modified: []string{"docs/notes.md"}}
	}

	files, ok := pushChangedFiles(pushEvent{Commits: commits})
	if ok {
		t.Fatalf("expected changed files to be treated as ambiguous, got %#v", files)
	}
}

func TestRootDirectoryMatchesChangedFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rootDir string
		files   []string
		want    bool
	}{
		{
			name:    "repo root matches any file",
			rootDir: "/",
			files:   []string{"README.md"},
			want:    true,
		},
		{
			name:    "nested root matches file in subtree",
			rootDir: "/web/landing",
			files:   []string{"web/landing/src/App.tsx"},
			want:    true,
		},
		{
			name:    "nested root matches directory root file",
			rootDir: "/web/landing",
			files:   []string{"web/landing/package.json"},
			want:    true,
		},
		{
			name:    "nested root ignores sibling path prefix",
			rootDir: "/web/landing",
			files:   []string{"web/landingish/src/App.tsx"},
			want:    false,
		},
		{
			name:    "nested root ignores unrelated file",
			rootDir: "/web/landing",
			files:   []string{"web/dashboard/src/App.tsx"},
			want:    false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := rootDirectoryMatchesChangedFiles(tt.rootDir, tt.files)
			if got != tt.want {
				t.Fatalf("rootDirectoryMatchesChangedFiles(%q, %#v) = %v, want %v", tt.rootDir, tt.files, got, tt.want)
			}
		})
	}
}

func TestShouldCreateDeploymentForPush(t *testing.T) {
	t.Parallel()

	push := pushEvent{
		Commits: []pushCommit{
			{Modified: []string{"docs/notes.md"}},
		},
	}

	if !shouldCreateDeploymentForPush(queries.Project{
		RootDirectory:          "/web/landing",
		BuildOnlyOnRootChanges: false,
	}, push) {
		t.Fatalf("expected deploy when smart-build option is disabled")
	}

	if shouldCreateDeploymentForPush(queries.Project{
		RootDirectory:          "/web/landing",
		BuildOnlyOnRootChanges: true,
	}, push) {
		t.Fatalf("expected deploy to be skipped when only unrelated files changed")
	}

	if !shouldCreateDeploymentForPush(queries.Project{
		RootDirectory:          "/web/landing",
		BuildOnlyOnRootChanges: true,
	}, pushEvent{Commits: []pushCommit{{}}}) {
		t.Fatalf("expected deploy when changed files are ambiguous")
	}
}

func TestShouldCreateDeploymentForService(t *testing.T) {
	t.Parallel()

	push := pushEvent{
		Commits: []pushCommit{
			{Modified: []string{"services/api/server.ts", "docs/notes.md"}},
		},
	}

	if !shouldCreateDeploymentForService(queries.Service{
		RootDirectory:          "/services/api",
		BuildOnlyOnRootChanges: true,
	}, push) {
		t.Fatalf("expected service deploy when files changed under the service root")
	}

	if shouldCreateDeploymentForService(queries.Service{
		RootDirectory:          "/services/worker",
		BuildOnlyOnRootChanges: true,
	}, push) {
		t.Fatalf("expected service deploy to be skipped when files changed outside the service root")
	}

	if !shouldCreateDeploymentForService(queries.Service{
		RootDirectory:          "/services/worker",
		BuildOnlyOnRootChanges: false,
	}, push) {
		t.Fatalf("expected service deploy when smart-build is disabled")
	}
}
