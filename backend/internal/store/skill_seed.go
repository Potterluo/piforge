package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Built-in skills (PRD FR-14.7).
//
// A deployment with no skills is a deployment where the feature looks broken: the page
// says "you can write one" and there is nothing to try. These three are shared, admin-owned
// and deliberately small — a starting point someone can read, copy or delete, not a
// library to hide behind.

// BuiltinSkill is one shipped skill: a name, a description and its SKILL.md.
type BuiltinSkill struct {
	Name        string
	Description string
	Body        string
}

// BuiltinSkills is the shipped set. Kept in code rather than on disk so an update
// replaces them instead of leaving two copies to diverge.
func BuiltinSkills() []BuiltinSkill {
	return []BuiltinSkill{
		{
			Name:        "code-review",
			Description: "审查改动：先读 diff，再只针对真实风险提意见。",
			Body: `---
name: code-review
description: 审查当前工作区的改动，只报告真实存在的问题。
---

# 审查改动

1. 用 ` + "`git diff`" + ` 看未暂存的改动，用 ` + "`git diff --cached`" + ` 看已暂存的改动。
2. 按风险排序，只报告**会导致错误**的问题：逻辑错误、边界条件、并发、资源泄漏、错误的错误处理。
3. 不评论格式、命名偏好和"可以更优雅"这类没有实际后果的事。
4. 每条意见必须能指出具体文件和行，并说明什么输入会触发它。
5. 如果改动没有实际问题，直接说"没有发现需要修改的问题"，不要为了凑数而找问题。
`,
		},
		{
			Name:        "test-gap",
			Description: "找出改动中缺少测试覆盖的行为，并补上最小的测试。",
			Body: `---
name: test-gap
description: 针对当前改动补齐缺失的测试。
---

# 补测试

1. 先读改动，列出**行为上的变化**（不是代码行的变化）。
2. 对照已有测试，找出没有被覆盖的新行为或修好的 bug。
3. 每个缺失写一个最小的测试：只断言那个行为，不复制已有测试的脚手架。
4. 优先覆盖边界：空输入、超长输入、重复调用、并发、失败路径。
5. 运行测试并贴出结果。测试跑不过就不算完成。
`,
		},
		{
			Name:        "explain-codebase",
			Description: "快速讲清一个陌生仓库的结构：入口在哪、数据怎么流动。",
			Body: `---
name: explain-codebase
description: 用最少的话讲清这个仓库是怎么组织的。
---

# 讲清一个仓库

1. 先看根目录的 README、构建文件和入口文件，不要一开始就读实现细节。
2. 找出三个东西：程序从哪里启动、核心数据放在哪里、外部依赖有哪些。
3. 用"一个请求从进入到返回经过哪些文件"的方式讲述，不要按目录罗列。
4. 每讲一个结论都给出文件路径，让人能自己去看。
5. 不确定的地方直说不确定，不要编。
`,
		},
	}
}

// EnsureBuiltinSkills installs the shipped skills once, for the admin account.
//
// It is idempotent: a skill whose name already exists is left alone, so an operator who
// edited one of these keeps their edit across restarts.
func (s *Store) EnsureBuiltinSkills(ctx context.Context, adminID string, write func(skillID string, files map[string]string) (bundleKey, contentHash string, err error)) (int, error) {
	if strings.TrimSpace(adminID) == "" {
		return 0, fmt.Errorf("store: seed skills: admin id is required: %w", ErrInvalid)
	}
	existing, err := s.ListSkills(ctx, adminID)
	if err != nil {
		return 0, err
	}
	have := map[string]bool{}
	for _, skill := range existing {
		have[skill.Name] = true
	}

	created := 0
	for _, builtin := range BuiltinSkills() {
		if have[builtin.Name] {
			continue
		}
		id := NewID("skl")
		prepared, err := s.CreateSkillRow(ctx, adminID, id, contracts.Skill{
			Name:        builtin.Name,
			Version:     "1.0.0",
			Description: builtin.Description,
			ContentHash: "pending",
			Scope:       contracts.SkillAdmin,
			Enabled:     true,
		})
		if err != nil {
			return created, err
		}
		bundleKey, hash, err := write(prepared.ID, map[string]string{"SKILL.md": builtin.Body})
		if err != nil {
			return created, err
		}
		if _, err := s.UpdateSkillBundle(ctx, adminID, prepared.ID, bundleKey, hash); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

// CreateSkillRow inserts a skill with an explicit id and scope. It exists for the seeder,
// which has to know the id before the bundle is written.
func (s *Store) CreateSkillRow(ctx context.Context, ownerID, id string, in contracts.Skill) (*contracts.Skill, error) {
	scope := in.Scope
	if scope == "" {
		scope = contracts.SkillUser
	}
	version := in.Version
	if version == "" {
		version = "0.0.0"
	}
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO skills (id, scope, owner_id, name, version, description, content_hash,
		                     bundle_key, source_url, enabled, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, string(scope), ownerID, in.Name, version, in.Description, in.ContentHash,
		in.BundleKey, in.SourceURL, boolInt(in.Enabled), formatTime(s.now()))
	if err != nil {
		return nil, fmt.Errorf("store: create skill %q: %w", in.Name, err)
	}
	// Read the row back from the write connection. The read pool is a separate
	// connection, and a read issued in the same instant as the commit can still be
	// looking at the old snapshot — which read as "skill not found" right after creating
	// it, the least believable error this code could produce.
	return scanSkill(s.w.QueryRowContext(ctx,
		`SELECT id, scope, name, version, description, content_hash, bundle_key, source_url, enabled, created_at
		   FROM skills WHERE id = ?`, id))
}
