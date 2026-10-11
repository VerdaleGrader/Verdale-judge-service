package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kanonkc/verdale-judge-service/configs"
	"github.com/kanonkc/verdale-judge-service/pkg/contract"
)

func testLanguages() map[contract.Language]Language {
	return Languages(configs.LanguagesConfig{
		C:      configs.LanguageConfig{Binary: "/usr/bin/gcc", Flags: []string{"-std=c17", "-O2"}, ExpectedVersion: "12.2.0"},
		Cpp:    configs.LanguageConfig{Binary: "/usr/bin/g++", Flags: []string{"-std=c++17", "-O2"}, ExpectedVersion: "12.2.0"},
		Python: configs.LanguageConfig{Binary: "/usr/bin/python3", Flags: []string{"-B"}, ExpectedVersion: "3.11.2"},
	})
}

func TestLanguageCommands(t *testing.T) {
	languages := testLanguages()
	assert.Equal(t, "/usr/bin/gcc -std=c17 -O2 -o main main.c -lm", strings.Join(languages[contract.LanguageC].CompileArgs, " "))
	assert.Equal(t, "/usr/bin/g++ -std=c++17 -O2 -o main main.cpp", strings.Join(languages[contract.LanguageCpp].CompileArgs, " "))
	assert.Equal(t, "/usr/bin/python3 -m py_compile main.py", strings.Join(languages[contract.LanguagePython].CompileArgs, " "))
	assert.Equal(t, "/usr/bin/python3 -B main.py", strings.Join(languages[contract.LanguagePython].RunArgs, " "))
	assert.Len(t, languages, 3, "c, cpp and python are the MVP languages")
}

func TestCheckVersions(t *testing.T) {
	installed := map[string]string{"/usr/bin/gcc": "12.2.0\n", "/usr/bin/g++": "12.2.0\n", "/usr/bin/python3": "Python 3.11.2\n"}
	fakeRun := func(_ context.Context, name string, _ ...string) ([]byte, []byte, int, error) {
		return []byte(installed[name]), nil, 0, nil
	}
	require.NoError(t, CheckVersions(context.Background(), testLanguages(), fakeRun))

	installed["/usr/bin/python3"] = "Python 3.12.1\n"
	err := CheckVersions(context.Background(), testLanguages(), fakeRun)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `version "3.12.1"`)
}

func TestSanitizeCompilerOutput(t *testing.T) {
	raw := "main.cpp: In function 'int main()':\nmain.cpp:3:5: error: 'x' was not declared in this scope\n" +
		"/usr/include/c++/12/bits/stl_vector.h:10: note: candidate\n/box/main.cpp:4:1: error: expected ';'\n" +
		"/var/local/lib/isolate/101/box/main.cpp: fatal\n"
	sanitized := SanitizeCompilerOutput([]byte(raw), 8192)
	assert.Contains(t, sanitized, "main.cpp:3:5: error")
	assert.Contains(t, sanitized, "<system>:10: note")
	assert.Contains(t, sanitized, "main.cpp:4:1: error")
	assert.NotContains(t, sanitized, "/usr/")
	assert.NotContains(t, sanitized, "isolate")

	long := strings.Repeat("ข้อผิดพลาด ", 2000)
	truncated := SanitizeCompilerOutput([]byte(long), 1000)
	assert.LessOrEqual(t, len(truncated), 1000)
	assert.True(t, strings.HasSuffix(truncated, "[output truncated]"))
	assert.True(t, strings.ToValidUTF8(truncated, "") == truncated, "never split a UTF-8 character")
}
