// Package runner describes how each supported language is compiled and run
// inside the sandbox. Every language has a compile step that runs in the
// sandbox: gcc/g++ for C and C++, and a syntax check (py_compile) for
// Python, so a Python syntax error is a Compilation error like in C.
package runner

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/kanonkc/verdale-judge-service/configs"
	"github.com/kanonkc/verdale-judge-service/pkg/contract"
	"github.com/kanonkc/verdale-judge-service/pkg/sandbox"
)

type Language struct {
	Code contract.Language
	// SourceFile is the file name the source is written to inside /box.
	SourceFile string
	// Artifact is the file carried from the compile box to every run box.
	Artifact string
	// ArtifactMode is the permission bits of the artifact in the run box.
	ArtifactMode uint32
	CompileArgs  []string
	RunArgs      []string
	// RunProcesses is how many processes/threads a solution may use.
	RunProcesses int
	// versionArgs prints the toolchain version; ExpectedVersion pins it.
	versionArgs     []string
	parseVersion    func(output string) string
	ExpectedVersion string
}

// Languages builds the language table from configuration.
func Languages(config configs.LanguagesConfig) map[contract.Language]Language {
	gccVersion := func(output string) string { return strings.TrimSpace(output) }
	return map[contract.Language]Language{
		contract.LanguageC: {
			Code:            contract.LanguageC,
			SourceFile:      "main.c",
			Artifact:        "main",
			ArtifactMode:    0o755,
			CompileArgs:     concat([]string{config.C.Binary}, config.C.Flags, []string{"-o", "main", "main.c", "-lm"}),
			RunArgs:         []string{"./main"},
			RunProcesses:    1,
			versionArgs:     []string{config.C.Binary, "-dumpfullversion"},
			parseVersion:    gccVersion,
			ExpectedVersion: config.C.ExpectedVersion,
		},
		contract.LanguageCpp: {
			Code:            contract.LanguageCpp,
			SourceFile:      "main.cpp",
			Artifact:        "main",
			ArtifactMode:    0o755,
			CompileArgs:     concat([]string{config.Cpp.Binary}, config.Cpp.Flags, []string{"-o", "main", "main.cpp"}),
			RunArgs:         []string{"./main"},
			RunProcesses:    1,
			versionArgs:     []string{config.Cpp.Binary, "-dumpfullversion"},
			parseVersion:    gccVersion,
			ExpectedVersion: config.Cpp.ExpectedVersion,
		},
		contract.LanguagePython: {
			Code:         contract.LanguagePython,
			SourceFile:   "main.py",
			Artifact:     "main.py",
			ArtifactMode: 0o644,
			CompileArgs:  []string{config.Python.Binary, "-m", "py_compile", "main.py"},
			RunArgs:      concat([]string{config.Python.Binary}, config.Python.Flags, []string{"main.py"}),
			RunProcesses: 1,
			versionArgs:  []string{config.Python.Binary, "--version"},
			parseVersion: func(output string) string {
				return strings.TrimPrefix(strings.TrimSpace(output), "Python ")
			},
			ExpectedVersion: config.Python.ExpectedVersion,
		},
	}
}

// CheckVersions makes Judge refuse to start when an installed toolchain is
// not the pinned version, so results stay reproducible across machines.
func CheckVersions(ctx context.Context, languages map[contract.Language]Language, run sandbox.CommandRunner) error {
	for code, language := range languages {
		stdout, stderr, exitCode, err := run(ctx, language.versionArgs[0], language.versionArgs[1:]...)
		if err != nil || exitCode != 0 {
			return fmt.Errorf("%s toolchain %s is not runnable: %v %s", code, language.versionArgs[0], err, strings.TrimSpace(string(stderr)))
		}
		// Older Pythons print the version to stderr.
		installed := language.parseVersion(string(stdout) + string(stderr))
		if installed != language.ExpectedVersion {
			return fmt.Errorf("%s toolchain %s is version %q, but languages.%s.expected_version pins %q", code, language.versionArgs[0], installed, code, language.ExpectedVersion)
		}
	}
	return nil
}

// systemPath matches absolute paths outside the box, e.g. system headers in
// compiler messages, so the message does not reveal the host layout.
var systemPath = regexp.MustCompile(`/(?:usr|lib|lib64|tmp|var|etc|home|root|opt|run|proc|sys)(?:/[^\s:'"()<>]*)?`)

const truncationNotice = "\n[output truncated]"

// SanitizeCompilerOutput removes host paths, makes the text valid UTF-8 and
// truncates it to limit bytes.
func SanitizeCompilerOutput(raw []byte, limit int) string {
	text := strings.ToValidUTF8(string(raw), "�")
	text = strings.ReplaceAll(text, "/box/", "")
	text = systemPath.ReplaceAllString(text, "<system>")
	if len(text) <= limit {
		return text
	}
	return TruncateUTF8(text, limit-len(truncationNotice)) + truncationNotice
}

// TruncateUTF8 cuts text to at most limit bytes without splitting a rune.
func TruncateUTF8(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

func concat(parts ...[]string) []string {
	var joined []string
	for _, part := range parts {
		joined = append(joined, part...)
	}
	return joined
}
