package update

import "testing"

func TestParseVersion(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"1.2.3", "1.2.3", false},
		{"v1.2.3", "1.2.3", false},
		{"0.60.0", "0.60.0", false},
		{"v0.60.0", "0.60.0", false},
		{"1.0.0-beta", "1.0.0-beta", false},
		{"1.0.0-rc.1", "1.0.0-rc.1", false},
		{"1.0.0-alpha.1+build.123", "1.0.0-alpha.1+build.123", false},
		{"  v1.2.3  ", "1.2.3", false},

		// Invalid
		{"", "", true},
		{"abc", "", true},
		{"1.2", "", true},
		{"v1.2", "", true},
		{"latest", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			v, err := ParseVersion(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseVersion(%q) should error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseVersion(%q) error: %v", tt.input, err)
			}
			if got := v.String(); got != tt.want {
				t.Errorf("ParseVersion(%q).String() = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestVersionCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "2.0.0", -1},
		{"2.0.0", "1.0.0", 1},
		{"1.1.0", "1.0.0", 1},
		{"1.0.1", "1.0.0", 1},
		{"0.60.0", "0.59.4", 1},
		{"0.59.4", "0.60.0", -1},

		// Prerelease
		{"1.0.0", "1.0.0-beta", 1},       // release > prerelease
		{"1.0.0-beta", "1.0.0", -1},       // prerelease < release
		{"1.0.0-alpha", "1.0.0-beta", -1}, // alpha < beta (lexicographic)
		{"1.0.0-rc.1", "1.0.0-rc.2", -1},
		{"1.0.0-rc.2", "1.0.0-rc.1", 1},
	}

	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			a, _ := ParseVersion(tt.a)
			b, _ := ParseVersion(tt.b)
			if got := a.Compare(b); got != tt.want {
				t.Errorf("Compare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestVersionCompareNil(t *testing.T) {
	v, _ := ParseVersion("1.0.0")
	if v.Compare(nil) != 1 {
		t.Error("Compare(nil) should return 1")
	}
}

func TestVersionHelpers(t *testing.T) {
	a, _ := ParseVersion("1.0.0")
	b, _ := ParseVersion("2.0.0")

	if !a.LessThan(b) {
		t.Error("1.0.0 should be less than 2.0.0")
	}
	if !b.GreaterThan(a) {
		t.Error("2.0.0 should be greater than 1.0.0")
	}
	if !a.Equal(a) {
		t.Error("1.0.0 should equal itself")
	}
}

func TestVersionIsPrerelease(t *testing.T) {
	v, _ := ParseVersion("1.0.0")
	if v.IsPrerelease() {
		t.Error("1.0.0 should not be prerelease")
	}

	v, _ = ParseVersion("1.0.0-beta")
	if !v.IsPrerelease() {
		t.Error("1.0.0-beta should be prerelease")
	}
}

func TestIsNewerVersion(t *testing.T) {
	if !IsNewerVersion("v0.60.0", "v0.59.4") {
		t.Error("0.60.0 should be newer than 0.59.4")
	}
	if IsNewerVersion("v0.59.4", "v0.60.0") {
		t.Error("0.59.4 should not be newer than 0.60.0")
	}
	if IsNewerVersion("invalid", "v0.60.0") {
		t.Error("invalid should not be newer")
	}
}

func TestCompareVersionStrings(t *testing.T) {
	if CompareVersionStrings("v0.60.0", "v0.59.4") != 1 {
		t.Error("0.60.0 should be > 0.59.4")
	}
	if CompareVersionStrings("invalid", "v1.0.0") != 0 {
		t.Error("invalid comparison should return 0")
	}
}

func TestNormalizeVersion(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"v1.2.3", "1.2.3"},
		{"1.2.3", "1.2.3"},
		{"  v1.0.0  ", "1.0.0"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := NormalizeVersion(tt.input); got != tt.want {
			t.Errorf("NormalizeVersion(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
