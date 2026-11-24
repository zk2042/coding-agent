package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ProjectScope 记录一次任务中被用户批准的项目与文件清单。
// write_file 和 delete_file 只能操作当前项目内、且已列入清单的文件。
type ProjectScope struct {
	mu        sync.Mutex
	root      string
	project   string
	mode      string
	allowed   map[string]bool
	overwrite map[string]bool
}

// NewProjectScope 创建项目范围控制器，root 是 workspace 的绝对路径。
func NewProjectScope(root string) *ProjectScope {
	return &ProjectScope{
		root:      root,
		allowed:   map[string]bool{},
		overwrite: map[string]bool{},
	}
}

// PlanInfo 描述一个待用户确认的方案。
type PlanInfo struct {
	Project   string
	Mode      string
	Files     []string
	Overwrite []string
	Create    []string
}

// BeginTask 清空上一次任务的授权，每次 Agent.Run 开始时调用。
func (s *ProjectScope) BeginTask() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.project = ""
	s.mode = ""
	s.allowed = map[string]bool{}
	s.overwrite = map[string]bool{}
}

// ActiveProject 返回当前任务已批准的项目目录名；未确认时为空。
func (s *ProjectScope) ActiveProject() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.project == "." {
		// workdir 根目录不是项目，记忆按全局处理。
		return ""
	}
	return s.project
}

// ProjectRoot 返回当前项目的绝对路径；未确认项目时返回空字符串。
func (s *ProjectScope) ProjectRoot() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.project == "" {
		return ""
	}
	if s.project == "." {
		return s.root
	}
	return filepath.Join(s.root, filepath.FromSlash(s.project))
}

// ValidatePlan 校验项目名、模式和文件清单，返回给用户看的信息。
// 它不改变当前授权，只有用户同意后才能调用 Commit。
func (s *ProjectScope) ValidatePlan(project, mode string, files []string) (PlanInfo, error) {
	if s == nil {
		return PlanInfo{}, fmt.Errorf("项目策略未配置")
	}
	project, err := normalizeProject(project)
	if err != nil {
		return PlanInfo{}, err
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "create" && mode != "modify" {
		return PlanInfo{}, fmt.Errorf("mode 必须是 create 或 modify")
	}
	if project == "." && mode != "modify" {
		return PlanInfo{}, fmt.Errorf("workspace 根目录只允许 modify，用于修改 README 等已有文件")
	}
	if len(files) == 0 {
		return PlanInfo{}, fmt.Errorf("files 不能为空")
	}

	projectRoot := filepath.Join(s.root, filepath.FromSlash(project))
	info, statErr := os.Lstat(projectRoot)
	exists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return PlanInfo{}, statErr
	}
	if exists {
		if info.Mode()&os.ModeSymlink != 0 {
			return PlanInfo{}, fmt.Errorf("项目目录不能是符号链接: %s", project)
		}
		if !info.IsDir() {
			return PlanInfo{}, fmt.Errorf("项目路径已存在但不是目录: %s", project)
		}
		if mode == "create" {
			empty, err := isEmptyDir(projectRoot)
			if err != nil {
				return PlanInfo{}, err
			}
			if !empty {
				return PlanInfo{}, fmt.Errorf("目录 %s 已存在且不为空；新项目请换一个目录名，或改用 modify", project)
			}
		}
	} else if mode == "modify" {
		return PlanInfo{}, fmt.Errorf("目录 %s 不存在；修改已有项目请确认项目目录名", project)
	}

	seen := make(map[string]bool, len(files))
	unique := make([]string, 0, len(files))
	overwrite := make([]string, 0, len(files))
	create := make([]string, 0, len(files))
	for _, raw := range files {
		rel, err := normalizePlanFile(project, raw)
		if err != nil {
			return PlanInfo{}, err
		}
		if seen[rel] {
			continue
		}
		seen[rel] = true
		full := filepath.Join(s.root, filepath.FromSlash(rel))
		fileInfo, err := os.Lstat(full)
		if err == nil {
			if fileInfo.IsDir() {
				return PlanInfo{}, fmt.Errorf("files 中不能包含目录: %s", rel)
			}
			overwrite = append(overwrite, rel)
		} else if os.IsNotExist(err) {
			if project == "." {
				return PlanInfo{}, fmt.Errorf("workspace 根目录只允许修改已有文件: %s", rel)
			}
			create = append(create, rel)
		} else {
			return PlanInfo{}, err
		}
		unique = append(unique, rel)
	}
	sort.Strings(overwrite)
	sort.Strings(create)

	return PlanInfo{
		Project:   project,
		Mode:      mode,
		Files:     unique,
		Overwrite: overwrite,
		Create:    create,
	}, nil
}

// Commit 在用户同意方案后保存授权。
func (s *ProjectScope) Commit(info PlanInfo) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.project = info.Project
	s.mode = info.Mode
	s.allowed = make(map[string]bool, len(info.Files))
	s.overwrite = make(map[string]bool, len(info.Overwrite))
	for _, file := range info.Files {
		s.allowed[file] = true
	}
	for _, file := range info.Overwrite {
		s.overwrite[file] = true
	}
}

// CheckPath 校验文件属于当前项目且已列入清单；不检查覆盖。
func (s *ProjectScope) CheckPath(rel string) error {
	if s == nil {
		return fmt.Errorf("项目策略未配置")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.project == "" {
		return fmt.Errorf("当前任务还没有确认项目；请先调用 confirm_plan")
	}
	key, err := projectRelative(s.project, rel)
	if err != nil {
		return err
	}
	if !s.allowed[key] {
		return fmt.Errorf("文件不在本次确认的方案中: %s", rel)
	}
	return nil
}

// CheckWrite 在 CheckPath 基础上禁止覆盖未在方案中声明的已有文件。
func (s *ProjectScope) CheckWrite(rel string) error {
	if s == nil {
		return fmt.Errorf("项目策略未配置")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.project == "" {
		return fmt.Errorf("当前任务还没有确认项目；请先调用 confirm_plan")
	}
	key, err := projectRelative(s.project, rel)
	if err != nil {
		return err
	}
	if !s.allowed[key] {
		return fmt.Errorf("文件不在本次确认的方案中: %s", rel)
	}
	full := filepath.Join(s.root, filepath.FromSlash(key))
	if _, err := os.Lstat(full); err == nil {
		if !s.overwrite[key] {
			return fmt.Errorf("文件已存在且方案未声明覆盖: %s", rel)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func normalizeProject(project string) (string, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return "", fmt.Errorf("project 不能为空")
	}
	if project == "." {
		return ".", nil
	}
	if project == ".." || filepath.Base(project) != project ||
		strings.ContainsAny(project, `/\`) {
		return "", fmt.Errorf("project 必须是 workspace 下的单层目录名: %q", project)
	}
	if strings.HasPrefix(project, ".") {
		return "", fmt.Errorf("project 不能以 . 开头: %q", project)
	}
	if len([]rune(project)) > 64 {
		return "", fmt.Errorf("project 名称过长")
	}
	for _, r := range project {
		if r < 32 || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
			return "", fmt.Errorf("project 含非法字符: %q", project)
		}
	}
	return project, nil
}

func normalizePlanFile(project, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("files 中存在空路径")
	}
	if !filepath.IsLocal(raw) {
		return "", fmt.Errorf("文件必须是 workspace 相对路径: %q", raw)
	}
	cleaned := filepath.ToSlash(filepath.Clean(raw))
	if project == "." {
		if cleaned == "." || strings.Contains(cleaned, "/") {
			return "", fmt.Errorf("workspace 根目录只允许直接文件: %q", raw)
		}
		return cleaned, nil
	}
	if cleaned == project {
		return "", fmt.Errorf("files 必须是项目内的文件，不能是项目目录: %q", raw)
	}
	if !strings.HasPrefix(cleaned, project+"/") {
		return "", fmt.Errorf("文件必须位于 %s/ 下: %q", project, raw)
	}
	return cleaned, nil
}

func projectRelative(project, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("path 不能为空")
	}
	if !filepath.IsLocal(raw) {
		return "", fmt.Errorf("只允许 workspace 相对路径: %q", raw)
	}
	cleaned := filepath.ToSlash(filepath.Clean(raw))
	if project == "." {
		if cleaned == "." || strings.Contains(cleaned, "/") {
			return "", fmt.Errorf("workspace 根目录只允许直接文件: %q", raw)
		}
		return cleaned, nil
	}
	if cleaned == project || !strings.HasPrefix(cleaned, project+"/") {
		return "", fmt.Errorf("文件不属于当前项目 %s: %q", project, raw)
	}
	return cleaned, nil
}

func isEmptyDir(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Name() == ".gitkeep" {
			continue
		}
		return false, nil
	}
	return true, nil
}
