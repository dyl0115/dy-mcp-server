package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 1. 시스템 환경 정보

type EnvInfoInput struct {
}

type EnvInfoOutput struct {
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	WorkingDir string `json:"workingDir"`
	Username   string `json:"username"`
}

// 2. 터미널 명령어 실행

type CommandInput struct {
	Command string `json:"command" jsonschema:"The full shell command to execute (e.g., 'ls -la' or 'git status')"`
	Timeout int    `json:"timeout,omitempty" jsonschema:"Maximum execution time in seconds (default: 30)"`
}

type CommandOutput struct {
	Result string `json:"result"`
}

// 3. 파일 읽기

type ReadFileInput struct {
	FilePath        string `json:"filePath" jsonschema:"Path to the file to be read"`
	WithLineNumbers bool   `json:"withLineNumbers" jsonschema:"If true, adds line numbers to output (e.g., '1: content'). Useful for precise editing"`
}

type ReadFileOutput struct {
	Content string `json:"content"`
}

// 4. 파일 쓰기

type WriteFileInput struct {
	FilePath  string `json:"filePath" jsonschema:"Path where the file will be created or updated"`
	Content   string `json:"content" jsonschema:"The full content to write into the file"`
	Overwrite bool   `json:"overwrite" jsonschema:"If true, replaces existing file. If false, fails if file exists"`
}

type WriteFileOutput struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// 5. 파일/폴더 삭제

type DeleteFileInput struct {
	FilePath string `json:"filePath" jsonschema:"Path to the file or directory to delete. Directories are deleted recursively"`
}

type DeleteFileOutput struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// 6. 폴더 목록 조회

type ListDirectoryInput struct {
	DirectoryPath string `json:"directoryPath" jsonschema:"Path to the directory to list (e.g., '.', './src')"`
}

type FileInfo struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

type ListDirectoryOutput struct {
	Files []FileInfo `json:"files"`
}

// 7. 정밀 라인 수정

type ReplaceLinesInput struct {
	FilePath   string `json:"filePath" jsonschema:"Path to the file to edit"`
	StartLine  int    `json:"startLine" jsonschema:"Starting line number to replace (1-indexed, inclusive)"`
	EndLine    int    `json:"endLine" jsonschema:"Ending line number to replace (inclusive)"`
	NewContent string `json:"newContent" jsonschema:"New text content to insert into the specified line range"`
}

type ReplaceLinesOutput struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// ─── EnvInfo 환경 조회 ───────────────────────────────────────────────

func GetEnvInfo(ctx context.Context, req *mcp.CallToolRequest, input EnvInfoInput) (*mcp.CallToolResult, EnvInfoOutput, error) {
	wd, _ := os.Getwd()
	currentUser, _ := user.Current()

	info := EnvInfoOutput{
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		WorkingDir: wd,
		Username:   currentUser.Username,
	}

	resultText := fmt.Sprintf("OS: %s\nArch: %s\nWorking Dir: %s\nUser: %s",
		info.OS, info.Arch, info.WorkingDir, info.Username)

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: resultText}},
	}, info, nil
}

// ─── CLI exec ───────────────────────────────────────────────

func ExecCli(ctx context.Context, req *mcp.CallToolRequest, input CommandInput) (
	*mcp.CallToolResult, CommandOutput, error) {

	trimmedCmd := strings.TrimSpace(input.Command)
	if trimmedCmd == "" {
		return nil, CommandOutput{Result: "error: empty command"}, nil
	}

	forbiddenKeywords := []string{"rm -rf /", "mkfs", "dd if=", ":(){ :|:& };:"}
	for _, word := range forbiddenKeywords {
		if strings.Contains(trimmedCmd, word) {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "security error: this command is too dangerous to execute"}},
			}, CommandOutput{Result: "blocked"}, nil
		}
	}

	if input.Timeout <= 0 {
		input.Timeout = 30
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(input.Timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", trimmedCmd)
	cmd.Env = os.Environ()

	out, err := cmd.CombinedOutput()

	const maxOutput = 100 * 1024
	resultStr := string(out)
	if len(resultStr) > maxOutput {
		resultStr = resultStr[:maxOutput] + "\n... [output truncated due to size limit]"
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			errMsg := fmt.Sprintf("command timed out after %ds", input.Timeout)
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: errMsg}},
			}, CommandOutput{Result: errMsg}, nil
		}

		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: resultStr}},
		}, CommandOutput{Result: resultStr}, nil
	}

	return &mcp.CallToolResult{
		IsError: false,
		Content: []mcp.Content{&mcp.TextContent{Text: resultStr}},
	}, CommandOutput{Result: resultStr}, nil
}

// ─── Filesystem: 파일 읽기 ───────────────────────────────────

func ReadFile(ctx context.Context, req *mcp.CallToolRequest, input ReadFileInput) (
	*mcp.CallToolResult, ReadFileOutput, error) {

	cleanPath := filepath.Clean(input.FilePath)

	info, err := os.Stat(cleanPath)
	if err != nil {
		return nil, ReadFileOutput{}, fmt.Errorf("file not found: %w", err)
	}

	const maxFileSize = 1 * 1024 * 1024
	if info.Size() > maxFileSize {
		return nil, ReadFileOutput{}, fmt.Errorf("file too large (%d bytes)", info.Size())
	}

	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, ReadFileOutput{}, fmt.Errorf("read failed: %w", err)
	}

	checkLen := len(data)
	if checkLen > 512 {
		checkLen = 512
	}
	if !utf8.Valid(data[:checkLen]) {
		return nil, ReadFileOutput{}, fmt.Errorf("binary file detected")
	}

	content := string(data)
	displayContent := content

	if input.WithLineNumbers {
		lines := strings.Split(content, "\n")
		var builder strings.Builder
		for i, line := range lines {
			builder.WriteString(fmt.Sprintf("%d: %s\n", i+1, line))
		}
		displayContent = builder.String()
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: displayContent}},
	}, ReadFileOutput{Content: content}, nil
}

// ─── Filesystem: 파일 쓰기 ───────────────────────────────────

func WriteFile(ctx context.Context, req *mcp.CallToolRequest, input WriteFileInput) (
	*mcp.CallToolResult, WriteFileOutput, error) {

	cleanPath := filepath.Clean(input.FilePath)

	dir := filepath.Dir(cleanPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, WriteFileOutput{Success: false}, fmt.Errorf("failed to create directory: %w", err)
	}

	if !input.Overwrite {
		if _, err := os.Stat(cleanPath); err == nil {
			return nil, WriteFileOutput{Success: false}, fmt.Errorf("file already exists: %s (set overwrite: true to overwrite)", cleanPath)
		}
	}

	err := os.WriteFile(cleanPath, []byte(input.Content), 0644)
	if err != nil {
		return nil, WriteFileOutput{Success: false}, fmt.Errorf("failed to write file: %w", err)
	}

	msg := fmt.Sprintf("successfully wrote to %s", cleanPath)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}, WriteFileOutput{Success: true, Message: msg}, nil
}

// ─── Filesystem: 파일 삭제 ───────────────────────────────────

func DeleteFile(ctx context.Context, req *mcp.CallToolRequest, input DeleteFileInput) (
	*mcp.CallToolResult, DeleteFileOutput, error) {

	cleanPath := filepath.Clean(input.FilePath)

	if _, err := os.Stat(cleanPath); os.IsNotExist(err) {
		return nil, DeleteFileOutput{Success: false}, fmt.Errorf("path not found: %s", cleanPath)
	}

	if err := os.RemoveAll(cleanPath); err != nil {
		return nil, DeleteFileOutput{Success: false}, fmt.Errorf("failed to delete: %w", err)
	}

	msg := fmt.Sprintf("successfully deleted %s", cleanPath)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}, DeleteFileOutput{Success: true, Message: msg}, nil
}

// ─── Filesystem: 디렉토리 목록 ──────────────────────────────

func ListDirectory(ctx context.Context, req *mcp.CallToolRequest, input ListDirectoryInput) (
	*mcp.CallToolResult, ListDirectoryOutput, error) {

	cleanPath := filepath.Clean(input.DirectoryPath)

	entries, err := os.ReadDir(cleanPath)
	if err != nil {
		return nil, ListDirectoryOutput{}, fmt.Errorf("failed to list directory: %w", err)
	}

	var files []FileInfo
	for _, entry := range entries {
		info, _ := entry.Info()
		files = append(files, FileInfo{
			Name:  entry.Name(),
			IsDir: entry.IsDir(),
			Size:  info.Size(),
		})
	}

	var resultText string
	for _, f := range files {
		typeStr := "📄 File"
		if f.IsDir {
			typeStr = "📁 Dir "
		}
		resultText += fmt.Sprintf("%s %-20s %d bytes\n", typeStr, f.Name, f.Size)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: resultText}},
	}, ListDirectoryOutput{Files: files}, nil
}

// ─── Filesystem: 파일 부분 수정 ──────────────────────────────

func ReplaceLines(ctx context.Context, req *mcp.CallToolRequest, input ReplaceLinesInput) (
	*mcp.CallToolResult, ReplaceLinesOutput, error) {

	cleanPath := filepath.Clean(input.FilePath)

	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, ReplaceLinesOutput{Success: false}, fmt.Errorf("failed to read file: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	totalLines := len(lines)

	if input.StartLine < 1 || input.StartLine > totalLines || input.EndLine < input.StartLine {
		return nil, ReplaceLinesOutput{Success: false},
			fmt.Errorf("invalid line range: %d-%d (total lines: %d)", input.StartLine, input.EndLine, totalLines)
	}

	startIdx := input.StartLine - 1
	endIdx := input.EndLine

	newLines := strings.Split(strings.TrimSuffix(input.NewContent, "\n"), "\n")
	updatedLines := append(lines[:startIdx], append(newLines, lines[endIdx:]...)...)
	finalContent := strings.Join(updatedLines, "\n")

	err = os.WriteFile(cleanPath, []byte(finalContent), 0644)
	if err != nil {
		return nil, ReplaceLinesOutput{Success: false}, fmt.Errorf("failed to write updated file: %w", err)
	}

	msg := fmt.Sprintf("successfully replaced lines %d to %d in %s", input.StartLine, input.EndLine, cleanPath)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}, ReplaceLinesOutput{Success: true, Message: msg}, nil
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "dy-mcp-server", Version: "v1.0.0"}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_env_info",
		Description: "Returns system information including OS, architecture, current working directory, and user." +
			"Use this tool to determine the environment before executing OS-specific shell commands."}, GetEnvInfo)

	mcp.AddTool(server, &mcp.Tool{
		Name: "exec_cli",
		Description: "Executes a system shell command and returns the combined stdout and stderr." +
			"Use this to run CLI tools like git, ls, or custom scripts. " +
			"Input should be a full command string (e.g., 'git status'). " +
			"Warning: Be extremely careful with destructive commands"}, ExecCli)

	mcp.AddTool(server, &mcp.Tool{
		Name: "read_file",
		Description: "Reads the content of a file at the specified path." +
			" Use this tool when you need to examine the source code, configuration, or data within a file. " +
			"Only text files under 1MB are supported"}, ReadFile)

	mcp.AddTool(server, &mcp.Tool{
		Name: "write_file",
		Description: "Creates or overwrites a file at the specified path with the provided content. " +
			"It automatically creates parent directories if they don't exist. Use this to save code, logs, or configuration data."}, WriteFile)

	mcp.AddTool(server, &mcp.Tool{
		Name: "delete_file",
		Description: "Permanently deletes a file or a directory (including all its contents). " +
			"This is a recursive delete similar to 'rm -rf'. " +
			"Use with extreme caution as this cannot be undone."}, DeleteFile)

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_dir",
		Description: "Lists all files and directories within a specified path. " +
			" Use this to explore the file system structure and find files to read, write, or delete."}, ListDirectory)

	mcp.AddTool(server, &mcp.Tool{
		Name: "replace_line",
		Description: "Replaces a specific range of lines in a file with new content. " +
			"This is much more efficient than overwriting the entire file. Line numbers start from 1. " +
			"Use 'ReadFile' first to check line numbers before calling this."}, ReplaceLines)

	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{
		Stateless:                  true,
		DisableLocalhostProtection: true,
	})

	// nginx가 SSL 처리 → 내부 HTTP 8080으로만 서빙
	log.Fatal(http.ListenAndServe(":8080", handler))
}
