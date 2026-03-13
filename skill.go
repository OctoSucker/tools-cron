package cron

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tools "github.com/OctoSucker/octosucker-tools"
	"github.com/google/uuid"
	robfigcron "github.com/robfig/cron/v3"
)

const providerName = "github.com/OctoSucker/tools-cron"

type staticJob struct {
	Spec      string
	TaskInput string
}

type SkillCron struct {
	mu             sync.RWMutex
	store          *Store
	submitter      func(string) error
	cr             *robfigcron.Cron
	loc            *time.Location
	staticJobs     []staticJob
	dynamicEntries map[string]robfigcron.EntryID
}

func (s *SkillCron) Init(config map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cr != nil {
		s.cr.Stop()
		s.cr = nil
	}
	s.dynamicEntries = nil
	if s.store != nil {
		_ = s.store.Close()
		s.store = nil
	}
	s.staticJobs = nil
	s.loc = time.Local

	if config == nil {
		return nil
	}
	path, _ := config["storage_path"].(string)
	if path == "" {
		return nil
	}
	path = filepath.Clean(path)
	store, err := Open(path)
	if err != nil {
		return fmt.Errorf("skill-cron: %w", err)
	}
	s.store = store

	if tz, ok := config["timezone"].(string); ok && tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			s.loc = loc
		}
	}

	if raw, ok := config["schedules"].([]interface{}); ok {
		for _, r := range raw {
			m, ok := r.(map[string]interface{})
			if !ok {
				continue
			}
			spec, _ := m["spec"].(string)
			taskInput, _ := m["task_input"].(string)
			spec = strings.TrimSpace(spec)
			taskInput = strings.TrimSpace(taskInput)
			if spec != "" && taskInput != "" {
				s.staticJobs = append(s.staticJobs, staticJob{Spec: spec, TaskInput: taskInput})
			}
		}
	}
	return nil
}

func (s *SkillCron) Cleanup() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cr != nil {
		ctx := s.cr.Stop()
		<-ctx.Done()
		s.cr = nil
	}
	s.dynamicEntries = nil
	s.submitter = nil
	if s.store != nil {
		err := s.store.Close()
		s.store = nil
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *SkillCron) startCron() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.submitter == nil || s.store == nil {
		return
	}
	if s.cr != nil {
		return
	}
	s.cr = robfigcron.New(robfigcron.WithLocation(s.loc))
	s.dynamicEntries = make(map[string]robfigcron.EntryID)

	submitter := s.submitter
	jobRunner := func(taskInput string) {
		payload := "[定时任务触发] 请仅按下面描述执行一次，不要调用 cron_add、cron_remove、cron_toggle，也不要添加或修改任何定时任务。\n\n" + taskInput
		_ = submitter(payload)
	}

	list, err := s.store.List()
	if err == nil {
		for _, j := range list {
			if !j.Enabled {
				continue
			}
			taskInput := j.TaskInput
			spec := j.Spec
			entryID, err := s.cr.AddFunc(spec, func() { jobRunner(taskInput) })
			if err != nil {
				continue
			}
			s.dynamicEntries[j.ID] = entryID
		}
	}

	for _, st := range s.staticJobs {
		taskInput := st.TaskInput
		spec := st.Spec
		_, _ = s.cr.AddFunc(spec, func() { jobRunner(taskInput) })
	}

	s.cr.Start()
}

func (s *SkillCron) getStore() *Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.store
}

var globalSkillCron *SkillCron

func RegisterCronSkill(registry *tools.ToolRegistry, agent interface{}) error {
	taskSubmitter, ok := agent.(tools.TaskSubmitter)
	if ok {
		globalSkillCron.mu.Lock()
		globalSkillCron.submitter = taskSubmitter.SubmitTask
		globalSkillCron.mu.Unlock()
		globalSkillCron.startCron()
	}

	registry.Register(&tools.Tool{
		Name:        "cron_list",
		Description: "列出当前所有定时任务（含配置中的静态任务与通过 cron_add 添加的动态任务）。返回 id、spec、task_input、enabled、created_at；静态任务无 id。",
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		Handler: handleCronList,
	})
	registry.Register(&tools.Tool{
		Name:        "cron_add",
		Description: "添加一条定时任务并持久化。到点时将 task_input 提交给 Agent 执行。spec 支持标准 cron（如 0 9 * * * 每天 9 点）或 @every 1h、@every 30m 等。",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"spec": map[string]interface{}{
					"type":        "string",
					"description": "Cron 表达式，如 0 9 * * * 或 @every 1h",
				},
				"task_input": map[string]interface{}{
					"type":        "string",
					"description": "到点时提交给 Agent 的任务内容",
				},
			},
			"required": []string{"spec", "task_input"},
		},
		Handler: handleCronAdd,
	})
	registry.Register(&tools.Tool{
		Name:        "cron_remove",
		Description: "按 job_id 删除定时任务（仅限通过 cron_add 添加的动态任务，无法删除配置中的静态任务）。",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"job_id": map[string]interface{}{
					"type":        "string",
					"description": "要删除的任务 ID（cron_list 返回的 id）",
				},
			},
			"required": []string{"job_id"},
		},
		Handler: handleCronRemove,
	})
	registry.Register(&tools.Tool{
		Name:        "cron_toggle",
		Description: "按 job_id 启用或禁用定时任务（仅限动态任务）。禁用后不再触发，但保留在列表中可再次启用。",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"job_id": map[string]interface{}{
					"type":        "string",
					"description": "任务 ID",
				},
				"enabled": map[string]interface{}{
					"type":        "boolean",
					"description": "true=启用，false=禁用",
				},
			},
			"required": []string{"job_id", "enabled"},
		},
		Handler: handleCronToggle,
	})
	return nil
}

func handleCronList(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	s := globalSkillCron
	store := s.getStore()
	if store == nil {
		return nil, fmt.Errorf("cron_list: storage not configured, set storage_path in skill-cron config")
	}
	s.mu.RLock()
	staticJobs := make([]staticJob, len(s.staticJobs))
	copy(staticJobs, s.staticJobs)
	s.mu.RUnlock()

	list, err := store.List()
	if err != nil {
		return nil, err
	}
	entries := make([]interface{}, 0, len(staticJobs)+len(list))
	for _, st := range staticJobs {
		entries = append(entries, map[string]interface{}{
			"id":         "",
			"spec":       st.Spec,
			"task_input": st.TaskInput,
			"enabled":    true,
			"static":     true,
			"created_at": int64(0),
		})
	}
	for _, j := range list {
		entries = append(entries, map[string]interface{}{
			"id":         j.ID,
			"spec":       j.Spec,
			"task_input": j.TaskInput,
			"enabled":    j.Enabled,
			"static":     false,
			"created_at": j.CreatedAt,
		})
	}
	return map[string]interface{}{"count": len(entries), "jobs": entries}, nil
}

func handleCronAdd(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	s := globalSkillCron
	store := s.getStore()
	if store == nil {
		return nil, fmt.Errorf("cron_add: storage not configured, set storage_path in skill-cron config")
	}
	spec, _ := params["spec"].(string)
	spec = strings.TrimSpace(spec)
	taskInput, _ := params["task_input"].(string)
	taskInput = strings.TrimSpace(taskInput)
	if spec == "" || taskInput == "" {
		return nil, fmt.Errorf("cron_add: spec and task_input are required")
	}
	id := uuid.New().String()
	if err := store.Add(id, spec, taskInput, true); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.cr != nil && s.submitter != nil {
		submitter := s.submitter
		entryID, err := s.cr.AddFunc(spec, func() { _ = submitter(taskInput) })
		if err != nil {
			s.mu.Unlock()
			_ = store.Remove(id)
			return nil, fmt.Errorf("cron_add: invalid spec %q: %w", spec, err)
		}
		if s.dynamicEntries == nil {
			s.dynamicEntries = make(map[string]robfigcron.EntryID)
		}
		s.dynamicEntries[id] = entryID
	}
	s.mu.Unlock()
	return map[string]interface{}{"ok": true, "job_id": id}, nil
}

func handleCronRemove(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	s := globalSkillCron
	store := s.getStore()
	if store == nil {
		return nil, fmt.Errorf("cron_remove: storage not configured")
	}
	jobID, _ := params["job_id"].(string)
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, fmt.Errorf("cron_remove: job_id is required")
	}
	s.mu.Lock()
	if entryID, ok := s.dynamicEntries[jobID]; ok && s.cr != nil {
		s.cr.Remove(entryID)
		delete(s.dynamicEntries, jobID)
	}
	s.mu.Unlock()
	if err := store.Remove(jobID); err != nil {
		return nil, err
	}
	return map[string]interface{}{"ok": true}, nil
}

func handleCronToggle(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	s := globalSkillCron
	store := s.getStore()
	if store == nil {
		return nil, fmt.Errorf("cron_toggle: storage not configured")
	}
	jobID, _ := params["job_id"].(string)
	jobID = strings.TrimSpace(jobID)
	enabled, _ := params["enabled"].(bool)
	if jobID == "" {
		return nil, fmt.Errorf("cron_toggle: job_id is required")
	}
	j, ok, err := store.Get(jobID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("cron_toggle: job_id %q not found", jobID)
	}
	if err := store.SetEnabled(jobID, enabled); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if entryID, has := s.dynamicEntries[jobID]; has && s.cr != nil {
		s.cr.Remove(entryID)
		delete(s.dynamicEntries, jobID)
	}
	if enabled {
		if s.cr != nil && s.submitter != nil {
			taskInput := j.TaskInput
			spec := j.Spec
			submitter := s.submitter
			entryID, err := s.cr.AddFunc(spec, func() { _ = submitter(taskInput) })
			if err != nil {
				_ = store.SetEnabled(jobID, false)
				return nil, fmt.Errorf("cron_toggle: add back failed: %w", err)
			}
			if s.dynamicEntries == nil {
				s.dynamicEntries = make(map[string]robfigcron.EntryID)
			}
			s.dynamicEntries[jobID] = entryID
		}
	}
	return map[string]interface{}{"ok": true, "enabled": enabled}, nil
}

func init() {
	globalSkillCron = &SkillCron{}
	tools.RegisterToolProviderWithMetadata(providerName, tools.ToolProviderMetadata{Name: providerName}, RegisterCronSkill, globalSkillCron)
}
