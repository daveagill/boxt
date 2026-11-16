package workspace

import (
	"reflect"
	"testing"

	"github.com/daveagill/boxt/internal/config"
)

func TestSeedMatcher(t *testing.T) {
	tests := []struct {
		name    string
		include []string
		exclude []string
		path    string
		want    bool
	}{
		{"directory trailing slash matches descendants", []string{"node_modules/"}, nil, "node_modules/pkg/index.js", true},
		{"plain directory name matches descendants", []string{"node_modules"}, nil, "node_modules/pkg/index.js", true},
		{"plain directory name is root relative", []string{"node_modules"}, nil, "pkg/node_modules/index.js", false},
		{"plain nested path matches descendants", []string{"build/cache"}, nil, "build/cache/a/b", true},
		{"double star directory", []string{"node_modules/**"}, nil, "node_modules/.cache/x", true},
		{"double star matches zero segments", []string{"node_modules/**"}, nil, "node_modules", true},
		{"double star leading root file", []string{"**/*.log"}, nil, "a.log", true},
		{"double star leading nested file", []string{"**/*.log"}, nil, "x/y/a.log", true},
		{"double star leading does not match suffix", []string{"**/*.log"}, nil, "a.log.bak", false},
		{"double star middle zero segments", []string{"cache/**/tmp"}, nil, "cache/tmp", true},
		{"double star middle nested", []string{"cache/**/tmp"}, nil, "cache/a/b/tmp", true},
		{"double star middle does not match descendants", []string{"cache/**/tmp"}, nil, "cache/a/b/tmp/file", false},
		{"basename glob root dotfile", []string{"*.env"}, nil, ".env", true},
		{"basename glob matches suffix", []string{"*.env"}, nil, "app.env", true},
		{"basename glob is root relative", []string{"*.env"}, nil, "sub/.env", false},
		{"basename glob does not match longer suffix", []string{"*.env"}, nil, ".env.local", false},
		{"character class matches", []string{"file[0-9].txt"}, nil, "file7.txt", true},
		{"character class miss", []string{"file[0-9].txt"}, nil, "filex.txt", false},
		{"question mark matches one character", []string{"foo?bar"}, nil, "fooXbar", true},
		{"question mark does not match slash", []string{"foo?bar"}, nil, "foo/bar", false},
		{"include is required when set", []string{"src/**"}, nil, "README.md", false},
		{"exclude wins over include", []string{"**/*.log"}, []string{"**/debug.log"}, "sub/debug.log", false},
		{"exclude leaves other includes", []string{"**/*.log"}, []string{"**/debug.log"}, "sub/app.log", true},
		{"exclude directory blocks descendants", nil, []string{"node_modules/"}, "node_modules/pkg/index.js", false},
		{"empty includes allow all", nil, nil, "any/path.dat", true},
		{"empty includes still honor exclude", nil, []string{"**/*.tmp"}, "cache/x.tmp", false},
		{"empty includes leave other paths", nil, []string{"**/*.tmp"}, "cache/x.log", true},
		{"nested include", []string{"node_modules/**"}, []string{"node_modules/.cache/**"}, "node_modules/pkg/a.js", true},
		{"nested exclude", []string{"node_modules/**"}, []string{"node_modules/.cache/**"}, "node_modules/.cache/tmp", false},
		{"hidden file glob", []string{"**/.env"}, nil, "a/.env", true},
		{"trailing slash wildcard matches descendants", []string{"cache/*/"}, nil, "cache/a/b/c", true},
		{"wildcard pattern is not a directory prefix", []string{"*.env"}, nil, "app.env/x", false},
		{"parent traversal never matches", nil, nil, "../outside", false},
		{"absolute path never matches", nil, nil, "/etc/passwd", false},
		{"empty path never matches", nil, nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := newSeedMatcher(config.SeedConfig{Include: tt.include, Exclude: tt.exclude})
			if err != nil {
				t.Fatalf("newSeedMatcher: %v", err)
			}
			if got := m.Match(tt.path); got != tt.want {
				t.Fatalf("Match(%q) = %v, want %v (include %v, exclude %v)", tt.path, got, tt.want, tt.include, tt.exclude)
			}
		})
	}
}

func TestSeedMatcherUnmatchedPreservesOrder(t *testing.T) {
	m, err := newSeedMatcher(config.SeedConfig{
		Include: []string{".env", "node_modules/**"},
		Exclude: []string{"node_modules/.cache/**"},
	})
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"node_modules/pkg/index.js", ".env", "cache/data", "node_modules/.cache/tmp"}
	want := []string{"cache/data", "node_modules/.cache/tmp"}
	if got := m.Unmatched(paths); !reflect.DeepEqual(got, want) {
		t.Fatalf("Unmatched = %#v, want %#v", got, want)
	}
}

func TestSeedMatcherRejectsInvalidPatterns(t *testing.T) {
	for _, rules := range []config.SeedConfig{
		{Include: []string{"["}},
		{Include: []string{"../outside"}},
		{Exclude: []string{"/absolute"}},
	} {
		if _, err := newSeedMatcher(rules); err == nil {
			t.Fatalf("newSeedMatcher(%#v) unexpectedly succeeded", rules)
		}
	}
}
