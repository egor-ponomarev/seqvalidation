package seqvalidation_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCommonFieldTypeIsEnforcedByCompiler(t *testing.T) {
	fixtures := []struct {
		name        string
		declaration string
		wantFailure bool
	}{
		{
			name: "same_field_type",
			declaration: `var _ = seqvalidation.Sequence(
    seqvalidation.Group(seqvalidation.Field(fieldA("first"), checkA), seqvalidation.Field(fieldA("second"), checkA)),
    seqvalidation.Group(seqvalidation.Field(fieldA("third"), checkA)),
)`,
		},
		{
			name:        "different_fields_in_group",
			declaration: `var _ = seqvalidation.Group(seqvalidation.Field(fieldA("first"), checkA), seqvalidation.Field(fieldB("second"), checkB))`,
			wantFailure: true,
		},
		{
			name: "different_fields_between_groups",
			declaration: `var _ = seqvalidation.Sequence(
    seqvalidation.Group(seqvalidation.Field(fieldA("first"), checkA)),
    seqvalidation.Group(seqvalidation.Field(fieldB("second"), checkB)),
)`,
			wantFailure: true,
		},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			// Arrange
			root, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			module := "module example.invalid/fieldcheck\n\ngo 1.22\n\nrequire github.com/egor-ponomarev/seqvalidation v0.0.0\n\nreplace github.com/egor-ponomarev/seqvalidation => " + strconv.Quote(filepath.ToSlash(root)) + "\n"
			source := `package fieldcheck
import (
    "context"
    "github.com/egor-ponomarev/seqvalidation"
)
type state struct{}
type violation struct{}
type fieldA string
type fieldB string
func checkA(context.Context, fieldA, *state) ([]*violation, error) { return nil, nil }
func checkB(context.Context, fieldB, *state) ([]*violation, error) { return nil, nil }
` + fixture.declaration + "\n"
			for name, contents := range map[string]string{"go.mod": module, "check.go": source} {
				if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command("go", "build", ".")
			command.Dir = directory
			command.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOFLAGS=")

			// Act
			output, err := command.CombinedOutput()

			// Assert
			if fixture.wantFailure {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || !strings.Contains(string(output), "fieldB") || !strings.Contains(string(output), "does not match") {
					t.Fatalf("expected incompatible field types, err=%v output=%s", err, output)
				}
			} else if err != nil {
				t.Fatalf("compatible fields must compile, err=%v output=%s", err, output)
			}
		})
	}
}
