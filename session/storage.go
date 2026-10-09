package session

import (
	"claude-squad/config"
	"claude-squad/session/workspace"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"time"
)

// InstanceData represents the serializable data of an Instance
type InstanceData struct {
	workspacePresent bool

	Title     string    `json:"title"`
	Path      string    `json:"path"`
	Branch    string    `json:"branch"`
	Status    Status    `json:"status"`
	Height    int       `json:"height"`
	Width     int       `json:"width"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	AutoYes   bool      `json:"auto_yes"`

	Program   string           `json:"program"`
	Workspace *workspace.Data  `json:"workspace,omitempty"`
	Worktree  *GitWorktreeData `json:"worktree,omitempty"`
	DiffStats DiffStatsData    `json:"diff_stats"`
}

// UnmarshalJSON tracks whether workspace was present independently of its value.
func (d *InstanceData) UnmarshalJSON(data []byte) error {
	type instanceData InstanceData
	var decoded instanceData
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	_, decoded.workspacePresent = fields["workspace"]
	*d = InstanceData(decoded)
	return nil
}

// GitWorktreeData represents the serializable data of a GitWorktree
type GitWorktreeData struct {
	RepoPath         string `json:"repo_path"`
	WorktreePath     string `json:"worktree_path"`
	SessionName      string `json:"session_name"`
	BranchName       string `json:"branch_name"`
	BaseCommitSHA    string `json:"base_commit_sha"`
	IsExistingBranch bool   `json:"is_existing_branch"`
}

// DiffStatsData represents the serializable data of a DiffStats
type DiffStatsData struct {
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Content string `json:"content"`
}

// Storage handles saving and loading instances using the state interface
type Storage struct {
	state config.InstanceStorage
}

// NewStorage creates a new storage instance
func NewStorage(state config.InstanceStorage) (*Storage, error) {
	return &Storage{
		state: state,
	}, nil
}

// SaveInstances saves the list of instances to disk
func (s *Storage) SaveInstances(instances []*Instance) error {
	// Convert instances to InstanceData
	data := make([]InstanceData, 0)
	for _, instance := range instances {
		if instance.Started() {
			data = append(data, instance.ToInstanceData())
		}
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal instances: %w", err)
	}

	return s.state.SaveInstances(jsonData)
}

var loadInstancePublishedHook func()

func newerApplyRepair(candidate, current workspace.ApplyMetadataRepair) bool {
	candidateTime, candidateErr := time.Parse(time.RFC3339Nano, candidate.AppliedAt)
	currentTime, currentErr := time.Parse(time.RFC3339Nano, current.AppliedAt)
	if candidateErr == nil && currentErr == nil && !candidateTime.Equal(currentTime) {
		return candidateTime.After(currentTime)
	}
	if candidate.AppliedAt != current.AppliedAt {
		return candidate.AppliedAt > current.AppliedAt
	}
	return candidate.TransactionID > current.TransactionID
}

// LoadInstances loads the list of instances from disk
func (s *Storage) LoadInstances() ([]*Instance, error) {
	jsonData := s.state.GetInstances()

	var instancesData []InstanceData
	if err := json.Unmarshal(jsonData, &instancesData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal instances: %w", err)
	}

	type projectRecovery struct {
		report workspace.RecoveryReport
		data   map[string]*workspace.ShadowData
	}
	projects := make(map[string]*projectRecovery)
	for i := range instancesData {
		data := &instancesData[i]
		if data.Workspace == nil || data.Workspace.Mode != workspace.ModeDirectory || data.Workspace.Shadow == nil {
			continue
		}
		projectPath := filepath.Dir(data.Workspace.Shadow.RepoPath)
		project := projects[projectPath]
		if project == nil {
			project = &projectRecovery{data: make(map[string]*workspace.ShadowData)}
			projects[projectPath] = project
		}
		project.data[data.Workspace.Shadow.SessionID] = data.Workspace.Shadow
	}

	projectPaths := make([]string, 0, len(projects))
	for projectPath := range projects {
		projectPaths = append(projectPaths, projectPath)
	}
	sort.Strings(projectPaths)
	leases := make(map[string]*workspace.ProjectRecoveryLease)
	acknowledgements := make(map[string][]workspace.ApplyMetadataRepair)
	var instances []*Instance
	var recoverLocked func(int) error
	recoverLocked = func(index int) error {
		if index < len(projectPaths) {
			projectPath := projectPaths[index]
			project := projects[projectPath]
			return workspace.RecoverProjectLocked(projectPath, project.data, func(report workspace.RecoveryReport, lease *workspace.ProjectRecoveryLease) error {
				project.report = report
				leases[projectPath] = lease
				return recoverLocked(index + 1)
			})
		}

		repaired := false
		informational := make(map[string][]workspace.RecoveryIssue)
		for _, projectPath := range projectPaths {
			project := projects[projectPath]
			authoritative := make(map[string]workspace.ApplyMetadataRepair)
			for _, issue := range project.report.Issues {
				if issue.Informational {
					informational[projectPath] = append(informational[projectPath], issue)
				}
			}
			for _, repair := range project.report.Repairs {
				shadow := project.data[repair.SessionID]
				if shadow == nil {
					continue
				}
				current, exists := authoritative[repair.SessionID]
				if !exists || newerApplyRepair(repair, current) {
					authoritative[repair.SessionID] = repair
				}
				if repair.State == workspace.TransactionComplete {
					acknowledgements[projectPath] = append(acknowledgements[projectPath], repair)
				}
			}
			for sessionID, repair := range authoritative {
				shadow := project.data[sessionID]
				shadow.LastAppliedTree = repair.AppliedTree
				shadow.LastAppliedAt = repair.AppliedAt
				shadow.LastAppliedTransactionID = repair.TransactionID
				repaired = true
			}
		}
		if repaired {
			updated, err := json.Marshal(instancesData)
			if err != nil {
				return fmt.Errorf("failed to marshal repaired instances: %w", err)
			}
			if err := s.state.SaveInstances(updated); err != nil {
				return fmt.Errorf("failed to save repaired instances: %w", err)
			}
		}

		instances = make([]*Instance, len(instancesData))
		for i, data := range instancesData {
			var access *workspace.AccessState
			var lease *workspace.ProjectRecoveryLease
			if data.Workspace != nil && data.Workspace.Mode == workspace.ModeDirectory && data.Workspace.Shadow != nil {
				projectPath := filepath.Dir(data.Workspace.Shadow.RepoPath)
				state := workspace.AccessStateFromRecovery(projects[projectPath].report)
				if len(informational[projectPath]) != 0 && state.Message == "" {
					state.Message = informational[projectPath][0].Message
				}
				access = &state
				lease = leases[projectPath]
			}
			instance, err := fromInstanceDataWithLease(data, access, lease)
			if err != nil {
				return fmt.Errorf("failed to create instance %s: %w", data.Title, err)
			}
			instances[i] = instance
		}
		if loadInstancePublishedHook != nil {
			loadInstancePublishedHook()
		}
		for _, projectPath := range projectPaths {
			sort.Slice(acknowledgements[projectPath], func(i, j int) bool {
				return acknowledgements[projectPath][i].TransactionID < acknowledgements[projectPath][j].TransactionID
			})
			for _, repair := range acknowledgements[projectPath] {
				if err := leases[projectPath].AcknowledgeApply(repair.TransactionID); err != nil {
					return fmt.Errorf("acknowledge transaction %s: %w", repair.TransactionID, err)
				}
			}
		}
		return nil
	}
	if err := recoverLocked(0); err != nil {
		return nil, err
	}
	return instances, nil
}

// DeleteInstance removes an instance from storage
func (s *Storage) DeleteInstance(title string) error {
	instances, err := s.LoadInstances()
	if err != nil {
		return fmt.Errorf("failed to load instances: %w", err)
	}

	found := false
	newInstances := make([]*Instance, 0)
	for _, instance := range instances {
		data := instance.ToInstanceData()
		if data.Title != title {
			newInstances = append(newInstances, instance)
		} else {
			found = true
		}
	}

	if !found {
		return fmt.Errorf("instance not found: %s", title)
	}

	return s.SaveInstances(newInstances)
}

// UpdateInstance updates an existing instance in storage
func (s *Storage) UpdateInstance(instance *Instance) error {
	instances, err := s.LoadInstances()
	if err != nil {
		return fmt.Errorf("failed to load instances: %w", err)
	}

	data := instance.ToInstanceData()
	found := false
	for i, existing := range instances {
		existingData := existing.ToInstanceData()
		if existingData.Title == data.Title {
			instances[i] = instance
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("instance not found: %s", data.Title)
	}

	return s.SaveInstances(instances)
}

// DeleteAllInstances removes all stored instances
func (s *Storage) DeleteAllInstances() error {
	return s.state.DeleteAllInstances()
}
