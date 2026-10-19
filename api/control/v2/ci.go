package v2

// GitHub Actions runner hosting (CTR-CTL-15, API 1.8). The kernel supervises the official runner
// processes; GitHub credentials and registration tokens are written once and never read back.

// CiInstanceState is the lifecycle state of one runner instance.
type CiInstanceState string

const (
	CiInstalling  CiInstanceState = "installing"
	CiRegistering CiInstanceState = "registering"
	CiStarting    CiInstanceState = "starting"
	CiIdle        CiInstanceState = "idle"
	CiBusy        CiInstanceState = "busy"
	CiBackoff     CiInstanceState = "backoff"
	CiStopping    CiInstanceState = "stopping"
	CiStopped     CiInstanceState = "stopped"
	CiBroken      CiInstanceState = "broken"
)

// CiProblemCode says why an instance needs attention.
type CiProblemCode string

const (
	CiProblemSessionConflict    CiProblemCode = "session_conflict"
	CiProblemRunnerRemoved      CiProblemCode = "runner_removed"
	CiProblemRegistrationFailed CiProblemCode = "registration_failed"
	CiProblemDownloadFailed     CiProblemCode = "download_failed"
	CiProblemMissingDependency  CiProblemCode = "missing_dependency"
	CiProblemStartFailed        CiProblemCode = "start_failed"
	CiProblemVersionDeprecated  CiProblemCode = "version_deprecated"
	CiProblemAuthFailed         CiProblemCode = "auth_failed"
	CiProblemUnclassified       CiProblemCode = "unclassified"
)

// CiResultKind is how a job ended, as the runner reports it.
type CiResultKind string

const (
	CiSucceeded CiResultKind = "succeeded"
	CiFailed    CiResultKind = "failed"
	CiCanceled  CiResultKind = "canceled"
	CiSkipped   CiResultKind = "skipped"
	CiAbandoned CiResultKind = "abandoned"
	CiUnknown   CiResultKind = "unknown"
)

// CiBindingSource is how a binding got its runner.
type CiBindingSource string

const (
	CiSourceAccount CiBindingSource = "account"
	CiSourceToken   CiBindingSource = "token"
	CiSourceImport  CiBindingSource = "import"
)

// CiNoticeCode names a condition that keeps runners from surviving a reboot or a logout.
type CiNoticeCode string

const (
	CiNoticeLingerDisabled    CiNoticeCode = "linger_disabled"
	CiNoticeAutologonDisabled CiNoticeCode = "autologon_disabled"
	CiNoticeGitMissing        CiNoticeCode = "git_missing"
	CiNoticeMissingDependency CiNoticeCode = "missing_dependency"
	CiNoticeRunnerUnsupported CiNoticeCode = "runner_unsupported"
)

type CiProblem struct {
	Code    CiProblemCode `json:"code"`
	Message string        `json:"message"`
}

type CiJob struct {
	Name      string `json:"name"`
	StartedAt Time   `json:"started_at"`
}

type CiResult struct {
	Name       string       `json:"name"`
	Result     CiResultKind `json:"result"`
	FinishedAt Time         `json:"finished_at"`
}

type CiInstance struct {
	Index      int             `json:"index"`
	Name       string          `json:"name"`
	State      CiInstanceState `json:"state"`
	PID        int             `json:"pid,omitempty"`
	Restarts   int             `json:"restarts"`
	Since      Time            `json:"since"`
	Job        *CiJob          `json:"job,omitempty"`
	LastResult *CiResult       `json:"last_result,omitempty"`
	Problem    *CiProblem      `json:"problem,omitempty"`
}

type CiBinding struct {
	ID          string          `json:"id"`
	Scope       string          `json:"scope"`
	Target      string          `json:"target"`
	Source      CiBindingSource `json:"source"`
	Account     string          `json:"account,omitempty"`
	Public      *bool           `json:"public,omitempty"`
	Concurrency int             `json:"concurrency"`
	Labels      []string        `json:"labels"`
	Paused      bool            `json:"paused"`
	Removing    bool            `json:"removing"`
	Dir         string          `json:"dir"`
	Instances   []CiInstance    `json:"instances"`
}

// CiAccount is a saved GitHub credential. TokenHint is a mask; the token itself never leaves the
// kernel.
type CiAccount struct {
	ID        string `json:"id"`
	Host      string `json:"host"`
	Login     string `json:"login"`
	TokenHint string `json:"token_hint"`
}

type CiNotice struct {
	Code    CiNoticeCode `json:"code"`
	Message string       `json:"message"`
	Command string       `json:"command,omitempty"`
}

type CiRunnerPackage struct {
	Version string `json:"version,omitempty"`
	Cached  bool   `json:"cached"`
}

// CiSummary is StatusResponse.ci.
type CiSummary struct {
	Bindings []CiBinding `json:"bindings"`
	Draining bool        `json:"draining,omitempty"`
}

// CiOverview is GET /v2/ci.
type CiOverview struct {
	Accounts              []CiAccount     `json:"accounts"`
	Bindings              []CiBinding     `json:"bindings"`
	Notices               []CiNotice      `json:"notices"`
	Runner                CiRunnerPackage `json:"runner"`
	PreventSleepWhileBusy bool            `json:"prevent_sleep_while_busy"`
	StopTimeoutS          int             `json:"stop_timeout_s"`
	Draining              bool            `json:"draining,omitempty"`
}

// CiAccountCreate is POST /v2/ci/accounts. Token appears in this request only.
type CiAccountCreate struct {
	Token string `json:"token"`
	Host  string `json:"host,omitempty"`
}

// CiReposQuery is the query string of GET /v2/ci/accounts/{id}/repos.
type CiReposQuery struct {
	Q    string `json:"q,omitempty"`
	Page string `json:"page,omitempty"`
}

type CiRepo struct {
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
	Binding  string `json:"binding,omitempty"`
}

type CiReposResponse struct {
	Repos    []CiRepo `json:"repos"`
	NextPage *string  `json:"next_page"`
}

// CiBindingCreate is POST /v2/ci/bindings. Exactly one of the three ways is used: Target with
// Account, Target with RegistrationToken, or ImportDir.
type CiBindingCreate struct {
	ID                string   `json:"id,omitempty"`
	Target            string   `json:"target,omitempty"`
	Account           string   `json:"account,omitempty"`
	RegistrationToken string   `json:"registration_token,omitempty"`
	ImportDir         string   `json:"import_dir,omitempty"`
	Concurrency       int      `json:"concurrency,omitempty"`
	Labels            []string `json:"labels,omitempty"`
	ConfirmPublic     bool     `json:"confirm_public,omitempty"`
}

// CiBindingPatch is PATCH /v2/ci/bindings/{id}; absent fields stay as they are.
type CiBindingPatch struct {
	Concurrency *int      `json:"concurrency,omitempty"`
	Labels      *[]string `json:"labels,omitempty"`
	Paused      *bool     `json:"paused,omitempty"`
	Account     *string   `json:"account,omitempty"`
}

// CiBindingRepair is POST /v2/ci/bindings/{id}/repair.
type CiBindingRepair struct {
	RegistrationToken string `json:"registration_token,omitempty"`
}

// CiDeleteQuery is the query string of DELETE /v2/ci/bindings/{id}.
type CiDeleteQuery struct {
	Force string `json:"force,omitempty"`
}

type CiImportInspectRequest struct {
	Dir string `json:"dir"`
}

type CiImportInspectResponse struct {
	Target           string   `json:"target"`
	RunnerName       string   `json:"runner_name"`
	Labels           []string `json:"labels"`
	ServiceInstalled bool     `json:"service_installed"`
	ServiceHint      string   `json:"service_hint,omitempty"`
	Running          bool     `json:"running"`
	Ephemeral        bool     `json:"ephemeral,omitempty"`
}
