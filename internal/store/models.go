package store

import "vaultkeeper/internal/proto"

// Document kinds.
const (
	KindAgent     = "agent"
	KindJob       = "job"
	KindCopy      = "copy"
	KindMirrorJob = "mirrorjob"
	KindSettings  = "settings"
)

type Agent struct {
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	SecretHash       string             `json:"secret_hash"`
	DataToken        string             `json:"data_token"`
	Roles            []string           `json:"roles"`
	Hostname         string             `json:"hostname"`
	OS               string             `json:"os"`
	Arch             string             `json:"arch"`
	Version          string             `json:"version"`
	Restic           string             `json:"restic_version"`
	Advertise        string             `json:"advertise"`
	CertPEM          string             `json:"cert_pem"`
	DataDir          string             `json:"data_dir"`
	DiskTotal        uint64             `json:"disk_total"`
	DiskFree         uint64             `json:"disk_free"`
	RepoSizes        map[string]int64   `json:"repo_sizes"`
	Running          int                `json:"running"`
	MirrorRoot       string             `json:"mirror_root"`
	Listen           string             `json:"listen"`
	AdvertiseSetting string             `json:"advertise_setting"`
	ConfigRev        int64              `json:"config_rev"`
	Desired          *proto.AgentConfig `json:"desired,omitempty"` // pending remote configuration
	LowDiskNotified  bool               `json:"low_disk_notified"`
	MirrorTotal      uint64             `json:"mirror_total"`
	AllowScripts     bool               `json:"allow_scripts"`
	MirrorFree       uint64             `json:"mirror_free"`
	LastSeen         int64              `json:"last_seen"`
	Created          int64              `json:"created"`
	// Online is derived, not stored.
	Online bool `json:"online"`
	// OfflineNotified suppresses repeat alerts.
	OfflineNotified bool `json:"offline_notified"`
}

func (a *Agent) Has(role string) bool {
	for _, r := range a.Roles {
		if r == role {
			return true
		}
	}
	return false
}

type Job struct {
	Hooks        *Hooks       `json:"hooks,omitempty"`
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Enabled      bool         `json:"enabled"`
	SourceAgent  string       `json:"source_agent"`
	DestAgent    string       `json:"dest_agent"`
	Paths        []string     `json:"paths"`
	Excludes     []string     `json:"excludes"`
	Mount        *proto.Mount `json:"mount,omitempty"`
	Schedule     string       `json:"schedule"` // cron expression, "" = manual only
	KeepLast     int          `json:"keep_last"`
	Compression  string       `json:"compression"` // auto|max|off
	BandwidthKB  int          `json:"bandwidth_kb"`
	RepoName     string       `json:"repo_name"`
	RepoPassword string       `json:"repo_password,omitempty"`
	Initialized  bool         `json:"initialized"`
	TestMode     string       `json:"test_mode"` // random|manual|off
	TestPath     string       `json:"test_path"`
	TestSchedule string       `json:"test_schedule"`
	CheckDataPct int          `json:"check_data_pct"`
	Created      int64        `json:"created"`
}

// Hooks are optional steps around a job: wake the backup target first, run a
// script afterwards.
type Hooks struct {
	Target string `json:"target,omitempty"` // optional label shared by jobs that use the same target
	Wake   *Wake  `json:"wake,omitempty"`
	Post   *Post  `json:"post,omitempty"`
}

type Wake struct {
	Enabled bool   `json:"enabled"`
	Agent   string `json:"agent"` // the agent that sends the wake-up and checks readiness
	proto.WakeSpec
}

type Post struct {
	Enabled    bool   `json:"enabled"`
	Agent      string `json:"agent"` // the agent that runs the script
	Script     string `json:"script"`
	On         string `json:"on"` // success (incl. warnings) | always
	TimeoutSec int    `json:"timeout_sec"`
}

type CopyJob struct {
	Hooks        *Hooks `json:"hooks,omitempty"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	JobID        string `json:"job_id"`
	DestAgent    string `json:"dest_agent"`
	Schedule     string `json:"schedule"`
	KeepLast     int    `json:"keep_last"`
	RepoName     string `json:"repo_name"`
	RepoPassword string `json:"repo_password,omitempty"`
	Created      int64  `json:"created"`
}

type Settings struct {
	AdminHash     string `json:"admin_hash"`
	SessionSecret string `json:"session_secret"`
	EnrollToken   string `json:"enroll_token"`

	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port"`
	SMTPSecurity string `json:"smtp_security"` // none|starttls|tls
	SMTPUser     string `json:"smtp_user"`
	SMTPPass     string `json:"smtp_pass,omitempty"`
	SMTPFrom     string `json:"smtp_from"`
	SMTPTo       string `json:"smtp_to"`
	// EmailLevel is the minimum severity that triggers an email:
	// info (everything) | warning | error | never
	EmailLevel string `json:"email_level"`

	LogRetentionDays int `json:"log_retention_days"`

	SettingsRev  int `json:"settings_rev"`
	DiskAlertPct int `json:"disk_alert_pct"` // email when a destination has less than this % free (0 = off)

	// Automatic encrypted export of the manager's configuration.
	AutoBackup        bool   `json:"auto_backup"`
	AutoBackupDir     string `json:"auto_backup_dir"`
	AutoBackupKeep    int    `json:"auto_backup_keep"`
	AutoBackupPass    string `json:"auto_backup_pass,omitempty"`
	LastAutoBackup    int64  `json:"last_auto_backup"`
	LastAutoBackupMsg string `json:"last_auto_backup_msg"`
}

// MirrorJob keeps a destination folder an exact file-for-file copy of a source
// folder (deletions included), unlike restic jobs which keep restore points.
type MirrorJob struct {
	Hooks            *Hooks       `json:"hooks,omitempty"`
	ID               string       `json:"id"`
	Name             string       `json:"name"`
	Enabled          bool         `json:"enabled"`
	SourceAgent      string       `json:"source_agent"`
	SourcePath       string       `json:"source_path"`
	Mount            *proto.Mount `json:"mount,omitempty"`
	DestAgent        string       `json:"dest_agent"`
	DestPath         string       `json:"dest_path"` // relative to the destination agent's mirror root
	Excludes         []string     `json:"excludes"`
	Schedule         string       `json:"schedule"`
	Compare          string       `json:"compare"` // mtime | checksum
	Workers          int          `json:"workers"`
	PropagateDeletes bool         `json:"propagate_deletes"`
	MaxDeletePct     int          `json:"max_delete_pct"`
	PreserveOwner    bool         `json:"preserve_owner"`
	OwnerMap         string       `json:"owner_map"` // numeric | names
	PreserveACLs     bool         `json:"preserve_acls"`
	BandwidthKB      int          `json:"bandwidth_kb"`
	Created          int64        `json:"created"`
}
