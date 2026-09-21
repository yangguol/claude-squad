package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadPolicyClassifiesDefaultsAndOverrides(t *testing.T) {
	source := t.TempDir()
	contents := `# project policy

!dist/
node_modules/
[shared]
vendor/
[generated]
.generated/
`
	require.NoError(t, os.WriteFile(filepath.Join(source, ".claude-squad-ignore"), []byte(contents), 0o644))

	policy, err := LoadPolicy(source)
	require.NoError(t, err)

	require.Equal(t, ClassShared, policy.Classify(".venv"))
	require.Equal(t, ClassShared, policy.Classify(".venv/bin/python"))
	require.Equal(t, ClassShared, policy.Classify("vendor"))
	require.Equal(t, ClassGenerated, policy.Classify("build/output"))
	require.Equal(t, ClassGenerated, policy.Classify(".generated/cache"))
	require.Equal(t, ClassIncluded, policy.Classify("dist"), "project negation must override defaults")
	require.Equal(t, ClassExcluded, policy.Classify("node_modules"), "later project rules must override defaults and earlier project rules")
	require.Equal(t, ClassIncluded, policy.Classify("src/main.go"))
	require.Equal(t, ClassIncluded, policy.Classify(".claude-squad-ignore"))
}

func TestPolicyOrdinaryPatternsAndNegationPrecedence(t *testing.T) {
	source := t.TempDir()
	contents := "*.tmp\nlogs/\n!important.tmp\n"
	require.NoError(t, os.WriteFile(filepath.Join(source, ".claude-squad-ignore"), []byte(contents), 0o644))

	policy, err := LoadPolicy(source)
	require.NoError(t, err)

	require.Equal(t, ClassExcluded, policy.Classify("scratch.tmp"))
	require.Equal(t, ClassExcluded, policy.Classify("nested/scratch.tmp"))
	require.Equal(t, ClassExcluded, policy.Classify("logs/today.txt"))
	require.Equal(t, ClassIncluded, policy.Classify("important.tmp"))
}

func TestLoadPolicyRejectsMalformedSectionWithLineNumber(t *testing.T) {
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, ".claude-squad-ignore"), []byte("# comment\n[unknown]\nfoo\n"), 0o644))

	_, err := LoadPolicy(source)
	require.Error(t, err)
	require.Contains(t, err.Error(), "line 2")
	require.Contains(t, err.Error(), "[unknown]")
}

func TestLoadPolicyWithoutProjectFileUsesDefaults(t *testing.T) {
	policy, err := LoadPolicy(t.TempDir())
	require.NoError(t, err)
	require.Equal(t, ClassShared, policy.Classify(".venv/bin/python"))
	require.Equal(t, ClassGenerated, policy.Classify("src/__pycache__/module.pyc"))
	require.Equal(t, ClassGenerated, policy.Classify(".DS_Store"))
}
