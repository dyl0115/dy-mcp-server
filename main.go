package main

import (
	"context"
	"fmt"
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

	// 1. 명령어 유효성 검사 및 보안 필터링
	trimmedCmd := strings.TrimSpace(input.Command)
	if trimmedCmd == "" {
		return nil, CommandOutput{Result: "error: empty command"}, nil
	}

	// [보안] 파괴적인 명령어 실행 방지 (간이 가드레일)
	forbiddenKeywords := []string{"rm -rf /", "mkfs", "dd if=", ":(){ :|:& };:"}
	for _, word := range forbiddenKeywords {
		if strings.Contains(trimmedCmd, word) {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "security error: this command is too dangerous to execute"}},
			}, CommandOutput{Result: "blocked"}, nil
		}
	}

	// 2. 타임아웃 설정
	if input.Timeout <= 0 {
		input.Timeout = 30 // 기본값 30초
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(input.Timeout)*time.Second)
	defer cancel()

	// 3. 명령어 준비
	cmd := exec.CommandContext(ctx, "bash", "-c", trimmedCmd)

	// [중요] 현재 프로세스의 환경 변수(PATH 등)를 상속받아야 ls, grep 등이 작동함
	cmd.Env = os.Environ()

	// 4. 실행 및 결과 수집 (Stdout + Stderr 통합)
	out, err := cmd.CombinedOutput()

	// 5. 출력 크기 제한 (AI 컨텍스트 보호)
	const maxOutput = 100 * 1024 // 100KB로 충분 (너무 크면 AI가 혼란을 겪음)
	resultStr := string(out)
	if len(resultStr) > maxOutput {
		resultStr = resultStr[:maxOutput] + "\n... [output truncated due to size limit]"
	}

	// 6. 결과 반환 처리
	if err != nil {
		// 타임아웃 케이스
		if ctx.Err() == context.DeadlineExceeded {
			errMsg := fmt.Sprintf("command timed out after %ds", input.Timeout)
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: errMsg}},
			}, CommandOutput{Result: errMsg}, nil
		}

		// 실행 중 에러 발생 (문법 오류, 권한 오류 등)
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: resultStr}},
		}, CommandOutput{Result: resultStr}, nil
	}

	// 성공적인 실행
	return &mcp.CallToolResult{
		IsError: false,
		Content: []mcp.Content{&mcp.TextContent{Text: resultStr}},
	}, CommandOutput{Result: resultStr}, nil
}

// ─── Filesystem: 파일 읽기 ───────────────────────────────────
func ReadFile(ctx context.Context, req *mcp.CallToolRequest, input ReadFileInput) (
	*mcp.CallToolResult, ReadFileOutput, error) {

	cleanPath := filepath.Clean(input.FilePath)

	// 1. 파일 정보 확인
	info, err := os.Stat(cleanPath)
	if err != nil {
		return nil, ReadFileOutput{}, fmt.Errorf("file not found: %w", err)
	}

	const maxFileSize = 1 * 1024 * 1024
	if info.Size() > maxFileSize {
		return nil, ReadFileOutput{}, fmt.Errorf("file too large (%d bytes)", info.Size())
	}

	// 2. 파일 읽기
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, ReadFileOutput{}, fmt.Errorf("read failed: %w", err)
	}

	// 3. 바이너리 체크
	checkLen := len(data)
	if checkLen > 512 {
		checkLen = 512
	}
	if !utf8.Valid(data[:checkLen]) {
		return nil, ReadFileOutput{}, fmt.Errorf("binary file detected")
	}

	content := string(data)

	// 💡 4. 라인 번호 추가 로직
	displayContent := content
	if input.WithLineNumbers {
		lines := strings.Split(content, "\n")
		var builder strings.Builder
		for i, line := range lines {
			// "1: 내용" 형식으로 빌드
			builder.WriteString(fmt.Sprintf("%d: %s\n", i+1, line))
		}
		displayContent = builder.String()
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{
			Text: displayContent,
		}},
	}, ReadFileOutput{Content: content}, nil // Raw 데이터는 원본 그대로 반환
}

// ─── Filesystem: 파일 쓰기 ───────────────────────────────────
func WriteFile(ctx context.Context, req *mcp.CallToolRequest, input WriteFileInput) (
	*mcp.CallToolResult, WriteFileOutput, error) {

	// 1. 경로 정규화
	cleanPath := filepath.Clean(input.FilePath)

	// 2. 디렉토리가 없으면 생성 (AI가 'logs/test.txt'라고 하면 logs 폴더를 만들어줌)
	dir := filepath.Dir(cleanPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, WriteFileOutput{Success: false}, fmt.Errorf("failed to create directory: %w", err)
	}

	// 파일 존재 여부 체크
	if !input.Overwrite {
		if _, err := os.Stat(cleanPath); err == nil {
			return nil, WriteFileOutput{Success: false}, fmt.Errorf("file already exists: %s (set overwrite: true to overwrite)", cleanPath)
		}
	}

	// 3. 파일 쓰기 (0644: 읽기/쓰기 권한)
	err := os.WriteFile(cleanPath, []byte(input.Content), 0644)
	if err != nil {
		return nil, WriteFileOutput{Success: false}, fmt.Errorf("failed to write file: %w", err)
	}

	msg := fmt.Sprintf("successfully wrote to %s", cleanPath)

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: msg},
		},
	}, WriteFileOutput{Success: true, Message: msg}, nil
}

// ─── Filesystem: 파일 쓰기 ───────────────────────────────────
func DeleteFile(ctx context.Context, req *mcp.CallToolRequest, input DeleteFileInput) (
	*mcp.CallToolResult, DeleteFileOutput, error) {

	cleanPath := filepath.Clean(input.FilePath)

	// 1. 존재 확인 (AI에게 정확한 상황 보고)
	if _, err := os.Stat(cleanPath); os.IsNotExist(err) {
		return nil, DeleteFileOutput{Success: false}, fmt.Errorf("path not found: %s", cleanPath)
	}

	// 2. 재귀적 삭제 (파일이면 파일만, 폴더면 내부 파일까지 전부 삭제)
	// os.RemoveAll은 경로가 없으면 에러를 안 내지만, 우리는 위에서 체크했으니 안전합니다.// 삭제 전 읽기 권한 등 체크가 필요할 수도 있지만 보통 바로 삭제 시도
	if err := os.RemoveAll(cleanPath); err != nil {
		return nil, DeleteFileOutput{Success: false}, fmt.Errorf("failed to delete: %w", err)
	}

	msg := fmt.Sprintf("successfully deleted %s", cleanPath)

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: msg},
		},
	}, DeleteFileOutput{Success: true, Message: msg}, nil
}

// ─── Filesystem: 디렉토리 목록 ──────────────────────────────
func ListDirectory(ctx context.Context, req *mcp.CallToolRequest, input ListDirectoryInput) (
	*mcp.CallToolResult, ListDirectoryOutput, error) {

	cleanPath := filepath.Clean(input.DirectoryPath)

	// 1. 디렉토리 읽기
	entries, err := os.ReadDir(cleanPath)
	if err != nil {
		return nil, ListDirectoryOutput{}, fmt.Errorf("failed to list directory: %w", err)
	}

	var files []FileInfo
	for _, entry := range entries {
		info, _ := entry.Info() // 상세 정보를 가져옴
		files = append(files, FileInfo{
			Name:  entry.Name(),
			IsDir: entry.IsDir(),
			Size:  info.Size(),
		})
	}

	// 2. AI가 읽기 편하게 텍스트 결과 생성
	var resultText string
	for _, f := range files {
		typeStr := "📄 File"
		if f.IsDir {
			typeStr = "📁 Dir "
		}
		resultText += fmt.Sprintf("%s %-20s %d bytes\n", typeStr, f.Name, f.Size)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: resultText},
		},
	}, ListDirectoryOutput{Files: files}, nil
}

// ─── Filesystem: 파일 부분 수정 ──────────────────────────────
func ReplaceLines(ctx context.Context, req *mcp.CallToolRequest, input ReplaceLinesInput) (
	*mcp.CallToolResult, ReplaceLinesOutput, error) {

	cleanPath := filepath.Clean(input.FilePath)

	// 1. 파일 읽기
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, ReplaceLinesOutput{Success: false}, fmt.Errorf("failed to read file: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	totalLines := len(lines)

	// 2. 라인 범위 유효성 검사
	if input.StartLine < 1 || input.StartLine > totalLines || input.EndLine < input.StartLine {
		return nil, ReplaceLinesOutput{Success: false},
			fmt.Errorf("invalid line range: %d-%d (total lines: %d)", input.StartLine, input.EndLine, totalLines)
	}

	// 3. 내용 교체 로직 (배열 슬라이싱 활용)
	// Go 슬라이스는 0부터 시작하므로 -1 해줍니다.
	startIdx := input.StartLine - 1
	endIdx := input.EndLine // 슬라이싱의 끝은 포함되지 않으므로 그대로 둠

	// 새로운 라인들 준비
	newLines := strings.Split(strings.TrimSuffix(input.NewContent, "\n"), "\n")

	// 앞부분 + 새 내용 + 뒷부분 합치기
	updatedLines := append(lines[:startIdx], append(newLines, lines[endIdx:]...)...)
	finalContent := strings.Join(updatedLines, "\n")

	// 4. 파일 쓰기
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
	}, nil)

	fmt.Println("dy-mcp-server listening on :3000")
	http.ListenAndServe(":3000", handler)
}
