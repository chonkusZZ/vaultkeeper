// Package proto defines the wire types shared by the manager and agents.
package proto

import "time"

// Version is the Vaultkeeper release (manager and agent are built together).
const Version = "0.2.0"

// Task kinds.
const (
	KindBackup    = "backup"
	KindTest      = "test"
	KindCopy      = "copy"
	KindSnapshots = "snapshots"
	KindPrune     = "prune"   // apply the restore-point limit now
	KindWake      = "wake"    // wake a backup target and wait until it is ready
	KindHook      = "hook"    // run a post-completion script
	KindForget    = "forget"  // delete one restore point
	KindPurge     = "purge"   // delete stored data (a repository or a mirror folder)
	KindMirror    = "mirror"  // raw file-for-file mirror (not a restic repository)
	KindLs        = "ls"      // browse a directory inside a restore point
	KindFind      = "find"    // search a restore point by file name
	KindDump      = "dump"    // stream a file/folder (zip) to the manager for browser download
	KindRestore   = "restore" // restore selected paths to disk
)

// Run / task statuses.
const (
	StatusQueued  = "queued"
	StatusRunning = "running"
	StatusSuccess = "success"
	StatusWarning = "warning"
	StatusFailed  = "failed"
)

// Log levels (ordered).
const (
	LevelDebug   = "debug"
	LevelInfo    = "info"
	LevelWarning = "warning"
	LevelError   = "error"
)

func LevelRank(l string) int {
	switch l {
	case LevelDebug:
		return 0
	case LevelInfo:
		return 1
	case LevelWarning:
		return 2
	case LevelError:
		return 3
	}
	return 1
}

// RegisterRequest is sent by a fresh agent using the enrolment token.
type RegisterRequest struct {
	Token string   `json:"token"`
	Name  string   `json:"name"`
	Roles []string `json:"roles"` // "source", "dest"
}

type RegisterResponse struct {
	AgentID string `json:"agent_id"`
	Secret  string `json:"secret"`
}

// Stats is reported on every poll (acts as heartbeat).
type Stats struct {
	Hostname      string           `json:"hostname"`
	OS            string           `json:"os"`
	Arch          string           `json:"arch"`
	Version       string           `json:"version"`
	ResticVersion string           `json:"restic_version"`
	Roles         []string         `json:"roles"`
	Name          string           `json:"name"`
	DataDir       string           `json:"data_dir,omitempty"`
	DiskTotal     uint64           `json:"disk_total"`
	DiskFree      uint64           `json:"disk_free"`
	RepoSizes     map[string]int64 `json:"repo_sizes,omitempty"`
	// Destination agents only.
	Advertise string `json:"advertise,omitempty"` // base URL other agents use to reach the data endpoint
	CertPEM   string `json:"cert_pem,omitempty"`
	Running   int    `json:"running"`
	// Destination agents: where raw mirrors live and how much room is left there.
	MirrorRoot       string `json:"mirror_root,omitempty"`
	Listen           string `json:"listen,omitempty"`
	ConfigRev        int64  `json:"config_rev,omitempty"`        // last remote config revision applied
	AdvertiseSetting string `json:"advertise_setting,omitempty"` // configured advertise URL ("" = auto)
	MirrorTotal      uint64 `json:"mirror_total,omitempty"`
	MirrorFree       uint64 `json:"mirror_free,omitempty"`
	AllowScripts     bool   `json:"allow_scripts,omitempty"` // agent was started with --allow-scripts
}

// AgentConfig is configuration the manager can push to an agent. The agent
// persists it and restarts itself to apply it.
type AgentConfig struct {
	Roles      []string `json:"roles"`
	Listen     string   `json:"listen"`
	Advertise  string   `json:"advertise"`
	MirrorRoot string   `json:"mirror_root"`
	Rev        int64    `json:"rev"` // increases with every change or restart request
}

type PollRequest struct {
	Stats  Stats `json:"stats"`
	NoWait bool  `json:"no_wait,omitempty"` // return immediately (first poll after start)
}

// PollResponse carries optional config + an optional task.
type PollResponse struct {
	DataToken string       `json:"data_token,omitempty"` // credential for the agent's own data endpoint
	Config    *AgentConfig `json:"config,omitempty"`     // desired configuration, when it differs
	Task      *Task        `json:"task,omitempty"`
}

// Repo describes how to reach a restic repository.
type Repo struct {
	// Local is set when the executing agent holds the repo on disk.
	Local string `json:"local,omitempty"`
	// URL is set for a remote agent's REST endpoint (e.g. https://host:8765/repoName).
	URL      string `json:"url,omitempty"`
	CertPEM  string `json:"cert_pem,omitempty"`
	Token    string `json:"token,omitempty"` // REST basic-auth password
	Password string `json:"password"`        // restic repository (encryption) password
}

type Mount struct {
	Type     string `json:"type"` // smb | nfs
	Remote   string `json:"remote"`
	Options  string `json:"options,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Domain   string `json:"domain,omitempty"`
}

// Explore carries the parameters of explorer tasks (ls/find/dump/restore).
type Explore struct {
	Snapshot  string   `json:"snapshot,omitempty"`
	Path      string   `json:"path,omitempty"`      // ls: directory; dump: item
	IsDir     bool     `json:"is_dir,omitempty"`    // dump: folder => zip archive
	Query     string   `json:"query,omitempty"`     // find: file name pattern
	Paths     []string `json:"paths,omitempty"`     // restore: items to restore
	Original  bool     `json:"original,omitempty"`  // restore to original location
	Target    string   `json:"target,omitempty"`    // restore: new location
	Overwrite string   `json:"overwrite,omitempty"` // always|if-changed|if-newer|never
	Mirror    bool     `json:"mirror,omitempty"`    // purge: Path is a mirror folder rather than a repository name
	Name      string   `json:"name,omitempty"`      // purge: job name, for the audit log and alerts
	// Mirror runs
	DryRun bool `json:"dry_run,omitempty"`
	Force  bool `json:"force_delete,omitempty"` // bypass the large-deletion guard
}

// MirrorDest says how the source agent reaches the destination mirror folder.
type MirrorDest struct {
	Local   bool   `json:"local,omitempty"` // destination is on the executing agent itself
	URL     string `json:"url,omitempty"`
	CertPEM string `json:"cert_pem,omitempty"`
	Token   string `json:"token,omitempty"`
}

// WakeSpec describes how to wake a backup target (e.g. a NAS) and when it counts as ready.
type WakeSpec struct {
	Method    string `json:"method"`              // wol | command
	MAC       string `json:"mac,omitempty"`       // wol: target MAC address
	Broadcast string `json:"broadcast,omitempty"` // wol: broadcast address[:port] (default: all interfaces, port 9)
	Command   string `json:"command,omitempty"`   // command: custom wake command (needs --allow-scripts)

	Ready        string `json:"ready"`                   // browse | ping | tcp | command
	Host         string `json:"host,omitempty"`          // ping, tcp
	Port         int    `json:"port,omitempty"`          // tcp
	Path         string `json:"path,omitempty"`          // browse: folder on the waking agent that must become listable
	Marker       string `json:"marker,omitempty"`        // browse: file that must exist inside Path
	TryMount     bool   `json:"try_mount,omitempty"`     // browse: run `mount <Path>` while waiting (needs an fstab entry)
	ReadyCommand string `json:"ready_command,omitempty"` // command: exit status 0 means ready (needs --allow-scripts)
	SettleSec    int    `json:"settle_sec,omitempty"`    // extra seconds to wait once ready
	TimeoutMin   int    `json:"timeout_min"`             // give up after this long
}

// HookSpec is a script to run on an agent once a job has finished.
type HookSpec struct {
	Script     string            `json:"script"`
	TimeoutSec int               `json:"timeout_sec"`
	Env        map[string]string `json:"env,omitempty"`
}

// MirrorSpec is the configuration of one mirror run.
type MirrorSpec struct {
	SourcePath       string     `json:"source_path"`
	Mount            *Mount     `json:"mount,omitempty"`
	Excludes         []string   `json:"excludes,omitempty"`
	DestPath         string     `json:"dest_path"`
	Dest             MirrorDest `json:"dest"`
	Compare          string     `json:"compare"` // mtime | checksum
	Workers          int        `json:"workers"`
	PropagateDeletes bool       `json:"propagate_deletes"`
	MaxDeletePct     int        `json:"max_delete_pct"`
	BandwidthKB      int        `json:"bandwidth_kb,omitempty"`
	PreserveOwner    bool       `json:"preserve_owner,omitempty"`
	OwnerByName      bool       `json:"owner_by_name,omitempty"`
	PreserveACLs     bool       `json:"preserve_acls,omitempty"`
	DryRun           bool       `json:"dry_run,omitempty"`
	ForceDelete      bool       `json:"force_delete,omitempty"`
}

type Task struct {
	RunID string `json:"run_id"`
	Kind  string `json:"kind"`
	Repo  Repo   `json:"repo"`

	// backup
	JobID       string   `json:"job_id"`
	Paths       []string `json:"paths,omitempty"`
	Excludes    []string `json:"excludes,omitempty"`
	Mount       *Mount   `json:"mount,omitempty"`
	Compression string   `json:"compression,omitempty"` // auto|max|off
	KeepLast    int      `json:"keep_last,omitempty"`
	BandwidthKB int      `json:"bandwidth_kb,omitempty"`
	PickTest    bool     `json:"pick_test,omitempty"` // choose a random file after backup

	// test
	TestPath     string `json:"test_path,omitempty"`
	TestRandom   bool   `json:"test_random,omitempty"`
	CheckDataPct int    `json:"check_data_pct,omitempty"`

	Explore *Explore    `json:"explore,omitempty"`
	Mirror  *MirrorSpec `json:"mirror,omitempty"`
	Wake    *WakeSpec   `json:"wake,omitempty"`
	Hook    *HookSpec   `json:"hook,omitempty"`

	// copy
	From *Repo `json:"from,omitempty"` // source repo (local to executing agent)
}

type LogLine struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

type LogBatch struct {
	Lines []LogLine `json:"lines"`
}

type Snapshot struct {
	ID    string    `json:"id"`
	Time  time.Time `json:"time"`
	Host  string    `json:"hostname"`
	Paths []string  `json:"paths"`
	Tags  []string  `json:"tags"`
}

// Result is the final report for a task.
type Result struct {
	Status   string         `json:"status"` // success|warning|failed
	Summary  map[string]any `json:"summary,omitempty"`
	TestFile string         `json:"test_file,omitempty"` // chosen test file (backup w/ PickTest, or re-pick on test)
	Message  string         `json:"message,omitempty"`
}
